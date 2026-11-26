package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

var weekdays = []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

func (s *Service) now() time.Time {
	t := s.Clock.Now()
	if s.TZ != nil {
		t = t.In(s.TZ)
	}
	return t
}

func dateText(t time.Time) string {
	return fmt.Sprintf("%s（%s）", t.Format("2006-01-02"), weekdays[t.Weekday()])
}

// connSummary 当前可见连接的摘要（每行一个）。
func connSummary(env *tools.Env) string {
	list, err := env.Visible()
	if err != nil || len(list) == 0 {
		return "（没有可用的连接）"
	}
	var b strings.Builder
	for _, a := range list {
		c := a.Conn
		e := c.Env
		if e == "" {
			e = "-"
		}
		fmt.Fprintf(&b, "  - %s「%s」%s，环境 %s，AI 档位：%s", c.ID, c.Name, c.Driver, e, tools.Label(a.AI))
		if len(a.Allow) > 0 {
			b.WriteString("，白名单 " + strings.Join(a.Allow, ", "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func tabsText(cc *ClientContext) string {
	if cc == nil || len(cc.OpenTabs) == 0 {
		return ""
	}
	var parts []string
	for i, t := range cc.OpenTabs {
		if i >= 15 {
			parts = append(parts, fmt.Sprintf("…共 %d 个标签", len(cc.OpenTabs)))
			break
		}
		desc := t.Kind
		if t.Table != "" {
			desc += " " + strings.Trim(t.DB+"."+t.Table, ".")
		} else if t.Title != "" {
			desc += " " + t.Title
		}
		if t.ConnectionID != "" {
			desc += "（连接 " + t.ConnectionID + "）"
		}
		if t.ID == cc.ActiveTab {
			desc += " ← 当前"
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", t.ID, desc))
	}
	return strings.Join(parts, "；")
}

func modeText(mode string) string {
	if mode == ModePlan {
		return "计划模式（只读分析：可以查询与分析，不能执行任何写入或 DDL；先给出方案，等用户确认后再执行）"
	}
	return "普通模式（写入按各连接的 AI 档位确认）"
}

// currentFacts 计算环境块里会变的部分。
func (s *Service) currentFacts(env *tools.Env, conv *Conversation, cc *ClientContext, keepTabs bool) EnvFacts {
	f := EnvFacts{Date: dateText(s.now()), Mode: conv.Cfg.Mode, Conns: connSummary(env)}
	if cc != nil {
		f.Tabs = tabsText(cc)
	} else if keepTabs {
		f.Tabs = conv.St.Env.Tabs
	}
	return f
}

// buildSystem 会话开始时固定的系统提示：基础提示 + 环境块 + 空间与个人自定义指令。
func (s *Service) buildSystem(ctx context.Context, env *tools.Env, conv *Conversation, u *accounts.User, facts EnvFacts) string {
	var b strings.Builder
	b.WriteString(basePrompt)
	now := s.now()
	b.WriteString("\n# 环境\n")
	fmt.Fprintf(&b, "- 日期：%s，时区 %s（UTC%s）\n", facts.Date, now.Location().String(), now.Format("-07:00"))
	name := u.Name()
	fmt.Fprintf(&b, "- 用户：%s（@%s）\n", name, u.Username)
	if ws, err := s.acc.Workspace(ctx, conv.WorkspaceID); err == nil {
		fmt.Fprintf(&b, "- 工作空间：%s（%s）\n", ws.Name, ws.ID)
	}
	fmt.Fprintf(&b, "- 模式：%s\n", modeText(facts.Mode))
	b.WriteString("- 可用连接：\n" + facts.Conns + "\n")
	if facts.Tabs != "" {
		b.WriteString("- 用户当前打开的标签：" + facts.Tabs + "\n")
	}
	if w := s.wsAI(ctx, conv.WorkspaceID); strings.TrimSpace(w.Instructions) != "" {
		b.WriteString("\n# 工作空间的约定\n" + strings.TrimSpace(w.Instructions) + "\n")
	}
	var ui struct {
		Instructions string `json:"instructions"`
	}
	if ok, _ := s.set.GetJSON(ctx, "user:"+u.ID+":ai", &ui); ok && strings.TrimSpace(ui.Instructions) != "" {
		b.WriteString("\n# 用户的个人指令\n" + strings.TrimSpace(ui.Instructions) + "\n")
	}
	if conv.Kind == KindTask {
		b.WriteString(taskPrompt)
	}
	return b.String()
}

// systemUpdate 环境变化时生成 <system-update> 文本（没有变化返回空）。
func systemUpdate(old, cur EnvFacts) string {
	var lines []string
	if old.Date != "" && cur.Date != old.Date {
		lines = append(lines, "日期变为 "+cur.Date+"。")
	}
	if old.Mode != "" && cur.Mode != old.Mode {
		if cur.Mode == ModePlan {
			lines = append(lines, "进入计划模式：现在只能做只读分析，不能执行任何写入或 DDL；请先给出方案，等用户确认后再执行。")
		} else {
			lines = append(lines, "退出计划模式：现在可以按各连接的 AI 档位执行写入（需要确认的会弹审批）。")
		}
	}
	if cur.Conns != old.Conns && old.Conns != "" {
		lines = append(lines, "可用连接有变化（档位、白名单或连接列表），现在是：\n"+cur.Conns)
	}
	if cur.Tabs != old.Tabs && cur.Tabs != "" {
		lines = append(lines, "用户当前打开的标签："+cur.Tabs)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<system-update>\n" + strings.Join(lines, "\n") + "\n</system-update>"
}

// ---------- 回放 ----------

// toLLM 把存储的消息转成模型消息：摘要替换为 <conversation-checkpoint>；没执行的工具调用（含服务重启时丢失的）丢掉；
// 尾部没有输出的 reasoning 丢掉；相邻的用户消息合并。
func toLLM(conv *Conversation, msgs []Message) []llm.Message {
	results := map[string]string{} // call_id → 状态
	for _, m := range msgs {
		if m.Role == "tool" {
			for _, p := range m.Parts {
				if p.Type == "tool_result" {
					results[p.CallID] = p.Status
				}
			}
		}
	}
	var out []llm.Message
	if conv.St.Summary != "" {
		out = append(out, llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: "<conversation-checkpoint>\n以下是此前对话的工作摘要，更早的详细记录已压缩：\n\n" +
			conv.St.Summary + "\n</conversation-checkpoint>"}}})
	}
	dropped := map[string]bool{}
	for _, m := range msgs {
		if m.Seq <= conv.St.SummaryUpTo {
			continue
		}
		var parts []llm.Part
		switch m.Role {
		case "assistant":
			for _, p := range m.Parts {
				if p.Type == "tool_call" {
					st, ok := results[p.CallID]
					if !ok || st == StatusLost {
						dropped[p.CallID] = true
						continue
					}
				}
				if p.Type == "text" && strings.TrimSpace(p.Text) == "" {
					continue
				}
				parts = append(parts, p.Part)
			}
			// 尾部没有输出的 reasoning 丢掉
			for len(parts) > 0 && parts[len(parts)-1].Type == "reasoning" {
				parts = parts[:len(parts)-1]
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, llm.Message{Role: llm.RoleAssistant, Parts: parts})
		case "tool":
			for _, p := range m.Parts {
				if p.Type == "tool_result" && (dropped[p.CallID] || p.Status == StatusLost) {
					continue
				}
				parts = append(parts, p.Part)
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, llm.Message{Role: llm.RoleTool, Parts: parts})
		default:
			for _, p := range m.Parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) == "" {
					continue
				}
				parts = append(parts, p.Part)
			}
			if len(parts) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == llm.RoleUser {
				out[n-1].Parts = append(out[n-1].Parts, parts...)
			} else {
				out = append(out, llm.Message{Role: llm.RoleUser, Parts: parts})
			}
		}
	}
	return out
}

// ---------- token 估算 ----------

// estimateTokens 粗略估算：中日韩字符约 1 个 1 token，其余约 4 字节 1 token。
func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other += len(string(r))
		}
	}
	return cjk + other/4 + 1
}

