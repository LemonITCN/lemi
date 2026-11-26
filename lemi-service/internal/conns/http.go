package conns

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载连接相关接口。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := srv.H
	r.Get("/drivers", h(s.hDrivers))
	r.Get("/workspaces/{ws}/connections", h(func(w http.ResponseWriter, r *http.Request) error { return s.hList(srv, w, r) }))
	r.Post("/workspaces/{ws}/connections", h(func(w http.ResponseWriter, r *http.Request) error { return s.hCreate(srv, w, r) }))
	r.Post("/workspaces/{ws}/connections/test", h(func(w http.ResponseWriter, r *http.Request) error { return s.hTestNew(srv, w, r) }))
	r.Get("/workspaces/{ws}/sqlite-files", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSQLiteList(srv, w, r) }))
	r.With(uploadBody(1<<30)).Post("/workspaces/{ws}/sqlite-files", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSQLiteUpload(srv, w, r) }))
	r.Delete("/workspaces/{ws}/sqlite-files/{id}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSQLiteDelete(srv, w, r) }))
	r.Get("/workspaces/{ws}/sqlite-dirs", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSQLiteDirs(srv, w, r) }))
	r.Get("/workspaces/{ws}/saved-queries", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSavedList(srv, w, r) }))
	r.Post("/workspaces/{ws}/saved-queries", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSavedSave(srv, w, r) }))
	r.Delete("/workspaces/{ws}/saved-queries/{id}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSavedDelete(srv, w, r) }))

	r.Route("/connections/{cid}", func(r chi.Router) {
		r.Get("/", h(func(w http.ResponseWriter, r *http.Request) error { return s.hGet(srv, w, r) }))
		r.Put("/", h(func(w http.ResponseWriter, r *http.Request) error { return s.hUpdate(srv, w, r) }))
		r.Delete("/", h(func(w http.ResponseWriter, r *http.Request) error { return s.hDelete(srv, w, r) }))
		r.Post("/test", h(func(w http.ResponseWriter, r *http.Request) error { return s.hTest(srv, w, r) }))
		r.Post("/host-key", h(func(w http.ResponseWriter, r *http.Request) error { return s.hHostKey(srv, w, r) }))
		r.Get("/limits", h(func(w http.ResponseWriter, r *http.Request) error { return s.hLimits(srv, w, r) }))
		r.Put("/limits/{user}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hLimitSet(srv, w, r) }))
		r.Get("/server", h(func(w http.ResponseWriter, r *http.Request) error { return s.hServer(srv, w, r) }))
		r.Get("/databases", h(func(w http.ResponseWriter, r *http.Request) error { return s.hDatabases(srv, w, r) }))
		r.Get("/databases/{db}/objects", h(func(w http.ResponseWriter, r *http.Request) error { return s.hObjects(srv, w, r) }))
		r.Get("/databases/{db}/tables/{table}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hTable(srv, w, r) }))
		r.Get("/databases/{db}/completion", h(func(w http.ResponseWriter, r *http.Request) error { return s.hCompletion(srv, w, r) }))
		r.Get("/ddl", h(func(w http.ResponseWriter, r *http.Request) error { return s.hDDL(srv, w, r) }))
		r.Post("/designer", h(func(w http.ResponseWriter, r *http.Request) error { return s.hDesigner(srv, w, r) }))
		r.Post("/data/query", h(func(w http.ResponseWriter, r *http.Request) error { return s.hData(srv, w, r) }))
		r.Post("/data/count", h(func(w http.ResponseWriter, r *http.Request) error { return s.hCount(srv, w, r) }))
		r.Post("/data/apply", h(func(w http.ResponseWriter, r *http.Request) error { return s.hApply(srv, w, r) }))
		r.Post("/data/cell", h(func(w http.ResponseWriter, r *http.Request) error { return s.hCell(srv, w, r) }))
		r.Post("/sessions", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSessionOpen(srv, w, r) }))
		r.Get("/history", h(func(w http.ResponseWriter, r *http.Request) error { return s.hHistory(srv, w, r) }))
		r.Get("/monitor/{what}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hMonitor(srv, w, r) }))
		r.Post("/monitor/kill", h(func(w http.ResponseWriter, r *http.Request) error { return s.hKill(srv, w, r) }))
		r.Get("/users", h(func(w http.ResponseWriter, r *http.Request) error { return s.hUsers(srv, w, r) }))
		r.Post("/users/sql", h(func(w http.ResponseWriter, r *http.Request) error { return s.hUserSQL(srv, w, r) }))
		r.Post("/export", h(func(w http.ResponseWriter, r *http.Request) error { return s.hExport(srv, w, r) }))
		r.With(uploadBody(2<<30)).Post("/import/preview", h(func(w http.ResponseWriter, r *http.Request) error { return s.hImportPreview(srv, w, r) }))
		r.Post("/import/run", h(func(w http.ResponseWriter, r *http.Request) error { return s.hImportRun(srv, w, r) }))
	})
	r.Route("/sessions/{sid}", func(r chi.Router) {
		r.Post("/execute", h(func(w http.ResponseWriter, r *http.Request) error { return s.hExecute(srv, w, r) }))
		r.Post("/fetch", h(func(w http.ResponseWriter, r *http.Request) error { return s.hFetch(srv, w, r) }))
		r.Post("/cancel", h(func(w http.ResponseWriter, r *http.Request) error { return s.hCancel(srv, w, r) }))
		r.Post("/use", h(func(w http.ResponseWriter, r *http.Request) error { return s.hUse(srv, w, r) }))
		r.Post("/explain", h(func(w http.ResponseWriter, r *http.Request) error { return s.hExplain(srv, w, r) }))
		r.Get("/state", h(func(w http.ResponseWriter, r *http.Request) error { return s.hState(srv, w, r) }))
		r.Delete("/", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSessionClose(srv, w, r) }))
	})
}

