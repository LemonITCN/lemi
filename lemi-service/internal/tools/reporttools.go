package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/reports"
)

func init() {
	r := Default
	r.Register(&Tool{
		Name: "report_write", Title: "写报告", Kind: KindLemi,
		Description: "创建或修改分析报告（保存在工作空间里，成员都能看到，可以导出与分享）。正文是 Markdown，可以插入引用块（单独成行）：" +
			"```lemi-chart <图表ID>```、```lemi-table <结果ID>```、```lemi-diagram <图ID>```，保存时服务端把引用的数据做快照。" +
			"不传 report_id 时新建（title 与 body 必填）；传 report_id 时可以整体替换 body、append 追加一段、或用 edits 做精确替换（old 必须在正文里恰好出现一次）。" +
			"报告结构建议：结论摘要 → 关键数据与图表 → 分析过程 → 建议；结论要有数据支撑。",
		Schema: obj(map[string]any{
			"report_id": str("可选：要修改的报告 ID"),
			"title":     str("报告标题（新建时必填）"),
			"body":      str("Markdown 正文（新建时必填；修改时传入则整体替换）"),
			"append":    str("可选：追加到正文末尾的内容"),
			"edits": arr(obj(map[string]any{
				"old": str("要替换的原文（逐字复制）"),
				"new": str("替换成的内容"),
			}, "old", "new"), "可选：精确替换"),
		}),
		Handler: reportWrite,
	})
	r.Register(&Tool{
		Name: "report_read", Title: "读报告", Kind: KindRead,
		Description: "读取报告。不传 report_id 时列出工作空间里最近的报告；传 report_id 时返回标题、Markdown 正文与引用块的快照状态。",
		Schema: obj(map[string]any{
			"report_id": str("可选：报告 ID"),
			"limit":     integer("列表时最多几条（默认 30）"),
		}),
		Handler: reportRead,
	})
	r.Alias("write_report", "report_write")
	r.Alias("report", "report_write")
	r.Alias("read_report", "report_read")
}

func reportWrite(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ReportID string `json:"report_id"`
		Title    string `json:"title"`
		Body     *string `json:"body"`
		Append   string `json:"append"`
		Edits    []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"edits"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	if _, err := env.Svc.Accounts.Require(env.Ctx, env.WorkspaceID, env.User, accounts.WSMember); err != nil {
		return nil, fail("你在这个工作空间里没有写报告的权限（需要成员及以上）")
	}
	var warns []string
	var rp *reports.Report
	created := false
	if strings.TrimSpace(in.ReportID) == "" {
		body := ""
		if in.Body != nil {
			body = *in.Body
		}
		if in.Append != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + in.Append
		}
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(body) == "" {
			return nil, fail("新建报告需要 title 与 body")
		}
		var err error
		rp, warns, err = env.Svc.Reports.Create(env.Ctx, env.WorkspaceID, env.User, in.Title, body, env.ConvID, env.TaskID)
		if err != nil {
			return nil, fail("%s", err.Error())
		}
		created = true
	} else {
		var err error
		rp, err = env.Svc.Reports.Get(env.Ctx, strings.ToLower(strings.TrimSpace(in.ReportID)))
		if err != nil || rp.WorkspaceID != env.WorkspaceID {
			return nil, fail("报告 %s 不存在", in.ReportID)
		}
		body := rp.Body
		if in.Body != nil {
			body = *in.Body
		}
		for i, e := range in.Edits {
			n := strings.Count(body, e.Old)
			if e.Old == "" || n == 0 {
				return nil, fail("edits[%d].old 在正文里找不到（必须逐字复制，先用 report_read 读最新正文）", i)
			}
			if n > 1 {
				return nil, fail("edits[%d].old 在正文里出现了 %d 次，请多带一些上下文使它唯一", i, n)
			}
			body = strings.Replace(body, e.Old, e.New, 1)
		}
		if in.Append != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + in.Append
		}
		warns, err = env.Svc.Reports.Update(env.Ctx, rp, env.User, in.Title, body)
		if err != nil {
			return nil, fail("%s", err.Error())
		}
	}
	action := "report.update"
	if created {
		action = "report.create"
	}
	env.Svc.Audit.Record(audit.Entry{UserID: env.User.ID, Username: env.User.Username, WorkspaceID: env.WorkspaceID, Source: env.Source, Action: action, Target: rp.ID, IP: env.IP, OK: true},
		map[string]any{"title": rp.Title})
	verb := "已更新"
	if created {
		verb = "已创建"
	}
	text := fmt.Sprintf("报告%s：%s「%s」（%d 个引用块）。", verb, rp.ID, rp.Title, len(reports.ParseBlocks(rp.Body)))
	if len(warns) > 0 {
		text += "\n注意：" + strings.Join(warns, "；")
	}
	return &Result{Text: text, Data: map[string]any{"report_id": rp.ID, "title": rp.Title, "created": created, "warnings": warns}}, nil
}

func reportRead(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ReportID string `json:"report_id"`
		Limit    int    `json:"limit"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ReportID) == "" {
		if in.Limit <= 0 || in.Limit > 200 {
			in.Limit = 30
		}
		list, err := env.Svc.Reports.List(env.Ctx, env.WorkspaceID, in.Limit)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return &Result{Text: "工作空间里还没有报告。"}, nil
		}
		var b strings.Builder
		for _, r := range list {
			fmt.Fprintf(&b, "- %s「%s」更新于 %s\n", r.ID, r.Title, fmtTime(r.UpdatedAt))
		}
		return &Result{Text: b.String()}, nil
	}
	rp, err := env.Svc.Reports.Get(env.Ctx, strings.ToLower(strings.TrimSpace(in.ReportID)))
	if err != nil || rp.WorkspaceID != env.WorkspaceID {
		return nil, fail("报告 %s 不存在", in.ReportID)
	}
	sn := rp.Decode()
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n（报告 %s，更新于 %s）\n\n%s\n", rp.Title, rp.ID, fmtTime(rp.UpdatedAt), rp.Body)
	blocks := reports.ParseBlocks(rp.Body)
	if len(blocks) > 0 {
		b.WriteString("\n---\n引用块：\n")
		for _, bl := range blocks {
			ok := false
			switch bl.Kind {
			case "chart":
				_, ok = sn.Charts[bl.ID]
			case "table":
				_, ok = sn.Tables[bl.ID]
			case "diagram":
				_, ok = sn.Diagrams[bl.ID]
			}
			st := "已快照"
			if !ok {
				st = "缺失"
			}
			fmt.Fprintf(&b, "- lemi-%s %s：%s\n", bl.Kind, bl.ID, st)
		}
	}
	return &Result{Text: b.String(), Data: map[string]any{"report_id": rp.ID}}, nil
}
