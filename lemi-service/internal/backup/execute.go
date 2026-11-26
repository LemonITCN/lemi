package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/target"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netdial"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// BackupManifest 是写在备份文件旁边的 .manifest.json（不加密）。
type BackupManifest struct {
	Format           string                `json:"format"` // lemi-backup-manifest/1
	BackupID         string                `json:"backup_id"`
	PlanID           string                `json:"plan_id,omitempty"`
	WorkspaceID      string                `json:"workspace_id"`
	ConnectionID     string                `json:"connection_id"`
	ConnectionName   string                `json:"connection_name"`
	Driver           string                `json:"driver"`
	Method           string                `json:"method"`
	Tool             string                `json:"tool"`
	ServerProduct    string                `json:"server_product"`
	ServerVersion    string                `json:"server_version"`
	Databases        []sqldump.DBManifest  `json:"databases"`
	File             string                `json:"file"`
	Compression      string                `json:"compression"`
	CompressionLevel int                   `json:"compression_level,omitempty"`
	Encrypted        bool                  `json:"encrypted"`
	Encryption       string                `json:"encryption,omitempty"`
	Size             int64                 `json:"size"`
	RawSize          int64                 `json:"raw_size"`
	SHA256           string                `json:"sha256"`
	StartedAt        int64                 `json:"started_at"`
	FinishedAt       int64                 `json:"finished_at"`
	Warnings         []string              `json:"warnings,omitempty"`
	HashAlgorithm    string                `json:"hash_algorithm,omitempty"` // 表内容哈希的算法（自研导出器）
}

// compressExt 压缩扩展名。
func compressExt(c string) string {
	switch c {
	case "zstd":
		return ".zst"
	case "gzip":
		return ".gz"
	}
	return ""
}

var nameReplacer = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_", "~", "_")

// safeName 把连接名、库名变成文件名片段（保留中文，替换路径与特殊字符，限长）。
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, nameReplacer.Replace(strings.TrimSpace(s)))
	s = strings.Trim(s, ".")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		s = "db"
	}
	return s
}

// backupFileName 按 docs/spec.md §12.3：<连接名>-<库名|all>-<UTC yyyyMMdd-HHmmss>.<sql|db>[.zst|.gz][.lemienc]。
// driver 为 sqlite 时库名部分是 main；MySQL 选了一个库用库名，选了多个用 multi，没选（全部）用 all。
func backupFileName(connName, driverID string, selected []string, method, compression string, encrypted bool, at time.Time) string {
	part := "all"
	switch {
	case driverID == "sqlite":
		part = "main"
	case len(selected) == 1:
		part = selected[0]
	case len(selected) > 1:
		part = "multi"
	}
	ext := ".sql"
	if method == MethodSQLiteFile {
		ext = ".db"
	}
	name := safeName(connName) + "-" + safeName(part) + "-" + at.UTC().Format("20060102-150405") + ext + compressExt(compression)
	if encrypted {
		name += cryptstream.Ext
	}
	return name
}

// ---------------- 写入链 ----------------

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// fanout 把同一份数据写给多个目标；某个目标失败时放弃它、继续写其余目标，全部失败才报错。
type fanout struct {
	ups    []*target.Upload
	names  []string
	failed []error
}

func (f *fanout) Write(p []byte) (int, error) {
	alive := 0
	for i, u := range f.ups {
		if f.failed[i] != nil {
			continue
		}
		if _, err := u.Write(p); err != nil {
			f.failed[i] = err
			u.Abort(err)
			continue
		}
		alive++
	}
	if alive == 0 {
		return 0, fmt.Errorf("全部存储目标都写入失败：%s", f.errSummary())
	}
	return len(p), nil
}

func (f *fanout) errSummary() string {
	var parts []string
	for i, e := range f.failed {
		if e != nil {
			parts = append(parts, f.names[i]+"："+e.Error())
		}
	}
	return strings.Join(parts, "；")
}

func newCompressor(w io.Writer, kind string, level int) (io.WriteCloser, error) {
	switch kind {
	case "zstd":
		return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)), zstd.WithEncoderConcurrency(2))
	case "gzip":
		return gzip.NewWriterLevel(w, level)
	case "none", "":
		return nopWriteCloser{w}, nil
	}
	return nil, fmt.Errorf("未知的压缩方式 %q", kind)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// decodeStream 解密（需要时）并解压，返回原始数据流。