// uploadBody 放宽上传接口的请求体上限（主干默认 2 MB）。
func uploadBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Service) hDrivers(w http.ResponseWriter, r *http.Request) error {
	type dv struct {
		driver.Info
		Network bool `json:"network"`
	}
	list := []dv{}
	for _, d := range driver.All() {
		_, isNet := d.(driver.NetworkDriver)
		list = append(list, dv{Info: d.Info(), Network: isNet})
	}
	return web.OK(w, map[string]any{"drivers": list})
}

func (s *Service) conn(r *http.Request, min string) (*Connection, string, error) {
	return s.Require(r.Context(), web.UserOf(r), chi.URLParam(r, "cid"), min)
}

func (s *Service) hList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	list, err := s.List(r.Context(), ws)
	if err != nil {
		return err
	}
	u := web.UserOf(r)
	out := []map[string]any{}
	for i := range list {
		lvl := s.Access(r.Context(), u, &list[i])
		if lvl == LevelNone {
			continue
		}
		out = append(out, s.View(&list[i], lvl))
	}
	return web.OK(w, map[string]any{"connections": out, "folders": Folders(list)})
}

func (s *Service) hCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var in Input
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Create(r.Context(), ws, web.UserOf(r).ID, &in)
	if err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "connection.create", WorkspaceID: ws, ConnectionID: c.ID, Target: c.Name}, map[string]any{"driver": c.Driver, "ai_level": c.AILevel})
	return web.OK(w, s.View(c, LevelManage))
}

func (s *Service) hGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	return web.OK(w, s.View(c, lvl))
}

func (s *Service) hUpdate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	var in Input
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	before := map[string]any{"ai_level": c.AILevel, "read_only": c.ReadOnly, "env": c.Env}
	if err := s.Update(r.Context(), c, &in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "connection.update", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: c.Name}, map[string]any{"before": before, "ai_level": c.AILevel, "read_only": c.ReadOnly, "env": c.Env})
	return web.OK(w, s.View(c, LevelManage))
}