func estimateParts(ps []llm.Part) int {
	n := 0
	for _, p := range ps {
		n += estimateTokens(p.Text) + estimateTokens(string(p.Args)) + 4
	}
	return n
}

func estimateMessages(ms []llm.Message) int {
	n := 0
	for _, m := range ms {
		n += estimateParts(m.Parts) + 3
	}
	return n
}

func estimateStored(ms []Message) int {
	n := 0
	for _, m := range ms {
		for _, p := range m.Parts {
			n += estimateTokens(p.Text) + estimateTokens(string(p.Args)) + 4
		}
		n += 3
	}
	return n
}

func estimateRequest(req llm.Request) int {
	n := estimateTokens(req.System) + estimateMessages(req.Messages)
	for _, t := range req.Tools {
		n += estimateTokens(t.Description) + estimateTokens(string(t.Schema)) + 10
	}
	return n
}

// contextLimit 上下文上限 = 窗口 − max(15% 窗口, 16k)。
func contextLimit(window int) int {
	if window <= 0 {
		window = 128000
	}
	reserve := window * 15 / 100
	if reserve < 16000 {
		reserve = 16000
	}
	if window-reserve < 8000 {
		return window * 3 / 4
	}
	return window - reserve
}

// estimateContext 估算 = 上次实测 + 之后新增内容的估算（没有实测时整体估算）。
func estimateContext(conv *Conversation, msgs []Message, fixed int) int {
	if conv.St.LastContext > 0 {
		var newer []Message
		for _, m := range msgs {
			if m.Seq > conv.St.LastContextSeq {
				newer = append(newer, m)
			}
		}
		return conv.St.LastContext + estimateStored(newer)
	}
	return fixed + estimateMessages(toLLM(conv, msgs))
}
