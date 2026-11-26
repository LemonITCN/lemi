// Package reports 是分析报告：Markdown 正文 + 引用块（```lemi-chart <id>```、```lemi-table <result_id>```、
// ```lemi-diagram <id>```）。保存时把引用的数据做快照，之后不依赖数据库；空间内共享；可生成公开链接
// （访问密码、有效期、可撤销，受系统设置 AllowPublicShare 控制）；可导出 Markdown（表格数据内嵌）。
package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 上限。
const (
	MaxBody       = 512 << 10
	MaxTableRows  = 1000
	MaxBlocks     = 100
	maxTitleRunes = 200
)

// Report 是报告。
type Report struct {
	ID             string `db:"id" json:"id"`
	WorkspaceID    string `db:"workspace_id" json:"workspace_id"`
	CreatedBy      string `db:"created_by" json:"created_by"`
	UpdatedBy      string `db:"updated_by" json:"updated_by"`
	Title          string `db:"title" json:"title"`
	Body           string `db:"body" json:"body"`
	SnapshotJSON   string `db:"snapshot" json:"-"`
	ConversationID string `db:"conversation_id" json:"conversation_id,omitempty"`
	TaskID         string `db:"task_id" json:"task_id,omitempty"`
	ShareToken     string `db:"share_token" json:"-"`
	SharePassword  string `db:"share_password" json:"-"`
	ShareExpiresAt int64  `db:"share_expires_at" json:"-"`
	ShareCreatedAt int64  `db:"share_created_at" json:"-"`
	CreatedAt      int64  `db:"created_at" json:"created_at"`
	UpdatedAt      int64  `db:"updated_at" json:"updated_at"`
}

// TableSnap 是表格引用的快照。
type TableSnap struct {
	Title     string          `json:"title,omitempty"`
	SQL       string          `json:"sql,omitempty"`
	Columns   []driver.Column `json:"columns"`
	Rows      [][]any         `json:"rows"`
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated,omitempty"`
}

// Snapshot 是引用块的数据快照。
type Snapshot struct {
	Charts   map[string]map[string]any `json:"charts"`
	Tables   map[string]*TableSnap     `json:"tables"`
	Diagrams map[string]map[string]any `json:"diagrams"`
	Missing  []string                  `json:"missing,omitempty"`
}

// Block 是正文里的一个引用块。
type Block struct {
	Kind    string // chart | table | diagram
	ID      string
	Caption string
	Start   int // 行号（含）
	End     int // 行号（含）
}

var blockRe = regexp.MustCompile("^```lemi-(chart|table|diagram)[ \\t]+([A-Za-z0-9]+)[ \\t]*(```)?[ \\t]*$")

// ParseBlocks 找出正文里的引用块。支持单行 ```lemi-chart <id>``` 与多行（下一行起到 ``` 为说明文字）。
func ParseBlocks(body string) []Block {
	lines := strings.Split(body, "\n")
	var out []Block
	for i := 0; i < len(lines); i++ {
		m := blockRe.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil {
			continue
		}
		b := Block{Kind: m[1], ID: strings.ToLower(m[2]), Start: i, End: i}
		if m[3] == "" {
			var cap []string
			j := i + 1
			for ; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "```" {
					break
				}
				cap = append(cap, lines[j])
			}
			b.End = min(j, len(lines)-1)
			b.Caption = strings.TrimSpace(strings.Join(cap, "\n"))
			i = j
		}
		out = append(out, b)
	}
	return out
}

// Service 报告服务。
type Service struct {
	st     *store.Store
	set    *settings.Service
	acc    *accounts.Service
	charts *charts.Service
	res    *results.Store
	// BaseURL 返回对外地址（公开链接用），可为空
	BaseURL func() string
}

// New 创建报告服务。
func New(st *store.Store, set *settings.Service, acc *accounts.Service, ch *charts.Service, res *results.Store) *Service {
	return &Service{st: st, set: set, acc: acc, charts: ch, res: res}
}

// ErrNotFound 报告不存在。
var ErrNotFound = errors.New("报告不存在")

// Get 取报告。
func (s *Service) Get(ctx context.Context, id string) (*Report, error) {
	if !ids.Valid(id) {
		return nil, ErrNotFound
	}
	var r Report
	if err := s.st.GetW(ctx, &r, `SELECT * FROM reports WHERE id = ?`, id); err != nil {
		return nil, ErrNotFound
	}
	return &r, nil
}

// List 空间里的报告（不含正文）。
func (s *Service) List(ctx context.Context, wsID string, limit int) ([]Report, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []Report
	err := s.st.Select(ctx, &out, `SELECT id, workspace_id, created_by, updated_by, title, '' AS body, '{}' AS snapshot, conversation_id, task_id,
		share_token, '' AS share_password, share_expires_at, share_created_at, created_at, updated_at
		FROM reports WHERE workspace_id = ? ORDER BY updated_at DESC LIMIT ?`, wsID, limit)
	return out, err
}