func (s *Service) hDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	if err := s.Delete(r.Context(), c); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "connection.delete", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: c.Name}, nil)
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) testConn(ctx context.Context, c *Connection) (map[string]any, error) {
	start := time.Now()
	conn, closeFn, err := s.OpenTemp(ctx, c)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	info, err := conn.Server(ctx)
	if err != nil {
		return nil, apperr.Wrap("connect_failed", err)
	}
	return map[string]any{"ok": true, "server": info, "latency_ms": time.Since(start).Milliseconds()}, nil
}

// hTestNew 测试还没保存的连接（编辑表单里的「测试连接」）：如果带了 id 就沿用该连接已保存的密钥。
func (s *Service) hTestNew(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var req struct {
		ID string `json:"id"`
		Input
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	var old *Connection
	id := "test-" + ids.New()
	if req.ID != "" {
		c, _, err := s.Require(r.Context(), web.UserOf(r), req.ID, LevelManage)
		if err != nil {
			return err
		}
		old, id = c, c.ID
	}
	params, network, err := s.prepare(r.Context(), id, &req.Input, old)
	if err != nil {
		return err
	}
	tmp := &Connection{ID: id, WorkspaceID: ws, Name: req.Name, Driver: req.Driver, Params: params, Network: network, ReadOnly: true}
	out, err := s.testConn(r.Context(), tmp)
	if err != nil {
		return err
	}
	return web.OK(w, out)
}

func (s *Service) hTest(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	out, err := s.testConn(r.Context(), c)
	if err != nil {
		return err
	}
	return web.OK(w, out)
}

func (s *Service) hHostKey(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	if !strings.HasPrefix(req.Fingerprint, "SHA256:") || len(req.Fingerprint) > 100 {
		return apperr.Bad("指纹格式不正确")
	}
	if err := s.SetHostKey(r.Context(), c, req.Fingerprint); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "connection.ssh_host_key", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: c.Name}, map[string]any{"fingerprint": req.Fingerprint})
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hLimits(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	m, err := s.Limits(r.Context(), c.ID)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"limits": m})
}

func (s *Service) hLimitSet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	var req struct {
		Level string `json:"level"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	uid := chi.URLParam(r, "user")
	if err := s.SetLimit(r.Context(), c.ID, uid, req.Level); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "connection.limit", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: uid}, map[string]any{"level": req.Level})
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hServer(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	conn, err := s.Open(r.Context(), c)
	if err != nil {
		return err
	}
	info, err := conn.Server(r.Context())
	if err != nil {
		return apperr.Wrap("server_failed", err)
	}
	return web.OK(w, info)
}

func (s *Service) hDatabases(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	list, err := sc.Databases(r.Context())
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	if list == nil {
		list = []driver.DatabaseInfo{}
	}
	return web.OK(w, map[string]any{"databases": list})
}

func (s *Service) hObjects(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	o, err := sc.Objects(r.Context(), chi.URLParam(r, "db"))
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	return web.OK(w, o)
}

func (s *Service) hTable(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	t, err := sc.Table(r.Context(), chi.URLParam(r, "db"), chi.URLParam(r, "table"))
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	return web.OK(w, t)
}

// Completion 结构快照：表 → 列（编辑器补全、智能体用）。
func (s *Service) Completion(ctx context.Context, sc driver.SQLConn, db string) (map[string][]string, error) {
	if sc.Dialect() == "mysql" {
		return mysql.Completion(ctx, sc.DB(), db)
	}
	objs, err := sc.Objects(ctx, db)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, t := range append(objs.Tables, objs.Views...) {
		rows, err := sc.ReadDB().QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", t.Name)
		if err != nil {
			continue
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out[t.Name] = append(out[t.Name], n)
			}
		}
		rows.Close()
	}
	return out, nil
}

func (s *Service) hCompletion(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	m, err := s.Completion(r.Context(), sc, chi.URLParam(r, "db"))
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	return web.OK(w, map[string]any{"tables": m})
}

func (s *Service) hDDL(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	ddl, err := sc.DDL(r.Context(), q.Get("db"), q.Get("kind"), q.Get("name"))
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	return web.OK(w, map[string]any{"ddl": ddl})
}

func (s *Service) hDesigner(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Database string             `json:"db"`
		Table    string             `json:"table"` // 空 = 新建
		Design   driver.TableDesign `json:"design"`
		Execute  bool               `json:"execute"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	min := LevelRead
	if req.Execute {
		min = LevelWrite
	}
	c, lvl, err := s.conn(r, min)
	if err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	var old *driver.TableInfo
	if req.Table != "" {
		if old, err = sc.Table(r.Context(), req.Database, req.Table); err != nil {
			return apperr.Wrap("meta_failed", err)
		}
	}
	stmts, err := sc.AlterSQL(r.Context(), req.Database, old, &req.Design)
	if err != nil {
		return apperr.Wrap("design_invalid", err)
	}
	if !req.Execute {
		return web.OK(w, map[string]any{"statements": stmts})
	}
	cs, err := s.OpenConsole(r.Context(), web.UserOf(r), c, lvl, req.Database, audit.SrcUI, false, 0)
	if err != nil {
		return err
	}
	defer s.CloseConsole(cs.ID)
	results := s.Exec(r.Context(), cs, strings.Join(stmts, ";\n")+";", ExecOptions{StopOnError: true, Source: audit.SrcUI, IP: web.ClientIP(r)})
	return web.OK(w, map[string]any{"statements": stmts, "results": results})
}