func decodeStream(r io.Reader, compression string, encrypted bool, passphrase string) (io.ReadCloser, error) {
	if encrypted {
		dr, err := cryptstream.NewReader(r, passphrase)
		if err != nil {
			return nil, err
		}
		r = dr
	}
	switch compression {
	case "zstd":
		d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(2))
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	case "gzip":
		return gzip.NewReader(bufio.NewReaderSize(r, 256<<10))
	case "none", "":
		return io.NopCloser(r), nil
	}
	return nil, fmt.Errorf("未知的压缩方式 %q", compression)
}

// ---------------- 启动 ----------------

// StartBackup 开始一次备份（异步执行），返回运行记录。trigger：manual | schedule | retry。
func (s *Service) StartBackup(ctx context.Context, p *Plan, trigger string, attempt int, userID string) (*Run, error) {
	c, err := s.Conns.Get(ctx, p.ConnectionID)
	if err != nil {
		return nil, err
	}
	runID := ids.New()
	release, ok := s.acquire(c.ID, runID)
	if !ok {
		return nil, apperr.Conflict("该连接正在执行备份、还原或校验，请稍后再试")
	}
	now := s.now()
	r := &Run{ID: runID, WorkspaceID: p.WorkspaceID, PlanID: p.ID, PlanName: p.Name, ConnectionID: c.ID, ConnectionName: c.Name, Driver: c.Driver,
		Trigger: trigger, Attempt: attempt, Status: StatusRunning, Method: p.Method, Databases: "[]", Compression: p.Compression, Encrypted: p.Encrypt,
		Manifest: "{}", Warnings: "[]", VerifyDetail: "{}", CreatedBy: userID, StartedAt: now.UnixMilli()}
	if p.Encrypt {
		pass, err := s.Box.Open(p.Passphrase, planAAD(p.ID))
		if err != nil || pass == "" {
			release()
			return nil, apperr.Bad("备份密码无法解密，请在计划里重新设置")
		}
		r.Passphrase = s.Box.Seal(pass, runAAD(r.ID))
	}
	if err := s.Store.Add(ctx, "backup_runs", r); err != nil {
		release()
		return nil, err
	}
	rctx, cancel := context.WithTimeout(s.stopCtx, RunTimeout)
	s.setCancel(r.ID, cancel)
	prog := &progress{Kind: "backup", ID: r.ID, WorkspaceID: r.WorkspaceID, ConnectionID: c.ID, PlanID: p.ID, phase: "queued"}
	finish := s.track(prog)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		defer cancel()
		defer s.setCancel(r.ID, nil)
		// 全局并发上限
		select {
		case s.sem <- struct{}{}:
		case <-rctx.Done():
			s.finishRun(r, p, nil, rctx.Err(), finish)
			return
		}
		defer func() { <-s.sem }()
		prog.set(func(p *progress) { p.phase = "running" })
		res, err := s.execute(rctx, r, p, c, prog)
		s.finishRun(r, p, res, err, finish)
	}()
	return r, nil
}

type execResult struct {
	manifest *BackupManifest
	files    []FileRec
	warnings []string
}