// Decode 解析快照。
func (r *Report) Decode() *Snapshot {
	var sn Snapshot
	_ = json.Unmarshal([]byte(r.SnapshotJSON), &sn)
	if sn.Charts == nil {
		sn.Charts = map[string]map[string]any{}
	}
	if sn.Tables == nil {
		sn.Tables = map[string]*TableSnap{}
	}
	if sn.Diagrams == nil {
		sn.Diagrams = map[string]map[string]any{}
	}
	return &sn
}

// buildSnapshot 按正文的引用块做数据快照。引用不到的对象（不存在、不属于本空间、结果已过期或不是自己的结果）
// 沿用旧快照；旧快照里也没有的记为缺失。
func (s *Service) buildSnapshot(ctx context.Context, u *accounts.User, wsID, body string, old *Snapshot) (*Snapshot, []string) {
	sn := &Snapshot{Charts: map[string]map[string]any{}, Tables: map[string]*TableSnap{}, Diagrams: map[string]map[string]any{}}
	var warns []string
	blocks := ParseBlocks(body)
	if len(blocks) > MaxBlocks {
		warns = append(warns, fmt.Sprintf("引用块超过 %d 个，后面的没有做快照", MaxBlocks))
		blocks = blocks[:MaxBlocks]
	}
	for _, b := range blocks {
		key := b.Kind + ":" + b.ID
		switch b.Kind {
		case "chart", "diagram":
			c, err := s.charts.Get(ctx, b.ID)
			wantKind := charts.KindECharts
			if b.Kind == "diagram" {
				wantKind = charts.KindMermaid
			}
			if err == nil && c.WorkspaceID == wsID && c.Kind == wantKind {
				v := s.charts.View(c)
				if b.Kind == "chart" {
					sn.Charts[b.ID] = v
				} else {
					sn.Diagrams[b.ID] = v
				}
				continue
			}
			if old != nil {
				if v, ok := old.Charts[b.ID]; ok && b.Kind == "chart" {
					sn.Charts[b.ID] = v
					continue
				}
				if v, ok := old.Diagrams[b.ID]; ok && b.Kind == "diagram" {
					sn.Diagrams[b.ID] = v
					continue
				}
			}
			if err == nil && c.Kind != wantKind {
				warns = append(warns, fmt.Sprintf("%s 是%s，请用 lemi-%s 引用", b.ID, map[string]string{charts.KindECharts: "图表", charts.KindMermaid: "图"}[c.Kind], map[string]string{charts.KindECharts: "chart", charts.KindMermaid: "diagram"}[c.Kind]))
			} else {
				warns = append(warns, fmt.Sprintf("找不到%s %s", map[string]string{"chart": "图表", "diagram": "图"}[b.Kind], b.ID))
			}
			sn.Missing = append(sn.Missing, key)
		case "table":
			if m, err := s.res.Get(ctx, b.ID, u.ID, wsID); err == nil && m.Kind == "rows" {
				if d, err := s.res.Load(m); err == nil {
					ts := &TableSnap{Title: m.Title, SQL: m.SQL, Columns: d.Columns, Rows: d.Rows, Total: len(d.Rows), Truncated: m.Truncated}
					if len(ts.Rows) > MaxTableRows {
						ts.Rows = ts.Rows[:MaxTableRows]
						ts.Truncated = true
					}
					if ts.Rows == nil {
						ts.Rows = [][]any{}
					}
					sn.Tables[b.ID] = ts
					continue
				}
			}
			if old != nil {
				if v, ok := old.Tables[b.ID]; ok {
					sn.Tables[b.ID] = v
					continue
				}
			}
			warns = append(warns, fmt.Sprintf("找不到结果 %s（结果保留 7 天，只能引用自己的查询结果）", b.ID))
			sn.Missing = append(sn.Missing, key)
		}
	}
	return sn, warns
}

func cleanTitle(t string) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "\n", " "))
	if r := []rune(t); len(r) > maxTitleRunes {
		t = string(r[:maxTitleRunes])
	}
	return t
}

// Create 新建报告。
func (s *Service) Create(ctx context.Context, wsID string, u *accounts.User, title, body, convID, taskID string) (*Report, []string, error) {
	title = cleanTitle(title)
	if title == "" {
		return nil, nil, errors.New("报告标题不能为空")
	}
	if len(body) > MaxBody {
		return nil, nil, fmt.Errorf("报告正文超过 %d KB", MaxBody>>10)
	}
	sn, warns := s.buildSnapshot(ctx, u, wsID, body, nil)
	b, _ := json.Marshal(sn)
	now := store.Now()
	r := &Report{ID: ids.New(), WorkspaceID: wsID, CreatedBy: u.ID, UpdatedBy: u.ID, Title: title, Body: body, SnapshotJSON: string(b),
		ConversationID: convID, TaskID: taskID, CreatedAt: now, UpdatedAt: now}
	if err := s.st.Add(ctx, "reports", r); err != nil {
		return nil, nil, err
	}
	return r, warns, nil
}