func (s *Service) hData(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var q DataQuery
	if err := web.ReadJSON(r, &q); err != nil {
		return err
	}
	res, t, err := s.TableData(r.Context(), web.UserOf(r), c, lvl, q)
	if err != nil {
		return err
	}
	if !CanWrite(c, lvl) && res.Editable != nil && res.Editable.OK {
		res.Editable = &driver.Editable{OK: false, Reason: "你对这个连接只有读取权限"}
	}
	return web.OK(w, map[string]any{"result": res, "table": t})
}

func (s *Service) hCount(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var q DataQuery
	if err := web.ReadJSON(r, &q); err != nil {
		return err
	}
	n, err := s.CountRows(r.Context(), web.UserOf(r), c, lvl, q)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"count": n})
}

func (s *Service) hApply(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var req ApplyRequest
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	res, err := s.Apply(r.Context(), web.UserOf(r), c, lvl, &req, web.ClientIP(r))
	if err != nil {
		return err
	}
	return web.OK(w, res)
}

// hCell 取某个单元格的完整值（被截断的长文本 / 二进制，上限 16 MB）。
func (s *Service) hCell(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var req struct {
		Database string         `json:"db"`
		Table    string         `json:"table"`
		Column   string         `json:"column"`
		Key      map[string]any `json:"key"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	t, err := sc.Table(r.Context(), req.Database, req.Table)
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	found := false
	for _, col := range t.Columns {
		if col.Name == req.Column {
			found = true
		}
	}
	if !found || len(t.Key) == 0 {
		return apperr.Bad("列不存在或表没有主键")
	}
	var conds []string
	var args []any
	for _, k := range t.Key {
		v, err := decodeValue(req.Key[k])
		if err != nil {
			return err
		}
		q := sc.QuoteIdent(k)
		if k == "rowid" && t.KeyKind == "rowid" {
			q = "rowid"
		}
		conds = append(conds, sc.Hooks().NullSafeEq(q))
		args = append(args, v)
	}
	stmt := "SELECT " + sc.QuoteIdent(req.Column) + " FROM " + qualified(sc, req.Database, req.Table) + " WHERE " + strings.Join(conds, " AND ")
	cs, err := s.OpenConsole(r.Context(), web.UserOf(r), c, lvl, "", audit.SrcGrid, true, 0)
	if err != nil {
		return err
	}
	defer s.CloseConsole(cs.ID)
	cs.S.CellLimit = 16 << 20
	res, err := cs.S.Exec(r.Context(), stmt, args, 1)
	if err != nil {
		return apperr.Wrap("query_failed", err)
	}
	if len(res.Rows) == 0 {
		return apperr.NotFound("行")
	}
	return web.OK(w, map[string]any{"value": res.Rows[0][0], "column": res.Columns[0]})
}

func (s *Service) hSessionOpen(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	var req struct {
		Database string `json:"db"`
		ReadOnly bool   `json:"read_only"`
	}
	_ = web.ReadJSON(r, &req)
	cs, err := s.OpenConsole(r.Context(), web.UserOf(r), c, lvl, req.Database, audit.SrcUI, req.ReadOnly, 0)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"id": cs.ID, "read_only": cs.ReadOnly, "dialect": cs.Dialect, "database": cs.S.Database(r.Context())})
}

func (s *Service) console(r *http.Request) (*Console, *Connection, error) {
	cs, err := s.Console(web.UserOf(r), chi.URLParam(r, "sid"))
	if err != nil {
		return nil, nil, err
	}
	// 每次都重新确认权限（权限可能已被收回）
	c, _, err := s.Require(r.Context(), web.UserOf(r), cs.ConnID, LevelRead)
	if err != nil {
		s.CloseConsole(cs.ID)
		return nil, nil, err
	}
	return cs, c, nil
}

func (s *Service) hExecute(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, c, err := s.console(r)
	if err != nil {
		return err
	}
	var req struct {
		SQL         string `json:"sql"`
		MaxRows     int    `json:"max_rows"`
		StopOnError *bool  `json:"stop_on_error"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.SQL) == "" {
		return apperr.Bad("没有要执行的语句")
	}
	stop := true
	if req.StopOnError != nil {
		stop = *req.StopOnError
	}
	results := s.Exec(r.Context(), cs, req.SQL, ExecOptions{MaxRows: req.MaxRows, StopOnError: stop, Source: audit.SrcUI, IP: web.ClientIP(r)})
	if len(results) == 1 && results[0].Kind == "rows" {
		s.EditableForQuery(r.Context(), c, cs, results[0])
		if results[0].Editable != nil && results[0].Editable.OK && cs.ReadOnly {
			results[0].Editable = &driver.Editable{OK: false, Reason: "当前是只读会话"}
		}
	}
	return web.OK(w, map[string]any{"results": results, "in_tx": cs.S.InTx(), "database": cs.S.Database(r.Context())})
}

