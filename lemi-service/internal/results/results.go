// Package results 保存智能体与 MCP 产生的大结果：查询结果集（最多 10 万行）与被截断的长工具输出。
// 完整内容存 <数据目录>/results/<id>.json，元数据在 ai_results 表；7 天后清理。
// 模型只看到前若干行与结果 ID，图表、报告、read_result 按 ID 引用，数据不经过模型 token。
package results

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 上限。
const (
	MaxRows  = 100000
	MaxBytes = 64 << 20
	Retain   = 7 * 24 * time.Hour
)

// Meta 是结果的元数据。
type Meta struct {
	ID           string          `db:"id" json:"id"`
	WorkspaceID  string          `db:"workspace_id" json:"workspace_id"`
	UserID       string          `db:"user_id" json:"-"`
	Kind         string          `db:"kind" json:"kind"` // rows | text
	ConnectionID string          `db:"connection_id" json:"connection_id,omitempty"`
	Database     string          `db:"database" json:"db,omitempty"`
	SQL          string          `db:"sql" json:"sql,omitempty"`
	Title        string          `db:"title" json:"title,omitempty"`
	ColumnsJSON  string          `db:"columns" json:"-"`
	RowCount     int             `db:"row_count" json:"row_count"`
	Truncated    bool            `db:"truncated" json:"truncated"`
	Bytes        int64           `db:"bytes" json:"bytes"`
	Source       string          `db:"source" json:"source"`
	CreatedAt    int64           `db:"created_at" json:"created_at"`
	Columns      []driver.Column `db:"-" json:"columns,omitempty"`
}

// Data 是完整内容。
type Data struct {
	Columns []driver.Column `json:"columns,omitempty"`
	Rows    [][]any         `json:"rows,omitempty"`
	Text    string          `json:"text,omitempty"`
}

// Store 结果存储。
type Store struct {
	st  *store.Store
	dir string
}

// New 创建结果存储。
func New(st *store.Store, dir string) *Store {
	_ = os.MkdirAll(dir, 0o700)
	return &Store{st: st, dir: dir}
}

// ErrNotFound 结果不存在、已过期或不属于当前用户。
var ErrNotFound = errors.New("结果不存在或已过期（结果保留 7 天）")

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) save(ctx context.Context, m *Meta, d *Data) (*Meta, error) {
	m.ID = ids.New()
	m.CreatedAt = store.Now()
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	m.Bytes = int64(len(b))
	if err := os.WriteFile(s.path(m.ID), b, 0o600); err != nil {
		return nil, fmt.Errorf("保存结果失败：%w", err)
	}
	cb, _ := json.Marshal(m.Columns)
	m.ColumnsJSON = string(cb)
	if err := s.st.Add(ctx, "ai_results", m); err != nil {
		_ = os.Remove(s.path(m.ID))
		return nil, err
	}
	return m, nil
}

// SaveRows 保存查询结果集。
func (s *Store) SaveRows(ctx context.Context, m Meta, cols []driver.Column, rows [][]any, truncated bool) (*Meta, error) {
	m.Kind = "rows"
	m.Columns = cols
	m.RowCount = len(rows)
	m.Truncated = truncated
	if rows == nil {
		rows = [][]any{}
	}
	return s.save(ctx, &m, &Data{Columns: cols, Rows: rows})
}

// SaveText 保存长文本（被截断的工具输出）。
func (s *Store) SaveText(ctx context.Context, m Meta, text string) (*Meta, error) {
	m.Kind = "text"
	m.RowCount = strings.Count(text, "\n") + 1
	return s.save(ctx, &m, &Data{Text: text})
}

// Get 取元数据，并要求属于该用户与工作空间。
func (s *Store) Get(ctx context.Context, id, userID, wsID string) (*Meta, error) {
	if !ids.Valid(id) {
		return nil, ErrNotFound
	}
	var m Meta
	if err := s.st.GetW(ctx, &m, `SELECT * FROM ai_results WHERE id = ?`, id); err != nil {
		return nil, ErrNotFound
	}
	if m.UserID != userID || (wsID != "" && m.WorkspaceID != wsID) {
		return nil, ErrNotFound
	}
	_ = json.Unmarshal([]byte(m.ColumnsJSON), &m.Columns)
	return &m, nil
}