// execute 执行一次备份：导出 → 压缩 → 加密 → 同时写多个目标。
func (s *Service) execute(ctx context.Context, r *Run, p *Plan, c *conns.Connection, prog *progress) (*execResult, error) {
	res := &execResult{}
	// 打开存储目标
	type opened struct {
		rec *Target
		tg  target.Target
	}
	var tgs []opened
	defer func() {
		for _, o := range tgs {
			o.tg.Close()
		}
	}()
	for _, id := range p.TargetIDs() {
		t, err := s.GetTarget(ctx, id)
		if err != nil {
			res.warnings = append(res.warnings, "存储目标已删除，跳过："+id)
			continue
		}
		tg, err := s.OpenTarget(ctx, t)
		if err != nil {
			res.warnings = append(res.warnings, err.Error())
			res.files = append(res.files, FileRec{RunID: r.ID, TargetID: t.ID, Status: "failed", Error: err.Error()})
			continue
		}
		tgs = append(tgs, opened{t, tg})
	}
	if len(tgs) == 0 {
		return res, errors.New("没有可用的存储目标")
	}
	sc, err := s.Conns.SQL(ctx, c)
	if err != nil {
		return res, err
	}
	info, err := sc.Server(ctx)
	if err != nil {
		return res, fmt.Errorf("读取服务器版本失败：%w", err)
	}
	r.ServerVersion = info.Version
	// 库清单
	dbs := p.DatabaseList()
	if c.Driver == "mysql" {
		if len(dbs) == 0 {
			all, err := sc.Databases(ctx)
			if err != nil {
				return res, fmt.Errorf("读取库清单失败：%w", err)
			}
			for _, d := range all {
				if !d.System {
					dbs = append(dbs, d.Name)
				}
			}
			if len(dbs) == 0 {
				return res, errors.New("没有可备份的库")
			}
		}
	} else {
		dbs = []string{"main"}
	}
	r.Databases = toJSON(dbs)
	started := s.now()
	r.FileName = backupFileName(c.Name, c.Driver, p.DatabaseList(), p.Method, p.Compression, p.Encrypt, started)
	dir := r.WorkspaceID + "/" + c.ID + "/"
	path := dir + r.FileName

	// 写入链：生产者 → raw 计数/旁观 → 缓冲 → 压缩 → 加密 → 哈希/计数 → 各目标
	fan := &fanout{}
	for _, o := range tgs {
		fan.ups = append(fan.ups, target.NewUpload(ctx, o.tg, path))
		fan.names = append(fan.names, o.rec.Name)
		fan.failed = append(fan.failed, nil)
	}
	abortAll := func(cause error) {
		for i, u := range fan.ups {
			if fan.failed[i] == nil {
				u.Abort(cause)
			}
		}
	}
	sum := sha256.New()
	var outW io.Writer = io.MultiWriter(sum, countWriter{fan, &prog.out})
	var enc io.WriteCloser
	if p.Encrypt {
		pass, err := s.Box.Open(r.Passphrase, runAAD(r.ID))
		if err != nil {
			abortAll(err)
			return res, err
		}
		enc, err = cryptstream.NewWriter(outW, pass)
		if err != nil {
			abortAll(err)
			return res, err
		}
		outW = enc
	}
	comp, err := newCompressor(outW, p.Compression, p.CompressionLevel)
	if err != nil {
		abortAll(err)
		return res, err
	}
	bw := bufio.NewWriterSize(comp, 1<<20)
	var rawW io.Writer = countWriter{bw, &prog.raw}

	manifest := &BackupManifest{Format: "lemi-backup-manifest/1", BackupID: r.ID, PlanID: p.ID, WorkspaceID: r.WorkspaceID, ConnectionID: c.ID,
		ConnectionName: c.Name, Driver: c.Driver, Method: p.Method, ServerProduct: info.Product, ServerVersion: info.Version, File: r.FileName,
		Compression: p.Compression, CompressionLevel: p.CompressionLevel, Encrypted: p.Encrypt, StartedAt: started.UnixMilli()}
	if p.Encrypt {
		manifest.Encryption = "LEMIENC1 (argon2id + AES-256-GCM)"
	}
	var produceErr error
	switch p.Method {
	case MethodLemi:
		produceErr = s.produceLemi(ctx, sc, c, dbs, rawW, manifest, prog)
	case MethodMysqldump:
		produceErr = s.produceMysqldump(ctx, c, info, dbs, rawW, manifest, prog, &res.warnings)
	case MethodSQLiteFile:
		produceErr = s.produceSQLiteFile(ctx, sc, rawW, manifest, prog)
	default:
		produceErr = fmt.Errorf("未知的备份方式 %q", p.Method)
	}
	r.Tool = manifest.Tool
	if produceErr == nil {
		produceErr = bw.Flush()
	}
	if produceErr == nil {
		produceErr = comp.Close()
	}
	if produceErr == nil && enc != nil {
		produceErr = enc.Close()
	}
	if produceErr == nil && ctx.Err() != nil {
		produceErr = ctx.Err()
	}
	if produceErr != nil {
		abortAll(produceErr)
		return res, produceErr
	}
	prog.set(func(p *progress) { p.phase = "uploading" })
	r.Size, r.RawSize, r.SHA256 = prog.out.Load(), prog.raw.Load(), hex.EncodeToString(sum.Sum(nil))
	manifest.Size, manifest.RawSize, manifest.SHA256, manifest.FinishedAt = r.Size, r.RawSize, r.SHA256, s.now().UnixMilli()
	manifest.Warnings = append(manifest.Warnings, res.warnings...)
	mb, _ := marshalManifest(manifest)
	okCount := 0
	for i, u := range fan.ups {
		rec := FileRec{RunID: r.ID, TargetID: tgs[i].rec.ID, Path: path, Size: r.Size}
		err := fan.failed[i]
		if err == nil {
			err = u.Commit()
		}
		if err == nil {
			mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			err = tgs[i].tg.Put(mctx, path+".manifest.json", strings.NewReader(string(mb)), int64(len(mb)))
			cancel()
			if err != nil {
				err = fmt.Errorf("写备份清单失败：%w", err)
			}
		}
		if err != nil {
			rec.Status, rec.Error, rec.Size = "failed", err.Error(), 0
			res.warnings = append(res.warnings, "存储目标「"+tgs[i].rec.Name+"」失败："+err.Error())
		} else {
			rec.Status = "ok"
			okCount++
		}
		res.files = append(res.files, rec)
	}
	res.manifest = manifest
	if okCount == 0 {
		return res, fmt.Errorf("全部存储目标都失败：%s", strings.Join(res.warnings, "；"))
	}
	return res, nil
}