func (s *Service) hFetch(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, _, err := s.console(r)
	if err != nil {
		return err
	}
	var req struct {
		N int `json:"n"`
	}
	_ = web.ReadJSON(r, &req)
	if req.N <= 0 || req.N > 10000 {
		req.N = 500
	}
	rows, more, err := cs.S.Fetch(r.Context(), req.N)
	if err != nil {
		return apperr.Wrap("fetch_failed", err)
	}
	if rows == nil {
		rows = [][]any{}
	}
	return web.OK(w, map[string]any{"rows": rows, "has_more": more})
}

func (s *Service) hCancel(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, err := s.Console(web.UserOf(r), chi.URLParam(r, "sid"))
	if err != nil {
		return err
	}
	if err := cs.S.Cancel(r.Context()); err != nil {
		return apperr.Wrap("cancel_failed", err)
	}
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hUse(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, _, err := s.console(r)
	if err != nil {
		return err
	}
	var req struct {
		Database string `json:"db"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	if err := cs.S.Use(r.Context(), req.Database); err != nil {
		return apperr.Wrap("use_failed", err)
	}
	return web.OK(w, map[string]any{"database": req.Database})
}

func (s *Service) hExplain(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, c, err := s.console(r)
	if err != nil {
		return err
	}
	var req struct {
		SQL string `json:"sql"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	stmt, format := sc.ExplainSQL(strings.TrimRight(strings.TrimSpace(req.SQL), ";"))
	res, err := cs.S.Exec(r.Context(), stmt, nil, 2000)
	if err != nil {
		return apperr.Wrap("explain_failed", err)
	}
	return web.OK(w, map[string]any{"format": format, "result": res})
}

func (s *Service) hState(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, _, err := s.console(r)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"in_tx": cs.S.InTx(), "running": cs.S.Running(), "read_only": cs.ReadOnly, "database": cs.S.Database(r.Context())})
}

