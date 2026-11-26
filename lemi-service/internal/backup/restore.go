package backup

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 还原方式。
const (
	RestoreMySQLClient = "mysql"
	RestoreLemi        = "lemi"
	RestoreSQLiteFile  = "sqlite_file"
)

// RestoreRec 是一次还原。
type RestoreRec struct {
	ID             string `db:"id" json:"id"`
	WorkspaceID    string `db:"workspace_id" json:"workspace_id"`
	RunID          string `db:"run_id" json:"run_id"`
	TargetID       string `db:"target_id" json:"target_id"`
	ConnectionID   string `db:"connection_id" json:"connection_id"`
	ConnectionName string `db:"connection_name" json:"connection_name"`
	DatabaseMap    string `db:"database_map" json:"-"`
	Method         string `db:"method" json:"method"`
	Status         string `db:"status" json:"status"`
	Error          string `db:"error" json:"error"`
	Result         string `db:"result" json:"-"`
	CreatedBy      string `db:"created_by" json:"created_by"`
	StartedAt      int64  `db:"started_at" json:"started_at"`
	FinishedAt     int64  `db:"finished_at" json:"finished_at"`
}

// View 给前端的还原记录。
func (r *RestoreRec) View() map[string]any {
	var m map[string]string
	var res any
	_ = json.Unmarshal([]byte(r.DatabaseMap), &m)
	_ = json.Unmarshal([]byte(r.Result), &res)
	return map[string]any{"id": r.ID, "workspace_id": r.WorkspaceID, "run_id": r.RunID, "target_id": r.TargetID, "connection_id": r.ConnectionID,
		"connection_name": r.ConnectionName, "database_map": m, "method": r.Method, "status": r.Status, "error": r.Error, "result": res,
		"created_by": r.CreatedBy, "started_at": r.StartedAt, "finished_at": r.FinishedAt}
}