// Load 读完整内容。
func (s *Store) Load(m *Meta) (*Data, error) {
	b, err := os.ReadFile(s.path(m.ID))
	if err != nil {
		return nil, ErrNotFound
	}
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Cleanup 删除过期结果。
func (s *Store) Cleanup(ctx context.Context) {
	cut := time.Now().Add(-Retain).UnixMilli()
	var idsOld []string
	_ = s.st.Select(ctx, &idsOld, `SELECT id FROM ai_results WHERE created_at < ?`, cut)
	for _, id := range idsOld {
		_ = os.Remove(s.path(id))
	}
	_, _ = s.st.Exec(ctx, `DELETE FROM ai_results WHERE created_at < ?`, cut)
	// 没有元数据的孤儿文件（例如写元数据前进程退出）
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > Retain+time.Hour {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// ---------- 读取与简单加工 ----------

// Cell 把单元格（含从 JSON 读回来的 $bin / $trunc 对象）转成文本。
func Cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return x
	case driver.BinValue, driver.TruncValue:
		return driver.CellText(x)
	case map[string]any:
		if l, ok := x["len"].(float64); ok {
			if t, ok := x["$trunc"].(string); ok {
				return t + "…（共 " + strconv.Itoa(int(l)) + " 字节，已截断）"
			}
			if _, ok := x["$bin"]; ok {
				return "0x(" + strconv.Itoa(int(l)) + " 字节二进制)"
			}
		}
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Number 尝试把单元格当数字（图表用）。
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// Filter 是 read_result 的简单过滤条件。
type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"` // = != > >= < <= contains starts_with is_null not_null
	Value  any    `json:"value"`
}

// Order 是排序。
type Order struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// Query 是读取参数。
type Query struct {
	Offset  int
	Limit   int
	Columns []string
	Filters []Filter
	Order   []Order
}

// Apply 在内存里做过滤、排序、选列、分页；返回列、行、过滤后的总行数。
func Apply(d *Data, q Query) ([]driver.Column, [][]any, int, error) {
	idx := map[string]int{}
	for i, c := range d.Columns {
		idx[strings.ToLower(c.Name)] = i
	}
	col := func(name string) (int, error) {
		i, ok := idx[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			names := make([]string, len(d.Columns))
			for k, c := range d.Columns {
				names[k] = c.Name
			}
			return 0, fmt.Errorf("结果里没有列 %q（可用列：%s）", name, strings.Join(names, ", "))
		}
		return i, nil
	}
	type cond struct {
		i  int
		op string
		v  any
	}
	var conds []cond
	for _, f := range q.Filters {
		i, err := col(f.Column)
		if err != nil {
			return nil, nil, 0, err
		}
		op := strings.ToLower(strings.TrimSpace(f.Op))
		switch op {
		case "=", "==", "eq":
			op = "="
		case "!=", "<>", "ne":
			op = "!="
		case ">", ">=", "<", "<=", "contains", "starts_with", "is_null", "not_null":
		default:
			return nil, nil, 0, fmt.Errorf("不支持的过滤运算 %q（可用：= != > >= < <= contains starts_with is_null not_null）", f.Op)
		}
		conds = append(conds, cond{i, op, f.Value})
	}
	match := func(row []any) bool {
		for _, c := range conds {
			v := row[c.i]
			switch c.op {
			case "is_null":
				if v != nil {
					return false
				}
				continue
			case "not_null":
				if v == nil {
					return false
				}
				continue
			}
			if v == nil {
				return false
			}
			want := Cell(c.v)
			if c.v == nil {
				want = ""
			}
			got := Cell(v)
			switch c.op {
			case "contains":
				if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
					return false
				}
				continue
			case "starts_with":
				if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(want)) {
					return false
				}
				continue
			}
			cmp := compare(got, want)
			ok := false
			switch c.op {
			case "=":
				ok = cmp == 0
			case "!=":
				ok = cmp != 0
			case ">":
				ok = cmp > 0
			case ">=":
				ok = cmp >= 0
			case "<":
				ok = cmp < 0
			case "<=":
				ok = cmp <= 0
			}
			if !ok {
				return false
			}
		}
		return true
	}
	rows := d.Rows
	if len(conds) > 0 {
		var kept [][]any
		for _, r := range rows {
			if match(r) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if len(q.Order) > 0 {
		type ord struct {
			i    int
			desc bool
		}
		var os []ord
		for _, o := range q.Order {
			i, err := col(o.Column)
			if err != nil {
				return nil, nil, 0, err
			}
			os = append(os, ord{i, o.Desc})
		}
		rows = append([][]any(nil), rows...)
		sort.SliceStable(rows, func(a, b int) bool {
			for _, o := range os {
				va, vb := rows[a][o.i], rows[b][o.i]
				if va == nil || vb == nil {
					if va == nil && vb == nil {
						continue
					}
					// NULL 排在最前（升序）
					return (va == nil) != o.desc
				}
				c := compare(Cell(va), Cell(vb))
				if c == 0 {
					continue
				}
				if o.desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
	}
	total := len(rows)
	cols := d.Columns
	var pick []int
	if len(q.Columns) > 0 {
		cols = nil
		for _, name := range q.Columns {
			i, err := col(name)
			if err != nil {
				return nil, nil, 0, err
			}
			pick = append(pick, i)
			cols = append(cols, d.Columns[i])
		}
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > total {
		q.Offset = total
	}
	end := total
	if q.Limit > 0 && q.Offset+q.Limit < end {
		end = q.Offset + q.Limit
	}
	page := rows[q.Offset:end]
	if pick != nil {
		out := make([][]any, len(page))
		for k, r := range page {
			nr := make([]any, len(pick))
			for j, i := range pick {
				nr[j] = r[i]
			}
			out[k] = nr
		}
		page = out
	}
	return cols, page, total, nil
}

// compare 数字按数值比较，否则按字符串比较。
func compare(a, b string) int {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}

// TextTable 把行渲染成给模型看的紧凑文本表（制表符分隔，单元格换行转义，超长截断）。
func TextTable(cols []driver.Column, rows [][]any, cellMax int) string {
	if cellMax <= 0 {
		cellMax = 300
	}
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(c.Name)
	}
	b.WriteByte('\n')
	for _, r := range rows {
		for i, v := range r {
			if i > 0 {
				b.WriteByte('\t')
			}
			t := Cell(v)
			t = strings.NewReplacer("\t", "\\t", "\n", "\\n", "\r", "\\r").Replace(t)
			if rs := []rune(t); len(rs) > cellMax {
				t = string(rs[:cellMax]) + "…"
			}
			b.WriteString(t)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TextLines 按行分页读取文本结果。
func TextLines(text string, offset, limit int) (string, int) {
	lines := strings.Split(text, "\n")
	total := len(lines)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return strings.Join(lines[offset:end], "\n"), total
}