func (s *Service) hSessionClose(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	cs, err := s.Console(web.UserOf(r), chi.URLParam(r, "sid"))
	if err == nil {
		if cs.S.InTx() {
			srv.AuditR(r, audit.Entry{Action: "sql.tx_rolled_back_close", WorkspaceID: cs.WorkspaceID, ConnectionID: cs.ConnID}, nil)
		}
		s.CloseConsole(cs.ID)
	}
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hHistory(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	list, err := s.HistoryList(r.Context(), web.UserOf(r).ID, c.ID, r.URL.Query().Get("q"), web.QueryInt64(r, "before"), web.QueryInt(r, "limit", 100))
	if err != nil {
		return err
	}
	if list == nil {
		list = []History{}
	}
	return web.OK(w, map[string]any{"history": list})
}

func (s *Service) hMonitor(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelRead)
	if err != nil {
		return err
	}
	conn, err := s.Open(r.Context(), c)
	if err != nil {
		return err
	}
	m, ok := conn.(driver.Monitor)
	if !ok {
		return apperr.Bad("该数据库类型不支持监控")
	}
	ctx := r.Context()
	var v any
	switch chi.URLParam(r, "what") {
	case "processes":
		v, err = m.Processes(ctx)
	case "status":
		v, err = m.Status(ctx)
	case "variables":
		v, err = m.Variables(ctx)
	case "tables":
		v, err = m.TableSizes(ctx, web.QueryInt(r, "limit", 50))
	case "slow":
		v, err = m.SlowQueries(ctx, web.QueryInt(r, "limit", 50))
	default:
		return apperr.NotFound("监控项")
	}
	if err != nil {
		if errors.Is(err, driver.ErrUnsupported) {
			return apperr.Bad("%s", err.Error())
		}
		return apperr.Wrap("monitor_failed", err)
	}
	return web.OK(w, map[string]any{"data": v, "at": time.Now().UnixMilli()})
}

