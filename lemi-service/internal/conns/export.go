package conns

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/xuri/excelize/v2"
)

// Downloads 一次性下载（由装配时注入）。
var Downloads *downloads.Store

// ExportRequest 导出请求：sql 或 表 + 条件 二选一。
type ExportRequest struct {
	SQL      string `json:"sql"`
	Database string `json:"db"`
	Table    string `json:"table"`
	Where    string `json:"where"`
	OrderBy  string `json:"order_by"`
	Format   string `json:"format"` // csv | tsv | json | sql | markdown | xlsx
	Header   *bool  `json:"header"`
	NullText string `json:"null_text"`
	SQLTable string `json:"sql_table"` // SQL INSERT 用的表名
	MaxRows  int    `json:"max_rows"`
	Filename string `json:"filename"`
}

var exportTypes = map[string][2]string{
	"csv":      {"text/csv; charset=utf-8", ".csv"},
	"tsv":      {"text/tab-separated-values; charset=utf-8", ".tsv"},
	"json":     {"application/json; charset=utf-8", ".json"},
	"sql":      {"application/sql; charset=utf-8", ".sql"},
	"markdown": {"text/markdown; charset=utf-8", ".md"},
	"xlsx":     {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
}

func (s *Service) hExport(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var req ExportRequest
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	ft, ok := exportTypes[req.Format]
	if !ok {
		return apperr.Bad("不支持的导出格式")
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	stmt := strings.TrimSpace(req.SQL)
	name := req.Filename
	if stmt == "" {
		if req.Table == "" {
			return apperr.Bad("请指定 SQL 或表")
		}
		if err := fragmentOK(req.Where, sc.Dialect()); err != nil {
			return err
		}
		if err := fragmentOK(req.OrderBy, sc.Dialect()); err != nil {
			return err
		}
		stmt = "SELECT * FROM " + qualified(sc, req.Database, req.Table)
		if req.Where != "" {
			stmt += " WHERE " + req.Where
		}
		if req.OrderBy != "" {
			stmt += " ORDER BY " + req.OrderBy
		}
		if name == "" {
			name = req.Table
		}
		if req.SQLTable == "" {
			req.SQLTable = req.Table
		}
	} else {
		info := sqlclass.Classify(stmt, sc.Dialect())
		if !info.ReturnsRows || info.Kind != sqlclass.Read {
			return apperr.Bad("只能导出查询语句的结果")
		}
	}
	if name == "" {
		name = "result"
	}
	name += "-" + time.Now().Format("20060102-150405") + ft[1]
	u := web.UserOf(r)
	ip := web.ClientIP(r)
	tok := Downloads.Add(&downloads.Job{UserID: u.ID, Filename: name, ContentType: ft[0], Size: -1, Write: func(ctx context.Context, out io.Writer) error {
		n, err := s.export(ctx, u, c, lvl, req.Database, stmt, &req, out)
		srv.Audit.Record(audit.Entry{UserID: u.ID, Username: u.Username, WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Source: audit.SrcUI, Action: "data.export",
			Target: req.Format, IP: ip, OK: err == nil, Error: errText(err)}, map[string]any{"sql": stmt, "rows": n})
		return err
	}})
	return web.OK(w, map[string]any{"url": "/api/v1/downloads/" + tok, "filename": name})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// cellText 导出用的文本（二进制写成 0x 十六进制）。
func cellText(v any, null string) (string, bool) {
	switch x := v.(type) {
	case nil:
		return null, true
	case string:
		return x, false
	case driver.BinValue:
		b, _ := base64.StdEncoding.DecodeString(x.B64)
		return "0x" + hex.EncodeToString(b), false
	case driver.TruncValue:
		return x.Text, false
	}
	return fmt.Sprint(v), false
}

// export 流式导出，返回行数。
func (s *Service) export(ctx context.Context, u *accounts.User, c *Connection, lvl, db, stmt string, req *ExportRequest, out io.Writer) (int64, error) {
	cs, err := s.OpenConsole(ctx, u, c, lvl, db, audit.SrcUI, true, 0)
	if err != nil {
		return 0, err
	}
	defer s.CloseConsole(cs.ID)
	cs.S.CellLimit = 64 << 20
	res, err := cs.S.Exec(ctx, stmt, nil, 2000)
	if err != nil {
		return 0, err
	}
	cols := res.Columns
	header := req.Header == nil || *req.Header
	bw := bufio.NewWriterSize(out, 256<<10)
	defer bw.Flush()
	var n int64
	limit := int64(req.MaxRows)
	next := func() ([][]any, bool, error) {
		if res != nil {
			rows, more := res.Rows, res.HasMore
			res = nil
			return rows, more, nil
		}
		return cs.S.Fetch(ctx, 5000)
	}
	names := make([]string, len(cols))
	for i, col := range cols {
		names[i] = col.Name
	}
	var xl *excelize.File
	var sw *excelize.StreamWriter
	var cw *csv.Writer
	switch req.Format {
	case "csv", "tsv":
		if req.Format == "csv" {
			_, _ = bw.WriteString("\xEF\xBB\xBF") // Excel 打开 UTF-8 CSV 需要 BOM
		}
		cw = csv.NewWriter(bw)
		if req.Format == "tsv" {
			cw.Comma = '\t'
		}
		if header {
			_ = cw.Write(names)
		}
	case "json":
		_, _ = bw.WriteString("[\n")
	case "markdown":
		_, _ = bw.WriteString("| " + strings.Join(mdEscapeAll(names), " | ") + " |\n|" + strings.Repeat(" --- |", len(names)) + "\n")
	case "xlsx":
		xl = excelize.NewFile()
		sw, err = xl.NewStreamWriter("Sheet1")
		if err != nil {
			return 0, err
		}
		if header {
			row := make([]any, len(names))
			for i, nm := range names {
				row[i] = nm
			}
			_ = sw.SetRow("A1", row)
		}
	}
	sc, _ := s.SQL(ctx, c)
	for {
		rows, more, err := next()
		if err != nil {
			return n, err
		}
		for _, row := range rows {
			if limit > 0 && n >= limit {
				more = false
				break
			}
			switch req.Format {
			case "csv", "tsv":
				rec := make([]string, len(row))
				for i, v := range row {
					rec[i], _ = cellText(v, req.NullText)
				}
				if err := cw.Write(rec); err != nil {
					return n, err
				}
			case "json":
				obj := make(map[string]any, len(row))
				for i, v := range row {
					if v == nil {
						obj[names[i]] = nil
						continue
					}
					t, _ := cellText(v, "")
					if cols[i].Kind == "json" {
						var j any
						if json.Unmarshal([]byte(t), &j) == nil {
							obj[names[i]] = j
							continue
						}
					}
					obj[names[i]] = t
				}
				b, _ := json.Marshal(obj)
				if n > 0 {
					_, _ = bw.WriteString(",\n")
				}
				_, _ = bw.Write(b)
			case "sql":
				vals := make([]string, len(row))
				for i, v := range row {
					vals[i] = sqlLiteral(sc, v, cols[i].Kind)
				}
				tbl := req.SQLTable
				if tbl == "" {
					tbl = "result"
				}
				qn := make([]string, len(names))
				for i, nm := range names {
					qn[i] = sc.QuoteIdent(nm)
				}
				_, _ = bw.WriteString("INSERT INTO " + sc.QuoteIdent(tbl) + " (" + strings.Join(qn, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ");\n")
			case "markdown":
				rec := make([]string, len(row))
				for i, v := range row {
					t, _ := cellText(v, req.NullText)
					rec[i] = mdEscape(t)
				}
				_, _ = bw.WriteString("| " + strings.Join(rec, " | ") + " |\n")
			case "xlsx":
				cells := make([]any, len(row))
				for i, v := range row {
					t, isNull := cellText(v, req.NullText)
					switch {
					case isNull && req.NullText == "":
						cells[i] = nil
					case cols[i].Kind == "int" || cols[i].Kind == "float" || cols[i].Kind == "decimal":
						if f, err := strconv.ParseFloat(t, 64); err == nil && len(t) < 16 {
							cells[i] = f
						} else {
							cells[i] = t
						}
					default:
						cells[i] = t
					}
				}
				off := int64(1)
				if !header {
					off = 0
				}
				cell, _ := excelize.CoordinatesToCellName(1, int(n+off+1))
				if err := sw.SetRow(cell, cells); err != nil {
					return n, err
				}
			}
			n++
		}
		if !more || ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	switch req.Format {
	case "csv", "tsv":
		cw.Flush()
		if err := cw.Error(); err != nil {
			return n, err
		}
	case "json":
		_, _ = bw.WriteString("\n]\n")
	case "xlsx":
		if err := sw.Flush(); err != nil {
			return n, err
		}
		if err := xl.Write(bw); err != nil {
			return n, err
		}
		_ = xl.Close()
	}
	return n, bw.Flush()
}

func sqlLiteral(sc driver.SQLConn, v any, kind string) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case driver.BinValue:
		b, _ := base64.StdEncoding.DecodeString(x.B64)
		if sc.Dialect() == "sqlite" {
			return "X'" + hex.EncodeToString(b) + "'"
		}
		if len(b) == 0 {
			return "''"
		}
		return "0x" + hex.EncodeToString(b)
	case string:
		switch kind {
		case "int", "float", "decimal":
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return x
			}
		}
		return sc.QuoteString(x)
	}
	t, _ := cellText(v, "")
	return sc.QuoteString(t)
}

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r\n", "<br>")
	return strings.ReplaceAll(s, "\n", "<br>")
}

func mdEscapeAll(a []string) []string {
	out := make([]string, len(a))
	for i, s := range a {
		out[i] = mdEscape(s)
	}
	return out
}
