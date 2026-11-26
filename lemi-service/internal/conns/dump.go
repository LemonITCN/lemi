package conns

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// DumpRoutes 整库导出为 SQL 文件 / 运行 SQL 文件（自研导出导入器，docs/spec.md §10、§11）。
func (s *Service) DumpRoutes(srv *web.Server, r chi.Router) {
	r.Post("/connections/{cid}/dump/export", srv.H(func(w http.ResponseWriter, r *http.Request) error { return s.hDumpExport(srv, w, r) }))
	r.With(uploadBody(4<<30)).Post("/connections/{cid}/dump/import", srv.H(func(w http.ResponseWriter, r *http.Request) error { return s.hDumpImport(srv, w, r) }))
}

// Hub 实时推送（装配时注入，用于导入进度）。
var Hub *realtime.Hub

func (s *Service) hDumpExport(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var req struct {
		Databases      []string `json:"databases"`
		Tables         []string `json:"tables"`
		NoData         bool     `json:"no_data"`
		NoStructure    bool     `json:"no_structure"`
		NoDrop         bool     `json:"no_drop"`
		ExtendedInsert bool     `json:"extended_insert"`
		KeepDefiner    bool     `json:"keep_definer"`
		CreateDatabase bool     `json:"create_database"`
		Gzip           bool     `json:"gzip"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	d := sqldump.Dialect(sc.Dialect())
	if d == sqldump.MySQL && len(req.Databases) == 0 {
		return apperr.Bad("请选择要导出的库")
	}
	name := c.Name
	if len(req.Databases) == 1 {
		name = req.Databases[0]
	}
	if len(req.Tables) == 1 {
		name += "-" + req.Tables[0]
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>| `, r) {
			return '_'
		}
		return r
	}, name) + "-" + time.Now().Format("20060102-150405") + ".sql"
	ct := "application/sql; charset=utf-8"
	if req.Gzip {
		name += ".gz"
		ct = "application/gzip"
	}
	u := web.UserOf(r)
	ip := web.ClientIP(r)
	host := ""
	if p, err := s.DecryptParams(c); err == nil {
		if h, ok := p["host"].(string); ok {
			host = h
		}
	}
	tok := Downloads.Add(&downloads.Job{UserID: u.ID, Filename: name, ContentType: ct, Size: -1, Write: func(ctx context.Context, out io.Writer) error {
		var dst io.Writer = out
		var gz *gzip.Writer
		if req.Gzip {
			gz = gzip.NewWriter(out)
			dst = gz
		}
		bw := bufio.NewWriterSize(dst, 256<<10)
		db := sc.ReadDB()
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		start := time.Now()
		m, err := sqldump.Export(ctx, conn, bw, sqldump.ExportOptions{Dialect: d, Databases: req.Databases, Tables: req.Tables, NoData: req.NoData, NoStructure: req.NoStructure,
			NoDrop: req.NoDrop, ExtendedInsert: req.ExtendedInsert, KeepDefiner: req.KeepDefiner, CreateDatabase: req.CreateDatabase, ConnectionName: c.Name, HostLabel: host})
		if err == nil {
			err = bw.Flush()
		}
		if err == nil && gz != nil {
			err = gz.Close()
		}
		rows := int64(0)
		if m != nil {
			for _, dbm := range m.Databases {
				for _, t := range dbm.Tables {
					rows += t.Rows
				}
			}
		}
		srv.Audit.Record(audit.Entry{UserID: u.ID, Username: u.Username, WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Source: audit.SrcUI, Action: "data.dump_export",
			Target: strings.Join(req.Databases, ","), IP: ip, OK: err == nil, Error: errText(err), DurationMS: time.Since(start).Milliseconds()}, map[string]any{"tables": req.Tables, "rows": rows})
		return err
	}})
	return web.OK(w, map[string]any{"url": "/api/v1/downloads/" + tok, "filename": name})
}

func (s *Service) hDumpImport(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelWrite)
	if err != nil {
		return err
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return apperr.Bad("请上传 SQL 文件")
	}
	// 先读表单字段（必须放在文件前面），再流式读文件，不落盘
	opts := map[string]string{}
	var part io.Reader
	var fname string
	var total int64
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return apperr.Bad("上传失败：%v", err)
		}
		if p.FormName() == "file" {
			part, fname = p, p.FileName()
			break
		}
		b, _ := io.ReadAll(io.LimitReader(p, 4096))
		opts[p.FormName()] = string(b)
	}
	if part == nil {
		return apperr.Bad("请上传 SQL 文件")
	}
	if n, err := parseInt(opts["size"]); err == nil {
		total = n
	}
	src := part
	if strings.EqualFold(filepath.Ext(fname), ".gz") {
		gz, err := gzip.NewReader(bufio.NewReader(part))
		if err != nil {
			return apperr.Bad("gzip 文件无法解压：%v", err)
		}
		defer gz.Close()
		src = gz
		total = 0
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	d := sqldump.Dialect(sc.Dialect())
	conn, err := sc.DB().Conn(r.Context())
	if err != nil {
		return apperr.Wrap("connect_failed", err)
	}
	defer conn.Close()
	u := web.UserOf(r)
	ws := c.WorkspaceID
	progress := func(p sqldump.ImportProgress) {
		if Hub != nil {
			Hub.SendToUser(u.ID, "dump_import_progress", map[string]any{"connection_id": c.ID, "bytes": p.Bytes, "total": p.TotalBytes, "statements": p.Statements, "errors": p.Errors, "table": p.Table})
		}
	}
	start := time.Now()
	res, err := sqldump.Import(r.Context(), conn, src, sqldump.ImportOptions{Dialect: d, TargetDatabase: opts["db"], ContinueOnError: opts["continue_on_error"] == "true",
		TotalBytes: total, Progress: progress})
	srv.AuditR(r, audit.Entry{Action: "data.dump_import", WorkspaceID: ws, ConnectionID: c.ID, Target: fname, OK: err == nil, Error: errText(err), DurationMS: time.Since(start).Milliseconds()},
		map[string]any{"db": opts["db"], "result": res})
	if err != nil {
		msg := err.Error()
		if errors.Is(err, sqldump.ErrIncomplete) {
			msg = "文件不完整（可能被截断），已回滚未完成的部分：" + msg
		}
		return &apperr.Error{Status: 400, Code: "import_failed", Message: msg, Extra: res}
	}
	return web.OK(w, res)
}

func parseInt(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("bad")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}