func (s *Service) hKill(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	var req struct {
		ID        string `json:"id"`
		QueryOnly bool   `json:"query_only"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	conn, err := s.Open(r.Context(), c)
	if err != nil {
		return err
	}
	m, ok := conn.(driver.Monitor)
	if !ok {
		return apperr.Bad("该数据库类型不支持")
	}
	if err := m.KillProcess(r.Context(), req.ID, req.QueryOnly); err != nil {
		return apperr.Wrap("kill_failed", err)
	}
	srv.AuditR(r, audit.Entry{Action: "monitor.kill", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: req.ID}, map[string]any{"query_only": req.QueryOnly})
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hUsers(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	conn, err := s.Open(r.Context(), c)
	if err != nil {
		return err
	}
	ua, ok := conn.(driver.UserAdmin)
	if !ok {
		return apperr.Bad("该数据库类型不支持账号管理")
	}
	list, err := ua.Users(r.Context())
	if err != nil {
		return apperr.Wrap("users_failed", err)
	}
	if u, h := r.URL.Query().Get("user"), r.URL.Query().Get("host"); u != "" {
		g, err := ua.Grants(r.Context(), u, h)
		if err != nil {
			return apperr.Wrap("users_failed", err)
		}
		return web.OK(w, map[string]any{"grants": g})
	}
	if list == nil {
		list = []driver.DBUser{}
	}
	return web.OK(w, map[string]any{"users": list})
}

// hUserSQL 生成账号操作语句；execute=true 时直接执行（需要管理权限）。口令不写审计。
func (s *Service) hUserSQL(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelManage)
	if err != nil {
		return err
	}
	var req struct {
		Op      string         `json:"op"`
		Params  map[string]any `json:"params"`
		Execute bool           `json:"execute"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	conn, err := s.Open(r.Context(), c)
	if err != nil {
		return err
	}
	ua, ok := conn.(driver.UserAdmin)
	if !ok {
		return apperr.Bad("该数据库类型不支持账号管理")
	}
	stmts, err := ua.UserSQL(req.Op, req.Params)
	if err != nil {
		return apperr.Wrap("users_invalid", err)
	}
	masked := make([]string, len(stmts))
	for i, st := range stmts {
		masked[i] = maskPassword(st)
	}
	if !req.Execute {
		return web.OK(w, map[string]any{"statements": masked})
	}
	sc := conn.(driver.SQLConn)
	if c.ReadOnly {
		return apperr.Forbidden("该连接设为只读")
	}
	for i, st := range stmts {
		if _, err := sc.DB().ExecContext(r.Context(), st); err != nil {
			srv.AuditR(r, audit.Entry{Action: "dbuser." + req.Op, WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, OK: false, Error: err.Error()}, map[string]any{"sql": masked[i]})
			return apperr.Wrap("users_failed", err)
		}
		srv.AuditR(r, audit.Entry{Action: "dbuser." + req.Op, WorkspaceID: c.WorkspaceID, ConnectionID: c.ID}, map[string]any{"sql": masked[i]})
	}
	_ = lvl
	return web.OK(w, map[string]any{"statements": masked, "ok": true})
}

func maskPassword(st string) string {
	i := strings.Index(strings.ToUpper(st), "IDENTIFIED BY ")
	if i < 0 {
		return st
	}
	return st[:i+len("IDENTIFIED BY ")] + "'******'"
}

// ---------- 收藏的 SQL ----------

// SavedQuery 收藏的 SQL。
type SavedQuery struct {
	ID           string `db:"id" json:"id"`
	WorkspaceID  string `db:"workspace_id" json:"workspace_id"`
	ConnectionID string `db:"connection_id" json:"connection_id"`
	Folder       string `db:"folder" json:"folder"`
	Name         string `db:"name" json:"name"`
	SQL          string `db:"sql" json:"sql"`
	Shared       bool   `db:"shared" json:"shared"`
	CreatedBy    string `db:"created_by" json:"created_by"`
	CreatedAt    int64  `db:"created_at" json:"created_at"`
	UpdatedAt    int64  `db:"updated_at" json:"updated_at"`
}

func (s *Service) hSavedList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	var out []SavedQuery
	if err := s.st.Select(r.Context(), &out, `SELECT * FROM saved_queries WHERE workspace_id = ? AND (shared = 1 OR created_by = ?) ORDER BY folder, name`, ws, web.UserOf(r).ID); err != nil {
		return err
	}
	if out == nil {
		out = []SavedQuery{}
	}
	return web.OK(w, map[string]any{"queries": out})
}

func (s *Service) hSavedSave(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	var q SavedQuery
	if err := web.ReadJSON(r, &q); err != nil {
		return err
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.SQL) > 1<<20 {
		return apperr.Bad("请填写名称（SQL 不超过 1 MB）")
	}
	u := web.UserOf(r)
	now := store.Now()
	if q.ID != "" {
		var cur SavedQuery
		if err := s.st.Get(r.Context(), &cur, `SELECT * FROM saved_queries WHERE id = ? AND workspace_id = ?`, q.ID, ws); err != nil {
			return apperr.NotFound("收藏")
		}
		if cur.CreatedBy != u.ID {
			if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
				return apperr.Forbidden("只能修改自己收藏的 SQL")
			}
		}
		q.CreatedBy, q.CreatedAt = cur.CreatedBy, cur.CreatedAt
	} else {
		q.ID, q.CreatedBy, q.CreatedAt = ids.New(), u.ID, now
	}
	q.WorkspaceID, q.UpdatedAt = ws, now
	if err := s.st.Put(r.Context(), "saved_queries", &q); err != nil {
		return err
	}
	return web.OK(w, q)
}

func (s *Service) hSavedDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	var cur SavedQuery
	if err := s.st.Get(r.Context(), &cur, `SELECT * FROM saved_queries WHERE id = ? AND workspace_id = ?`, chi.URLParam(r, "id"), ws); err != nil {
		return apperr.NotFound("收藏")
	}
	if cur.CreatedBy != web.UserOf(r).ID {
		if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
			return apperr.Forbidden("只能删除自己收藏的 SQL")
		}
	}
	_, err := s.st.Exec(r.Context(), `DELETE FROM saved_queries WHERE id = ?`, cur.ID)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"ok": true})
}