func marshalManifest(m *BackupManifest) ([]byte, error) {
	return jsonIndent(m)
}

// produceLemi 用自研导出器导出。
func (s *Service) produceLemi(ctx context.Context, sc driver.SQLConn, c *conns.Connection, dbs []string, w io.Writer, m *BackupManifest, prog *progress) error {
	d := sqldump.Dialect(sc.Dialect())
	db := sc.DB()
	if d == sqldump.SQLite {
		db = sc.ReadDB()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("连接数据库失败：%w", err)
	}
	defer conn.Close()
	host := ""
	if params, err := s.Conns.DecryptParams(c); err == nil {
		if h, ok := params["host"].(string); ok && h != "" {
			host = h
			if pt := fmt.Sprint(params["port"]); pt != "" && pt != "<nil>" {
				host = net.JoinHostPort(h, pt)
			}
		}
	}
	opts := sqldump.ExportOptions{Dialect: d, ExtendedInsert: true, ConnectionName: c.Name, HostLabel: host,
		Progress: func(p sqldump.Progress) {
			prog.rows.Store(p.Rows)
			prog.set(func(x *progress) {
				x.database, x.table, x.tablesDone, x.tablesTotal = p.Database, p.Table, p.TablesDone, p.TablesTotal
			})
		}}
	if d == sqldump.MySQL {
		opts.Databases, opts.CreateDatabase = dbs, true
	}
	man, err := sqldump.Export(ctx, conn, w, opts)
	if err != nil {
		return err
	}
	m.Tool = man.Tool
	m.Databases = man.Databases
	m.HashAlgorithm = "lemi-sqldump v1"
	m.Warnings = append(m.Warnings, man.Warnings...)
	return nil
}