// Update 修改报告（title 为空表示不改标题）。
func (s *Service) Update(ctx context.Context, r *Report, u *accounts.User, title, body string) ([]string, error) {
	if t := cleanTitle(title); t != "" {
		r.Title = t
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("报告正文超过 %d KB", MaxBody>>10)
	}
	sn, warns := s.buildSnapshot(ctx, u, r.WorkspaceID, body, r.Decode())
	b, _ := json.Marshal(sn)
	r.Body, r.SnapshotJSON, r.UpdatedBy, r.UpdatedAt = body, string(b), u.ID, store.Now()
	_, err := s.st.Exec(ctx, `UPDATE reports SET title = ?, body = ?, snapshot = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
		r.Title, r.Body, r.SnapshotJSON, r.UpdatedBy, r.UpdatedAt, r.ID)
	return warns, err
}

// Delete 删除报告。
func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.st.Exec(ctx, `DELETE FROM reports WHERE id = ?`, id)
	return err
}

// View 给前端的报告（含快照）。
func (s *Service) View(r *Report) map[string]any {
	out := map[string]any{"id": r.ID, "workspace_id": r.WorkspaceID, "title": r.Title, "body": r.Body, "snapshot": r.Decode(),
		"created_by": r.CreatedBy, "updated_by": r.UpdatedBy, "conversation_id": r.ConversationID, "task_id": r.TaskID,
		"created_at": r.CreatedAt, "updated_at": r.UpdatedAt}
	out["share"] = s.shareView(r)
	return out
}

func (s *Service) shareView(r *Report) map[string]any {
	if r.ShareToken == "" {
		return nil
	}
	return map[string]any{"enabled": true, "has_password": r.SharePassword != "", "expires_at": r.ShareExpiresAt, "created_at": r.ShareCreatedAt}
}

// ---------- Markdown 导出 ----------

// Markdown 导出为纯 Markdown：表格数据内嵌为 Markdown 表格，图表附数据表，图为 mermaid 代码块。
func (s *Service) Markdown(r *Report) string {
	sn := r.Decode()
	lines := strings.Split(r.Body, "\n")
	blocks := ParseBlocks(r.Body)
	var b strings.Builder
	b.WriteString("# " + r.Title + "\n\n")
	next := 0
	for _, bl := range blocks {
		for ; next < bl.Start; next++ {
			b.WriteString(lines[next] + "\n")
		}
		next = bl.End + 1
		switch bl.Kind {
		case "table":
			t := sn.Tables[bl.ID]
			if t == nil {
				b.WriteString("> （表格数据缺失）\n")
				continue
			}
			if bl.Caption != "" {
				b.WriteString("**" + mdEscape(bl.Caption) + "**\n\n")
			}
			writeTable(&b, t.Columns, t.Rows)
			if t.Truncated {
				fmt.Fprintf(&b, "\n> 只列出前 %d 行\n", len(t.Rows))
			}
		case "chart":
			c := sn.Charts[bl.ID]
			if c == nil {
				b.WriteString("> （图表缺失）\n")
				continue
			}
			title, _ := c["title"].(string)
			fmt.Fprintf(&b, "**图表：%s**\n\n", mdEscape(title))
			if opt, ok := c["option"].(map[string]any); ok {
				writeDataset(&b, opt)
			}
		case "diagram":
			d := sn.Diagrams[bl.ID]
			if d == nil {
				b.WriteString("> （图缺失）\n")
				continue
			}
			code, _ := d["code"].(string)
			b.WriteString("```mermaid\n" + code + "\n```\n")
		}
	}
	for ; next < len(lines); next++ {
		b.WriteString(lines[next])
		if next < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", "").Replace(s)
}

func writeTable(b *strings.Builder, cols []driver.Column, rows [][]any) {
	if len(cols) == 0 {
		return
	}
	b.WriteString("|")
	for _, c := range cols {
		b.WriteString(" " + mdEscape(c.Name) + " |")
	}
	b.WriteString("\n|")
	for range cols {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("|")
		for i := range cols {
			v := ""
			if i < len(r) {
				v = results.Cell(r[i])
			}
			b.WriteString(" " + mdEscape(v) + " |")
		}
		b.WriteString("\n")
	}
}

func writeDataset(b *strings.Builder, opt map[string]any) {
	ds, _ := opt["dataset"].(map[string]any)
	src, _ := ds["source"].([]any)
	if len(src) == 0 {
		return
	}
	hdr, _ := src[0].([]any)
	cols := make([]driver.Column, len(hdr))
	for i, h := range hdr {
		cols[i] = driver.Column{Name: fmt.Sprint(h)}
	}
	var rows [][]any
	for _, r := range src[1:] {
		if rr, ok := r.([]any); ok {
			rows = append(rows, rr)
		}
	}
	writeTable(b, cols, rows)
}