// GetRestore 取还原记录。
func (s *Service) GetRestore(ctx context.Context, id string) (*RestoreRec, error) {
	var r RestoreRec
	if err := s.Store.Get(ctx, &r, `SELECT * FROM backup_restores WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("还原记录")
		}
		return nil, err
	}
	return &r, nil
}

// ListRestores 空间的还原记录（倒序）。
func (s *Service) ListRestores(ctx context.Context, wsID, cursor string, limit int) ([]RestoreRec, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []RestoreRec{}
	q, args := `SELECT * FROM backup_restores WHERE workspace_id = ?`, []any{wsID}
	if cursor != "" {
		q, args = q+` AND id < ?`, append(args, cursor)
	}
	if err := s.Store.Select(ctx, &out, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// ---------------- 读取备份文件 ----------------

func (s *Service) runPassphrase(r *Run) (string, error) {
	if !r.Encrypted {
		return "", nil
	}
	p, err := s.Box.Open(r.Passphrase, runAAD(r.ID))
	if err != nil || p == "" {
		return "", errors.New("这份备份的密码无法解密（主密钥不匹配）")
	}
	return p, nil
}

// openArtifact 打开备份的原始文件（压缩、加密的原样），按目标顺序尝试。
func (s *Service) openArtifact(ctx context.Context, r *Run, preferTarget string) (io.ReadCloser, *FileRec, error) {
	files, err := s.okFiles(ctx, r, preferTarget)
	if err != nil {
		return nil, nil, err
	}
	var errs []string
	for i := range files {
		f := files[i]
		t, err := s.GetTarget(ctx, f.TargetID)
		if err != nil {
			errs = append(errs, f.TargetID+"：存储目标已删除")
			continue
		}
		tg, err := s.OpenTarget(ctx, t)
		if err != nil {
			errs = append(errs, t.Name+"："+err.Error())
			continue
		}
		rc, err := tg.Get(ctx, f.Path)
		if err != nil {
			tg.Close()
			errs = append(errs, t.Name+"："+targetErr(t.Name, err).Error())
			continue
		}
		return &closeBoth{rc, tg}, &f, nil
	}
	return nil, nil, apperr.Bad("读取备份文件失败：%s", strings.Join(errs, "；"))
}

type closeBoth struct {
	io.ReadCloser
	c io.Closer
}

func (c *closeBoth) Close() error {
	err := c.ReadCloser.Close()
	c.c.Close()
	return err
}

// openDecoded 打开解密、解压后的数据流（SQL 或 SQLite 文件）。count 非空时统计读到的原始字节。
func (s *Service) openDecoded(ctx context.Context, r *Run, preferTarget string, count func(n int64)) (io.ReadCloser, *FileRec, error) {
	pass, err := s.runPassphrase(r)
	if err != nil {
		return nil, nil, err
	}
	raw, f, err := s.openArtifact(ctx, r, preferTarget)
	if err != nil {
		return nil, nil, err
	}
	var src io.Reader = raw
	if count != nil {
		src = &countReader{raw, count}
	}
	dec, err := decodeStream(src, r.Compression, r.Encrypted, pass)
	if err != nil {
		raw.Close()
		return nil, nil, apperr.Wrap("decode_failed", err)
	}
	return &closeBoth{dec, raw}, f, nil
}

type countReader struct {
	r  io.Reader
	fn func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.fn(int64(n))
	}
	return n, err
}

// ---------------- 库名改写（mysqldump 产物用 mysql 客户端还原到别的库名时） ----------------

// dbRenamer 改写 mysqldump 输出里的 `CREATE DATABASE ... `库``、`USE `库`;` 两种行（只看行首，其余字节原样通过）。
// mysqldump 的值里不会出现裸换行（转义成 \n），所以行首判断是可靠的。
type dbRenamer struct {
	br        *bufio.Reader
	m         map[string]string
	pending   []byte
	lineStart bool
}

func newDBRenamer(r io.Reader, m map[string]string) io.Reader {
	return &dbRenamer{br: bufio.NewReaderSize(r, 1<<20), m: m, lineStart: true}
}

func quoteBacktick(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// renameFirstIdent 把 s 中 prefix 之后的第一个反引号标识符按映射替换。
func (d *dbRenamer) renameFirstIdent(line string) string {
	i := strings.IndexByte(line, '`')
	if i < 0 {
		return line
	}
	name, ok := backtickName(line[i+1:])
	if !ok {
		return line
	}
	to, ok := d.m[name]
	if !ok {
		return line
	}
	end := i + 1 + len(strings.ReplaceAll(name, "`", "``")) + 1
	return line[:i] + quoteBacktick(to) + line[end:]
}

func (d *dbRenamer) Read(p []byte) (int, error) {
	for len(d.pending) == 0 {
		if d.lineStart {
			peek, _ := d.br.Peek(32)
			if bytes.HasPrefix(peek, []byte("CREATE DATABASE ")) || bytes.HasPrefix(peek, []byte("USE `")) {
				line, err := d.br.ReadString('\n')
				if err != nil && err != io.EOF {
					return 0, err
				}
				if line == "" && err == io.EOF {
					return 0, io.EOF
				}
				d.pending = []byte(d.renameFirstIdent(line))
				d.lineStart = strings.HasSuffix(line, "\n")
				break
			}
		}
		chunk, err := d.br.ReadSlice('\n')
		if len(chunk) > 0 {
			d.pending = append(d.pending[:0], chunk...)
			d.lineStart = chunk[len(chunk)-1] == '\n'
			break
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(p, d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

// ---------------- 预检 ----------------

// RestoreInput 是还原请求。
type RestoreInput struct {
	ConnectionID string            `json:"connection_id"`
	DatabaseMap  map[string]string `json:"database_map"` // 备份里的库名 → 目标库名，没给的用原名
	Method       string            `json:"method"`       // mysql | lemi | sqlite_file，空 = 默认
	TargetID     string            `json:"target_id"`    // 从哪个存储目标读取，空 = 自动
	// Confirm 目标库非空时必须再输入一遍库名（SQLite 输入连接名）：目标库名 → 输入的文字
	Confirm map[string]string `json:"confirm"`
}

// PrecheckItem 是一个目标库的预检结果。
type PrecheckItem struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	Exists      bool   `json:"exists"`
	Tables      int    `json:"tables"`
	NeedConfirm bool   `json:"need_confirm"`
	ConfirmText string `json:"confirm_text,omitempty"` // 需要输入的文字
}

func validDBName(n string) bool {
	return n != "" && len(n) <= 64 && !strings.ContainsAny(n, "\x00/\\.") && strings.TrimSpace(n) == n
}

// mapping 规范化库名映射。
func restoreMapping(r *Run, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	if r.Driver != "mysql" {
		return out, nil
	}
	for _, db := range r.DatabaseList() {
		to := db
		if v, ok := in[db]; ok && strings.TrimSpace(v) != "" {
			to = strings.TrimSpace(v)
		}
		if !validDBName(to) {
			return nil, apperr.Bad("目标库名「%s」不正确", to)
		}
		out[db] = to
	}
	seen := map[string]bool{}
	for _, to := range out {
		if seen[strings.ToLower(to)] {
			return nil, apperr.Bad("两个库不能还原到同一个目标库「%s」", to)
		}
		seen[strings.ToLower(to)] = true
	}
	return out, nil
}

// Precheck 检查目标库是否存在、是否为空。
func (s *Service) Precheck(ctx context.Context, r *Run, c *conns.Connection, in map[string]string) ([]PrecheckItem, map[string]string, error) {
	if r.Status != StatusSuccess {
		return nil, nil, apperr.Bad("只能还原成功的备份")
	}
	if c.Driver != r.Driver {
		return nil, nil, apperr.Bad("目标连接的数据库类型与备份不一致")
	}
	if c.WorkspaceID != r.WorkspaceID {
		return nil, nil, apperr.Bad("只能还原到同一工作空间的连接")
	}
	m, err := restoreMapping(r, in)
	if err != nil {
		return nil, nil, err
	}
	sc, err := s.Conns.SQL(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	var items []PrecheckItem
	if c.Driver == "sqlite" {
		var n int
		_ = sc.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`).Scan(&n)
		items = append(items, PrecheckItem{Source: "main", Target: "main", Exists: true, Tables: n, NeedConfirm: n > 0, ConfirmText: c.Name})
		return items, m, nil
	}
	for _, db := range r.DatabaseList() {
		to := m[db]
		it := PrecheckItem{Source: db, Target: to}
		var cnt int
		if err := sc.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, to).Scan(&cnt); err != nil {
			return nil, nil, apperr.Wrap("precheck_failed", err)
		}
		it.Exists = cnt > 0
		if it.Exists {
			_ = sc.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?`, to).Scan(&it.Tables)
			var routines int
			_ = sc.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?`, to).Scan(&routines)
			it.NeedConfirm = it.Tables+routines > 0
			if it.NeedConfirm {
				it.ConfirmText = to
			}
		}
		items = append(items, it)
	}
	return items, m, nil
}

// defaultRestoreMethod 备份方式对应的默认还原方式。
func (s *Service) restoreMethod(r *Run, want string) (string, error) {
	switch r.Method {
	case MethodSQLiteFile:
		if want != "" && want != RestoreSQLiteFile {
			return "", apperr.Bad("SQLite 文件快照只能直接替换文件")
		}
		return RestoreSQLiteFile, nil
	case MethodLemi:
		if want != "" && want != RestoreLemi {
			return "", apperr.Bad("自研导出器的备份只能用自研导入器还原")
		}
		return RestoreLemi, nil
	case MethodMysqldump:
		switch want {
		case RestoreLemi:
			return RestoreLemi, nil
		case "", RestoreMySQLClient:
			if s.Tools != nil {
				if ok, _ := s.Tools.Available(); ok {
					return RestoreMySQLClient, nil
				}
			}
			if want == RestoreMySQLClient {
				return "", apperr.Bad("本平台没有可用的 mysql 客户端，请改用 Lemi 自研导入器")
			}
			return RestoreLemi, nil
		}
	}
	return "", apperr.Bad("不支持的还原方式")
}

// StartRestore 预检通过后开始还原（异步）。调用方已确认发起人对目标连接有写权限。
func (s *Service) StartRestore(ctx context.Context, r *Run, c *conns.Connection, in *RestoreInput, userID string) (*RestoreRec, error) {
	items, m, err := s.Precheck(ctx, r, c, in.DatabaseMap)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.NeedConfirm && strings.TrimSpace(in.Confirm[it.Target]) != it.ConfirmText {
			return nil, &apperr.Error{Status: 409, Code: "restore_confirm_required",
				Message: fmt.Sprintf("目标「%s」不是空的，还原会覆盖其中的同名对象。请勾选覆盖并输入「%s」确认", it.Target, it.ConfirmText), Extra: items}
		}
	}
	method, err := s.restoreMethod(r, in.Method)
	if err != nil {
		return nil, err
	}
	if in.TargetID != "" {
		if _, err := s.okFiles(ctx, r, in.TargetID); err != nil {
			return nil, err
		}
	}
	rec := &RestoreRec{ID: ids.New(), WorkspaceID: r.WorkspaceID, RunID: r.ID, TargetID: in.TargetID, ConnectionID: c.ID, ConnectionName: c.Name,
		DatabaseMap: toJSON(m), Method: method, Status: StatusRunning, Result: "{}", CreatedBy: userID, StartedAt: store.Now()}
	release, ok := s.acquire(c.ID, rec.ID)
	if !ok {
		return nil, apperr.Conflict("目标连接正在执行备份、还原或校验，请稍后再试")
	}
	if err := s.Store.Add(ctx, "backup_restores", rec); err != nil {
		release()
		return nil, err
	}
	rctx, cancel := context.WithTimeout(s.stopCtx, RunTimeout)
	s.setCancel(rec.ID, cancel)
	prog := &progress{Kind: "restore", ID: rec.ID, WorkspaceID: rec.WorkspaceID, ConnectionID: c.ID, PlanID: r.PlanID, phase: "queued"}
	prog.total.Store(r.Size)
	finish := s.track(prog)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		defer cancel()
		defer s.setCancel(rec.ID, nil)
		var res map[string]any
		var err error
		select {
		case s.sem <- struct{}{}:
			prog.set(func(p *progress) { p.phase = "running" })
			res, err = s.doRestore(rctx, r, c, rec, m, prog)
			<-s.sem
		case <-rctx.Done():
			err = rctx.Err()
		}
		s.finishRestore(r, rec, res, err, finish)
	}()
	return rec, nil
}

func (s *Service) doRestore(ctx context.Context, r *Run, c *conns.Connection, rec *RestoreRec, m map[string]string, prog *progress) (map[string]any, error) {
	src, f, err := s.openDecoded(ctx, r, rec.TargetID, func(n int64) { prog.out.Add(n) })
	if err != nil {
		return nil, err
	}
	defer src.Close()
	rec.TargetID = f.TargetID
	counted := &countReader{src, func(n int64) { prog.raw.Add(n) }}
	switch rec.Method {
	case RestoreSQLiteFile:
		bak, err := s.replaceSQLiteFile(ctx, c, counted)
		if err != nil {
			return nil, err
		}
		return map[string]any{"previous_file": bak}, nil
	case RestoreMySQLClient:
		sc, err := s.Conns.SQL(ctx, c)
		if err != nil {
			return nil, err
		}
		info, err := sc.Server(ctx)
		if err != nil {
			return nil, err
		}
		tool, warns, err := s.Tools.Resolve(ctx, info.Product, info.Version, true, nil)
		if err != nil {
			return nil, err
		}
		cc, closeFn, err := s.clientConn(ctx, c)
		if err != nil {
			return nil, err
		}
		defer closeFn()
		var in io.Reader = counted
		if renamed(m) {
			in = newDBRenamer(counted, m)
		}
		w2, err := tool.Restore(ctx, cc, "", s.tmpDir("client"), in)
		warns = append(warns, w2...)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tool": "mysql " + tool.Version, "warnings": warns}, nil
	default:
		return s.importLemi(ctx, c, counted, m, prog)
	}
}

func renamed(m map[string]string) bool {
	for k, v := range m {
		if k != v {
			return true
		}
	}
	return false
}

// importLemi 用自研导入器执行。
func (s *Service) importLemi(ctx context.Context, c *conns.Connection, src io.Reader, m map[string]string, prog *progress) (map[string]any, error) {
	sc, err := s.Conns.SQL(ctx, c)
	if err != nil {
		return nil, err
	}
	conn, err := sc.DB().Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return importInto(ctx, conn, sqldump.Dialect(sc.Dialect()), src, m, prog)
}

func importInto(ctx context.Context, conn *sql.Conn, d sqldump.Dialect, src io.Reader, m map[string]string, prog *progress) (map[string]any, error) {
	dm := map[string]string{}
	for k, v := range m {
		if k != v {
			dm[k] = v
		}
	}
	res, err := sqldump.Import(ctx, conn, src, sqldump.ImportOptions{Dialect: d, DatabaseMap: dm, Progress: func(p sqldump.ImportProgress) {
		if prog != nil {
			prog.set(func(x *progress) { x.statements, x.errors, x.table = p.Statements, p.Errors, p.Table })
		}
	}})
	out := map[string]any{"import": res}
	if err != nil {
		if errors.Is(err, sqldump.ErrIncomplete) {
			return out, fmt.Errorf("备份文件不完整（可能被截断）：%w", err)
		}
		return out, err
	}
	return out, nil
}

// replaceSQLiteFile 用快照替换 SQLite 文件：先写到同目录临时文件并做完整性检查，关闭连接池，
// 把原文件（连同 -wal/-shm）改名留作备份，再把临时文件改成正式名。返回原文件的备份路径。
func (s *Service) replaceSQLiteFile(ctx context.Context, c *conns.Connection, src io.Reader) (string, error) {
	sc, err := s.Conns.SQL(ctx, c)
	if err != nil {
		return "", err
	}
	pc, ok := sc.(interface{ Path() string })
	if !ok {
		return "", errors.New("不是 SQLite 文件连接")
	}
	path := pc.Path()
	tmp := path + ".lemi-restore-" + ids.New() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("无法在数据库所在目录创建临时文件：%w", err)
	}
	ok = false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, ctxReader{ctx, src}); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if res, err := integrityCheck(ctx, tmp); err != nil || res != "ok" {
		if err == nil {
			err = errors.New(res)
		}
		return "", fmt.Errorf("快照完整性检查失败：%w", err)
	}
	s.Conns.Invalidate(c.ID)
	bak := path + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(path, bak); err != nil {
		return "", fmt.Errorf("备份原文件失败：%w", err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + sfx); err == nil {
			_ = os.Rename(path+sfx, bak+sfx)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(bak, path) // 尽量放回原文件
		return "", fmt.Errorf("替换文件失败：%w", err)
	}
	ok = true
	return filepath.Base(bak), nil
}

// integrityCheck 对 SQLite 文件做 PRAGMA integrity_check。
func integrityCheck(ctx context.Context, path string) (string, error) {
	if err := checkSQLiteHeader(path); err != nil {
		return "", err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return "", err
		}
		msgs = append(msgs, m)
		if len(msgs) >= 20 {
			break
		}
	}
	return strings.Join(msgs, "；"), rows.Err()
}

func checkSQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 16)
	if _, err := io.ReadFull(f, buf); err != nil || string(buf) != "SQLite format 3\x00" {
		return errors.New("不是 SQLite 3 数据库文件")
	}
	return nil
}

func (s *Service) finishRestore(r *Run, rec *RestoreRec, res map[string]any, err error, finish func(map[string]any)) {
	ctx := context.Background()
	rec.FinishedAt = store.Now()
	switch {
	case err == nil:
		rec.Status = StatusSuccess
	case errors.Is(err, context.Canceled):
		rec.Status, rec.Error = StatusCanceled, "已取消"
	default:
		rec.Status, rec.Error = StatusFailed, err.Error()
	}
	if res != nil {
		rec.Result = toJSON(res)
	}
	_ = s.Store.Put(ctx, "backup_restores", rec)
	finish(map[string]any{"status": rec.Status, "error": rec.Error})
	s.Audit.Record(audit.Entry{UserID: rec.CreatedBy, WorkspaceID: rec.WorkspaceID, ConnectionID: rec.ConnectionID, Source: audit.SrcUI, Action: "backup.restore",
		Target: r.FileName, OK: rec.Status == StatusSuccess, Error: rec.Error, DurationMS: rec.FinishedAt - rec.StartedAt},
		map[string]any{"restore_id": rec.ID, "run_id": r.ID, "method": rec.Method, "database_map": json.RawMessage(rec.DatabaseMap), "source_connection": r.ConnectionID})
	var channels []string
	if p, err := s.GetPlan(ctx, r.PlanID); err == nil {
		if rec.Status == StatusSuccess {
			channels = jsonStrings(p.NotifySuccess)
		} else {
			channels = jsonStrings(p.NotifyFailure)
		}
	}
	link := "/w/" + rec.WorkspaceID + "/backups?restore=" + rec.ID
	switch rec.Status {
	case StatusSuccess:
		s.notifyWS(rec.WorkspaceID, channels, notify.Event{Kind: notify.EvRestoreDone, Level: "info", Title: "还原完成：" + rec.ConnectionName,
			Message: fmt.Sprintf("备份 %s 已还原到连接「%s」。", r.FileName, rec.ConnectionName), Link: link})
	case StatusFailed:
		s.notifyWS(rec.WorkspaceID, channels, notify.Event{Kind: notify.EvRestoreFailed, Level: "error", Title: "还原失败：" + rec.ConnectionName,
			Message: fmt.Sprintf("备份 %s 还原到连接「%s」失败：%s", r.FileName, rec.ConnectionName, rec.Error), Link: link})
	}
}

