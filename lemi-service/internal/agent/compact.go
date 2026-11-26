package agent

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
)

// KeepRecentTokens 压缩时保留的最近原文（约 15k token）。
var KeepRecentTokens = 15000

var summaryHeadings = []string{"## 目标", "## 要求", "## 决定", "## 进展", "### 已完成", "### 进行中", "### 受阻", "## 下一步", "## 相关对象", "## 重要上下文"}

// validSummary 摘要是否符合模板。
func validSummary(s string) bool {
	for _, h := range summaryHeadings {
		if !strings.Contains(s, h) {
			return false
		}
	}
	return true
}

// collectText 请求模型并收集文字（压缩、标题用），没有输出之前的可重试错误按策略重试。
func (s *Service) collectText(ctx context.Context, prov llm.Provider, req llm.Request) (string, llm.Usage, error) {
	var st retryState
	for {
		text, usage, err := func() (string, llm.Usage, error) {
			stream, err := prov.Stream(ctx, req)
			if err != nil {
				return "", llm.Usage{}, err
			}
			defer stream.Close()
			var b strings.Builder
			var u llm.Usage
			for {
				ev, err := stream.Recv()
				if err == io.EOF {
					return b.String(), u, nil
				}
				if err != nil {
					return "", u, err
				}
				switch ev.Type {
				case llm.EvText:
					b.WriteString(ev.Text)
				case llm.EvUsage:
					u = ev.Usage
				}
			}
		}()
		if err == nil {
			return text, usage, nil
		}
		ae := llm.Classify(err)
		if ctx.Err() != nil {
			return "", usage, ctx.Err()
		}
		d, ok := s.Retry.next(ae, &st, s.Clock.Now())
		if !ok {
			return "", usage, ae
		}
		if err := s.Clock.Sleep(ctx, d); err != nil {
			return "", usage, err
		}
	}
}

// compact 把较早的对话压缩成摘要：保留最近约 15k token 原文（target 更小时按比例减少），其余按固定模板写成
// <conversation-checkpoint>；已有旧摘要时合并。模板不合格补发一次提示，再不合格判失败。
func (s *Service) compact(ctx context.Context, run *Run, conv *Conversation, prov llm.Provider, mdl llm.Model, msgs []Message, target int, reason string) error {
	conv.St.Compacting = true
	_ = s.saveState(context.WithoutCancel(ctx), conv)
	run.emit("compaction_start", map[string]any{"reason": reason})
	ok := false
	defer func() {
		conv.St.Compacting = false
		_ = s.saveState(context.WithoutCancel(ctx), conv)
		if !ok {
			run.emit("compaction_done", map[string]any{"ok": false})
		}
	}()
	var active []Message
	for _, m := range msgs {
		if m.Seq > conv.St.SummaryUpTo {
			active = append(active, m)
		}
	}
	keep := KeepRecentTokens
	if target > 0 && target*30/100 < keep {
		keep = target * 30 / 100
	}
	if keep < 1000 {
		keep = 1000
	}
	b := len(active)
	acc := 0
	for i := len(active) - 1; i >= 0; i-- {
		acc += estimateStored(active[i : i+1])
		if acc > keep {
			break
		}
		b = i
	}
	// 切分点不能落在工具结果上（工具调用与结果必须在同一侧）
	for b < len(active) && active[b].Role == "tool" {
		b++
	}
	if b == 0 {
		// 最近的内容本身就超过保留量：至少压缩到最后一条消息之前
		b = len(active) - 1
		for b > 0 && active[b].Role == "tool" {
			b--
		}
	}
	if b <= 0 {
		return errors.New("没有可压缩的历史")
	}
	older := active[:b]
	// 压缩输入：长工具结果截短，整体放不进窗口时丢掉最早的部分
	trimmed := make([]Message, len(older))
	for i, m := range older {
		cp := m
		cp.Parts = make([]StoredPart, len(m.Parts))
		for j, p := range m.Parts {
			if p.Type == "tool_result" && len(p.Text) > 3000 {
				p.Text = p.Text[:3000] + "…（已截短）"
			}
			cp.Parts[j] = p
		}
		trimmed[i] = cp
	}
	in := toLLM(&Conversation{St: ConvState{Summary: conv.St.Summary}}, trimmed)
	budget := contextLimit(mdl.Context) - 12000
	if budget < 4000 {
		budget = 4000
	}
	for len(in) > 2 && estimateMessages(in) > budget {
		// 保留开头的旧摘要（如果有），丢掉其后最早的一条
		drop := 0
		if conv.St.Summary != "" {
			drop = 1
		}
		in = append(in[:drop], in[drop+1:]...)
		for len(in) > drop && in[drop].Role == llm.RoleTool {
			in = append(in[:drop], in[drop+1:]...)
		}
	}
	in = append(in, llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: compactPrompt}}})
	req := llm.Request{Model: mdl.ID, System: compactSystem, Messages: in, MaxTokens: 8000, SessionID: conv.ID}
	text, usage, err := s.collectText(ctx, prov, req)
	conv.Usage.Requests++
	conv.Usage.Input += usage.Input
	conv.Usage.Output += usage.Output
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if !validSummary(text) {
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: nonEmpty(text)}}},
			llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: compactRetryPrompt}}})
		text, usage, err = s.collectText(ctx, prov, req)
		conv.Usage.Requests++
		conv.Usage.Input += usage.Input
		conv.Usage.Output += usage.Output
		if err != nil {
			return err
		}
		text = strings.TrimSpace(text)
		if !validSummary(text) {
			return errors.New("模型给出的摘要不符合模板")
		}
	}
	conv.St.Summary = text
	conv.St.SummaryUpTo = older[len(older)-1].Seq
	conv.St.LastContext, conv.St.LastContextSeq = 0, 0
	ok = true
	run.emit("compaction_done", map[string]any{"ok": true, "summary": truncateRunes(text, 3000), "upto": conv.St.SummaryUpTo})
	return nil
}

func nonEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（空）"
	}
	return s
}