// clientConn 生成 mysqldump/mysql 的连接参数。经 SSH/代理，或需要执行网络策略时，经本机回环转发；
// 校验主机名（verify-full）且直连时直接连真实主机（拨号前先按网络策略检查解析出的地址）。
func (s *Service) clientConn(ctx context.Context, c *conns.Connection) (dumptools.Conn, func(), error) {
	params, err := s.Conns.DecryptParams(c)
	if err != nil {
		return dumptools.Conn{}, nil, err
	}
	str := func(k string) string {
		if v, ok := params[k].(string); ok {
			return v
		}
		if v, ok := params[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	port, _ := strconv.Atoi(str("port"))
	if f, ok := params["port"].(float64); ok {
		port = int(f)
	}
	dc := dumptools.Conn{Host: str("host"), Port: port, User: str("user"), Password: str("password"),
		TLSMode: str("tls_mode"), TLSCA: str("tls_ca"), TLSCert: str("tls_cert"), TLSKey: str("tls_key")}
	nc, err := s.Conns.DecryptNetwork(c)
	if err != nil {
		return dc, nil, err
	}
	sys := s.Settings.System(ctx)
	nc.Policy = netdial.ParsePolicy(sys.ConnAllow, sys.ConnDeny)
	d := netdial.New(nc)
	if dc.TLSMode == "verify-full" && d.Direct() {
		d.Close()
		// 直连且要校验主机名：客户端直接连真实主机名，先检查它解析出的全部地址
		var addrs []netip.Addr
		if ip, err := netip.ParseAddr(dc.Host); err == nil {
			addrs = []netip.Addr{ip}
		} else if addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", dc.Host); err != nil {
			return dc, nil, fmt.Errorf("解析主机名 %s 失败：%w", dc.Host, err)
		}
		for _, a := range addrs {
			if err := nc.Policy.Check(a); err != nil {
				return dc, nil, err
			}
		}
		return dc, func() {}, nil
	}
	dc.Dial = d.DialContext
	return dc, func() { d.Close() }, nil
}

// produceMysqldump 用 mysqldump 导出。
func (s *Service) produceMysqldump(ctx context.Context, c *conns.Connection, info driver.ServerInfo, dbs []string, w io.Writer, m *BackupManifest, prog *progress, warns *[]string) error {
	if s.Tools == nil {
		return errors.New("mysqldump 不可用")
	}
	prog.set(func(p *progress) { p.phase = "preparing_tool" })
	tool, tw, err := s.Tools.Resolve(ctx, info.Product, info.Version, true, func(done, total int64) {
		prog.set(func(p *progress) { p.phase = "downloading_tool" })
	})
	*warns = append(*warns, tw...)
	if err != nil {
		return err
	}
	prog.set(func(p *progress) { p.phase = "running" })
	cc, closeFn, err := s.clientConn(ctx, c)
	if err != nil {
		return err
	}
	defer closeFn()
	obs := &dumpObserver{
		onTable: func(db, t string) {
			prog.set(func(p *progress) { p.database, p.table = db, t; p.tablesDone++ })
		},
		onRows: func(n int64) { prog.rows.Add(n) },
	}
	opts := dumptools.DumpOptions{Databases: dbs, NoColumnStatistics: dumptools.Is57(info.Product, info.Version) || info.Product == "MariaDB"}
	dw, err := tool.Dump(ctx, cc, opts, s.tmpDir("client"), io.MultiWriter(w, obs))
	*warns = append(*warns, dw...)
	if err != nil {
		return err
	}
	m.Tool = "mysqldump " + tool.Version
	m.Databases = obs.manifest()
	return nil
}

// produceSQLiteFile 用 VACUUM INTO 得到一致的文件快照，统计每张表的行数与内容哈希后写入数据流。
// 快照必须先落在数据目录的临时文件里（VACUUM INTO 只能写文件），写完即删。
func (s *Service) produceSQLiteFile(ctx context.Context, sc driver.SQLConn, w io.Writer, m *BackupManifest, prog *progress) error {
	pc, ok := sc.(interface{ Path() string })
	if !ok {
		return errors.New("不是 SQLite 文件连接")
	}
	tmp := s.tmpDir("snapshot")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	snap := filepath.Join(tmp, ids.New()+".db")
	defer os.Remove(snap)
	src, err := openSQLite(pc.Path(), true)
	if err != nil {
		return err
	}
	_, err = src.ExecContext(ctx, `VACUUM INTO ?`, snap)
	src.Close()
	if err != nil {
		return fmt.Errorf("生成 SQLite 快照失败：%w", err)
	}
	tables, err := sqliteManifest(ctx, snap)
	if err != nil {
		return err
	}
	m.Tool = "sqlite VACUUM INTO"
	m.Databases = []sqldump.DBManifest{{Name: "main", Tables: tables}}
	m.HashAlgorithm = "lemi-sqldump v1"
	f, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		prog.total.Store(fi.Size())
	}
	_, err = io.Copy(w, ctxReader{ctx, f})
	return err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// openSQLite 打开一个 SQLite 文件（readOnly 时只读，不加 query_only，VACUUM INTO 需要）。
func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	mode := "rwc"
	if readOnly {
		mode = "ro"
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode="+mode+"&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// sqliteTables 列出用户表。
func sqliteTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// sqliteManifest 统计 SQLite 文件里每张表的行数与内容哈希。
func sqliteManifest(ctx context.Context, path string) ([]sqldump.TableManifest, error) {
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	names, err := sqliteTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	out := []sqldump.TableManifest{}
	for _, n := range names {
		rows, h, err := sqldump.TableChecksum(ctx, conn, sqldump.SQLite, "main", n)
		if err != nil {
			return nil, fmt.Errorf("统计表 %s 失败：%w", n, err)
		}
		out = append(out, sqldump.TableManifest{Name: n, Rows: rows, Hash: h})
	}
	return out, nil
}

// finishRun 写结果、通知、审计、保留策略、失败重试、备份后校验。
func (s *Service) finishRun(r *Run, p *Plan, res *execResult, err error, finish func(map[string]any)) {
	ctx := context.Background()
	if res == nil {
		res = &execResult{}
	}
	r.FinishedAt = store.Now()
	canceled := errors.Is(err, context.Canceled)
	switch {
	case err == nil:
		r.Status = StatusSuccess
	case canceled:
		r.Status, r.Error = StatusCanceled, "已取消"
	case errors.Is(err, context.DeadlineExceeded):
		r.Status, r.Error = StatusFailed, "超过 23 小时，已中止"
	default:
		r.Status, r.Error = StatusFailed, err.Error()
	}
	if res.manifest != nil {
		b, _ := marshalManifest(res.manifest)
		r.Manifest = string(b)
	}
	r.Warnings = toJSON(dedup(res.warnings))
	if r.Status != StatusSuccess {
		r.Size, r.SHA256 = 0, ""
	}
	_ = s.Store.Put(ctx, "backup_runs", r)
	for _, f := range res.files {
		if r.Status != StatusSuccess && f.Status == "ok" {
			f.Status, f.Error = "failed", "备份失败"
		}
		_, _ = s.Store.Exec(ctx, `INSERT INTO backup_files (run_id, target_id, path, size, status, error) VALUES (?,?,?,?,?,?)
			ON CONFLICT(run_id, target_id) DO UPDATE SET path = excluded.path, size = excluded.size, status = excluded.status, error = excluded.error`,
			f.RunID, f.TargetID, f.Path, f.Size, f.Status, f.Error)
	}
	finish(map[string]any{"status": r.Status, "error": r.Error, "size": r.Size})
	s.Audit.Record(audit.Entry{UserID: r.CreatedBy, WorkspaceID: r.WorkspaceID, ConnectionID: r.ConnectionID, Source: map[bool]string{true: audit.SrcUI, false: audit.SrcSystem}[r.Trigger == "manual"],
		Action: "backup.run", Target: r.FileName, OK: r.Status == StatusSuccess, Error: r.Error, DurationMS: r.FinishedAt - r.StartedAt},
		map[string]any{"run_id": r.ID, "plan_id": r.PlanID, "trigger": r.Trigger, "method": r.Method, "size": r.Size, "status": r.Status})
	// 最新的计划（执行期间可能被修改或删除）
	cur, perr := s.GetPlan(ctx, p.ID)
	if perr != nil {
		cur = nil
	}
	link := "/w/" + r.WorkspaceID + "/backups?run=" + r.ID
	switch r.Status {
	case StatusSuccess:
		if cur != nil {
			_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET retry_at = 0, retry_attempt = 0 WHERE id = ?`, cur.ID)
			s.notifyWS(r.WorkspaceID, jsonStrings(cur.NotifySuccess), notify.Event{Kind: notify.EvBackupSuccess, Level: "info",
				Title:   "备份成功：" + r.ConnectionName,
				Message: fmt.Sprintf("计划「%s」备份完成：%s，%s，用时 %s。", r.PlanName, r.FileName, humanBytes(r.Size), time.Duration(r.FinishedAt-r.StartedAt)*time.Millisecond),
				Link:    link})
			s.applyRetention(ctx, cur)
			if cur.VerifyRule().AfterBackup {
				if _, err := s.StartVerify(ctx, r, ""); err != nil {
					s.Log.Warn("备份后校验没有启动", "run", r.ID, "err", err)
				}
			}
		}
	case StatusFailed:
		if cur != nil {
			if (r.Trigger == "schedule" || r.Trigger == "retry") && r.Attempt < len(RetryDelays) {
				at := s.now().Add(RetryDelays[r.Attempt]).UnixMilli()
				_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET retry_at = ?, retry_attempt = ? WHERE id = ?`, at, r.Attempt+1, cur.ID)
			} else {
				_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET retry_at = 0, retry_attempt = 0 WHERE id = ?`, cur.ID)
			}
			s.notifyWS(r.WorkspaceID, jsonStrings(cur.NotifyFailure), notify.Event{Kind: notify.EvBackupFailed, Level: "error",
				Title:   "备份失败：" + r.ConnectionName,
				Message: fmt.Sprintf("计划「%s」备份失败（第 %d 次尝试）：%s", r.PlanName, r.Attempt+1, r.Error), Link: link})
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

