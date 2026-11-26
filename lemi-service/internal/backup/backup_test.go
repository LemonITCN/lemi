package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/sqlite"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/sched"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

func TestMain(m *testing.M) {
	cryptstream.DefaultParams = cryptstream.Params{Time: 1, MemoryKiB: 64, Threads: 1}
	os.Exit(m.Run())
}

// fakeNotify 记录发出的通知。
type fakeNotify struct {
	mu     sync.Mutex
	events []notify.Event
}

func (f *fakeNotify) Send(ctx context.Context, ws string, ids []string, ev notify.Event) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}
func (f *fakeNotify) CheckChannels(ctx context.Context, ws string, ids []string) error { return nil }
func (f *fakeNotify) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Kind)
	}
	return out
}

type fixture struct {
	s      *Service
	cs     *conns.Service
	owner  *accounts.User
	viewer *accounts.User
	ws     string
	dir    string
	nt     *fakeNotify
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "lemi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := crypt.New(bytes.Repeat([]byte{7}, 32))
	acc := accounts.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := audit.New(st, log)
	owner := &accounts.User{Username: "owner", PasswordHash: "x"}
	viewer := &accounts.User{Username: "viewer", PasswordHash: "x"}
	for _, u := range []*accounts.User{owner, viewer} {
		if err := acc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := acc.CreateWorkspace(ctx, "测试", "", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	acc.SetMember(ctx, ws.ID, viewer.ID, accounts.WSViewer)
	allowed := filepath.Join(dir, "allowed")
	os.MkdirAll(allowed, 0o700)
	os.MkdirAll(filepath.Join(dir, "sqlite"), 0o700)
	set := settings.New(st)
	sys := set.System(ctx)
	sys.AllowPrivateEgress = true
	set.SetSystem(ctx, sys)
	cs := conns.New(st, acc, set, box, au, log, driver.SQLiteEnv{AllowedDirs: []string{allowed}, UploadDir: filepath.Join(dir, "sqlite")})
	t.Cleanup(func() { cs.Close() })
	nt := &fakeNotify{}
	s := New(Deps{Store: st, Conns: cs, Accounts: acc, Settings: set, Box: box, Audit: au, Hub: realtime.New(log), Log: log, Notify: nt,
		Downloads: downloads.New(), DataDir: dir})
	t.Cleanup(func() { s.Close() })
	return &fixture{s: s, cs: cs, owner: owner, viewer: viewer, ws: ws.ID, dir: dir, nt: nt}
}

// sqliteDB 在白名单目录里建一个 SQLite 文件并写入测试数据，返回连接。
func (f *fixture) sqliteDB(t *testing.T, file string, seed bool) *conns.Connection {
	t.Helper()
	path := filepath.Join(f.dir, "allowed", file)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if seed {
		for _, q := range []string{
			`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, note TEXT, avatar BLOB, score REAL)`,
			`CREATE TABLE logs (msg TEXT)`,
			`CREATE INDEX logs_msg ON logs(msg)`,
			`CREATE VIEW v_users AS SELECT id, name FROM users`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		tx, _ := db.Begin()
		for i := 0; i < 500; i++ {
			tx.Exec(`INSERT INTO users (name, note, avatar, score) VALUES (?, ?, ?, ?)`, "用户"+strings.Repeat("x", i%7), "含'引号\n换行\\反斜杠", []byte{0, byte(i), 255}, float64(i)/3)
			tx.Exec(`INSERT INTO logs (msg) VALUES (?)`, "log")
		}
		tx.Commit()
	} else {
		db.Exec(`CREATE TABLE placeholder (x INTEGER)`)
	}
	db.Close()
	c, err := f.cs.Create(context.Background(), f.ws, f.owner.ID, &conns.Input{Name: "库" + file, Driver: "sqlite", Params: map[string]any{"file": path}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) localTarget(t *testing.T, name string) *Target {
	t.Helper()
	p := filepath.Join(f.dir, "nas-"+name)
	os.MkdirAll(p, 0o700)
	tg, err := f.s.CreateTarget(context.Background(), f.ws, f.owner.ID, &TargetInput{Name: name, Kind: "local", Config: map[string]any{"path": p}})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func (f *fixture) plan(t *testing.T, c *conns.Connection, method string, encrypt bool, targets ...*Target) *Plan {
	t.Helper()
	in := &PlanInput{ConnectionID: c.ID, Name: "计划-" + method, Method: method, Encrypt: encrypt, Schedule: sched.Spec{Kind: "manual"}}
	if encrypt {
		in.Passphrase = "一个足够长的备份密码-123"
	}
	for _, tg := range targets {
		in.Targets = append(in.Targets, tg.ID)
	}
	p, err := f.s.CreatePlan(context.Background(), f.ws, f.owner.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wait 等全部后台任务结束（包括备份后自动启动的校验）。
func (f *fixture) wait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if f.s.Active() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待后台任务超时")
}

func (f *fixture) runBackup(t *testing.T, p *Plan) *Run {
	t.Helper()
	r, err := f.s.StartBackup(context.Background(), p, "manual", 0, f.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	r, err = f.s.GetRun(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// dbDigest 读出 SQLite 文件里用户表的全部内容（按行排序），用于比对还原结果。
func dbDigest(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var b strings.Builder
	for _, q := range []string{`SELECT id, name, note, hex(avatar), score FROM users ORDER BY id`, `SELECT msg FROM logs ORDER BY rowid`, `SELECT seq FROM sqlite_sequence WHERE name = 'users'`} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s：%v", q, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(fmtRow(vals), "\n", "\\n"), "\t", " ")))
			b.WriteByte('\n')
		}
		rows.Close()
	}
	return b.String()
}

func fmtRow(vals []any) string {
	b, _ := json.Marshal(vals)
	return string(b)
}

func TestSQLiteFileSnapshotEndToEnd(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := f.sqliteDB(t, "src.db", true)
	t1, t2 := f.localTarget(t, "nas1"), f.localTarget(t, "nas2")
	p := f.plan(t, src, MethodSQLiteFile, true, t1, t2)
	r := f.runBackup(t, p)
	if r.Status != StatusSuccess {
		t.Fatalf("备份失败：%s", r.Error)
	}
	if !strings.HasSuffix(r.FileName, ".db.zst.lemienc") || !strings.Contains(r.FileName, "-main-") {
		t.Fatalf("文件名：%s", r.FileName)
	}
	files, _ := f.s.RunFiles(ctx, r.ID)
	if len(files) != 2 || files[0].Status != "ok" || files[1].Status != "ok" {
		t.Fatalf("文件记录：%+v", files)
	}
	// 两个目标上都有文件与清单，内容相同
	var blobs [][]byte
	for _, tg := range []*Target{t1, t2} {
		var cfg map[string]any
		json.Unmarshal([]byte(tg.Config), &cfg)
		p := filepath.Join(cfg["path"].(string), filepath.FromSlash(files[0].Path))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !cryptstream.IsEncrypted(b) || int64(len(b)) != r.Size {
			t.Fatalf("目标文件不对：%d 字节，记录 %d", len(b), r.Size)
		}
		blobs = append(blobs, b)
		mb, err := os.ReadFile(p + ".manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		var m BackupManifest
		json.Unmarshal(mb, &m)
		if m.SHA256 != r.SHA256 || len(m.Databases) != 1 || len(m.Databases[0].Tables) != 2 || m.Databases[0].Tables[1].Rows != 500 || m.Databases[0].Tables[1].Hash == "" {
			t.Fatalf("清单：%s", mb)
		}
	}
	if !bytes.Equal(blobs[0], blobs[1]) {
		t.Fatal("两个目标上的文件不一致")
	}
	if k := f.nt.kinds(); len(k) != 0 {
		t.Fatalf("没配置渠道不应发通知：%v", k)
	}
	// 还原校验
	if _, err := f.s.StartVerify(ctx, r, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	r, _ = f.s.GetRun(ctx, r.ID)
	if r.VerifyStatus != "ok" {
		t.Fatalf("校验失败：%s", r.VerifyDetail)
	}
	// 下载为 .db（解密解压）
	job, err := f.s.DownloadJob(ctx, r, f.owner.ID, "sql", "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := job.Write(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("SQLite format 3\x00")) || job.Filename != strings.TrimSuffix(r.FileName, ".zst.lemienc") {
		t.Fatalf("下载：%s %d 字节", job.Filename, buf.Len())
	}
	// 原样下载
	job, _ = f.s.DownloadJob(ctx, r, f.owner.ID, "raw", t2.ID)
	buf.Reset()
	job.Write(ctx, &buf)
	if !bytes.Equal(buf.Bytes(), blobs[1]) {
		t.Fatal("原样下载内容不对")
	}
	// 还原到另一个非空的 SQLite 连接：先预检，不确认时拒绝
	dst := f.sqliteDB(t, "dst.db", false)
	items, _, err := f.s.Precheck(ctx, r, dst, nil)
	if err != nil || len(items) != 1 || !items[0].NeedConfirm {
		t.Fatalf("预检：%+v %v", items, err)
	}
	if _, err := f.s.StartRestore(ctx, r, dst, &RestoreInput{ConnectionID: dst.ID}, f.owner.ID); err == nil {
		t.Fatal("没有确认应拒绝还原")
	}
	rec, err := f.s.StartRestore(ctx, r, dst, &RestoreInput{ConnectionID: dst.ID, Confirm: map[string]string{"main": dst.Name}}, f.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	rec, _ = f.s.GetRestore(ctx, rec.ID)
	if rec.Status != StatusSuccess {
		t.Fatalf("还原失败：%s", rec.Error)
	}
	if a, b := dbDigest(t, filepath.Join(f.dir, "allowed", "src.db")), dbDigest(t, filepath.Join(f.dir, "allowed", "dst.db")); a != b || a == "" {
		t.Fatal("还原后数据不一致")
	}
	// 原文件留了备份
	ents, _ := os.ReadDir(filepath.Join(f.dir, "allowed"))
	found := false
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "dst.db.before-restore-") {
			found = true
		}
	}
	if !found {
		t.Fatal("还原前没有保留原文件")
	}
	// 还原后连接可以正常使用（连接池已重建）
	sc, err := f.cs.SQL(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sc.ReadDB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 500 {
		t.Fatalf("还原后查询：%d %v", n, err)
	}
	// 篡改目标上的文件：校验失败并通知
	p.NotifyFailure = toJSON([]string{"ch1"})
	f.s.Store.Put(ctx, "backup_plans", p)
	var cfg map[string]any
	json.Unmarshal([]byte(t1.Config), &cfg)
	fp := filepath.Join(cfg["path"].(string), filepath.FromSlash(files[0].Path))
	b, _ := os.ReadFile(fp)
	b[len(b)/2] ^= 1
	os.WriteFile(fp, b, 0o600)
	os.Remove(filepath.Join(func() string { json.Unmarshal([]byte(t2.Config), &cfg); return cfg["path"].(string) }(), filepath.FromSlash(files[0].Path)))
	if _, err := f.s.StartVerify(ctx, r, ""); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	r, _ = f.s.GetRun(ctx, r.ID)
	if r.VerifyStatus != "failed" || !strings.Contains(r.VerifyDetail, "损坏") {
		t.Fatalf("篡改后校验应失败：%s %s", r.VerifyStatus, r.VerifyDetail)
	}
	if k := f.nt.kinds(); len(k) == 0 || k[len(k)-1] != notify.EvVerifyFailed {
		t.Fatalf("校验失败应通知：%v", k)
	}
}

func TestSQLiteLemiDumpEndToEnd(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := f.sqliteDB(t, "a.db", true)
	tg := f.localTarget(t, "nas")
	p := f.plan(t, src, MethodLemi, false, tg)
	upd := &PlanInput{ConnectionID: src.ID, Name: p.Name, Method: MethodLemi, Compression: "gzip", Targets: []string{tg.ID},
		Schedule: sched.Spec{Kind: "manual"}, Verify: VerifyConfig{AfterBackup: true}, NotifySuccess: []string{"ok-ch"}}
	if err := f.s.UpdatePlan(ctx, p, upd); err != nil {
		t.Fatal(err)
	}
	r := f.runBackup(t, p)
	if r.Status != StatusSuccess {
		t.Fatalf("备份失败：%s", r.Error)
	}
	f.wait(t) // 备份后自动校验
	r, _ = f.s.GetRun(ctx, r.ID)
	if r.VerifyStatus != "ok" {
		t.Fatalf("备份后校验：%s %s", r.VerifyStatus, r.VerifyDetail)
	}
	var vr VerifyResult
	json.Unmarshal([]byte(r.VerifyDetail), &vr)
	if !vr.HashChecked || vr.TablesChecked != 2 || vr.Integrity != "ok" {
		t.Fatalf("校验结果：%+v", vr)
	}
	if !strings.HasSuffix(r.FileName, ".sql.gz") || r.Tool == "" {
		t.Fatalf("记录：%s %s", r.FileName, r.Tool)
	}
	if k := f.nt.kinds(); len(k) != 1 || k[0] != notify.EvBackupSuccess {
		t.Fatalf("成功通知：%v", k)
	}
	// 下载为 .sql：是 Lemi 格式的 SQL
	job, _ := f.s.DownloadJob(ctx, r, f.owner.ID, "sql", "")
	var buf bytes.Buffer
	if err := job.Write(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Lemi Dump SQL") || !strings.HasSuffix(job.Filename, ".sql") {
		t.Fatalf("下载为 SQL：%s", job.Filename)
	}
	// 还原到一个空的 SQLite 连接（自研导入器）
	dst := f.sqliteDB(t, "b.db", false)
	if _, err := f.s.StartRestore(ctx, r, dst, &RestoreInput{ConnectionID: dst.ID, Method: RestoreMySQLClient}, f.owner.ID); err == nil {
		t.Fatal("自研导出的备份不能用 mysql 客户端还原")
	}
	rec, err := f.s.StartRestore(ctx, r, dst, &RestoreInput{ConnectionID: dst.ID, Confirm: map[string]string{"main": dst.Name}}, f.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	rec, _ = f.s.GetRestore(ctx, rec.ID)
	if rec.Status != StatusSuccess || rec.Method != RestoreLemi {
		t.Fatalf("还原：%s %s", rec.Status, rec.Error)
	}
	if a, b := dbDigest(t, filepath.Join(f.dir, "allowed", "a.db")), dbDigest(t, filepath.Join(f.dir, "allowed", "b.db")); a != b {
		t.Fatalf("还原后数据不一致\n%s\n----\n%s", a[:200], b[:min(200, len(b))])
	}
}

func TestPartialTargetFailureAndCancel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := f.sqliteDB(t, "a.db", true)
	good := f.localTarget(t, "good")
	bad := f.localTarget(t, "bad")
	// 让 bad 目标的目录消失（像 NAS 掉线）
	var cfg map[string]any
	json.Unmarshal([]byte(bad.Config), &cfg)
	os.RemoveAll(cfg["path"].(string))
	p := f.plan(t, src, MethodLemi, true, good, bad)
	r := f.runBackup(t, p)
	if r.Status != StatusSuccess {
		t.Fatalf("一个目标失败时整体应成功：%s", r.Error)
	}
	files, _ := f.s.RunFiles(ctx, r.ID)
	st := map[string]string{}
	for _, fl := range files {
		st[fl.TargetID] = fl.Status
	}
	if st[good.ID] != "ok" || st[bad.ID] != "failed" || !strings.Contains(r.Warnings, "bad") {
		t.Fatalf("文件状态：%v 警告：%s", st, r.Warnings)
	}
	// 只剩坏目标：失败
	p2 := f.plan(t, src, MethodLemi, false, bad)
	r2 := f.runBackup(t, p2)
	if r2.Status != StatusFailed {
		t.Fatalf("全部目标失败应失败：%s", r2.Status)
	}
	// 同一连接同一时刻只能有一个任务
	release, _ := f.s.acquire(src.ID, "x")
	if _, err := f.s.StartBackup(ctx, p, "manual", 0, ""); err == nil {
		t.Fatal("连接忙时应拒绝")
	}
	release()
	// 删除被使用的目标被拒
	if err := f.s.DeleteTarget(ctx, good); err == nil {
		t.Fatal("被计划使用的目标不能删除")
	}
	// 删除备份：目标上的文件与记录都删掉
	if err := f.s.DeleteRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(good.Config), &cfg)
	left := 0
	filepath.Walk(cfg["path"].(string), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			left++
		}
		return nil
	})
	if left != 0 {
		t.Fatalf("删除备份后目标上还剩 %d 个文件", left)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.s.Store.Add(ctx, "backup_runs", &Run{ID: "01RUN", WorkspaceID: f.ws, ConnectionID: "c", Trigger: "manual", Status: StatusRunning, Databases: "[]", Manifest: "{}", Warnings: "[]", VerifyDetail: "{}", StartedAt: 1})
	f.s.recoverInterrupted(ctx)
	r, _ := f.s.GetRun(ctx, "01RUN")
	if r.Status != StatusFailed || !strings.Contains(r.Error, "重启") {
		t.Fatalf("%+v", r)
	}
}

func TestTargetSecretsWriteOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tg, err := f.s.CreateTarget(ctx, f.ws, f.owner.ID, &TargetInput{Name: "s3", Kind: "s3", Config: map[string]any{
		"endpoint": "https://s3.example.com", "bucket": "lemi-bk", "access_key": "AKIDEXAMPLE", "secret_key": "secret-example-value"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tg.Config, "secret-example-value") {
		t.Fatal("数据库里有明文密钥")
	}
	v := f.s.TargetView(tg, true)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "secret-example-value") || !strings.Contains(string(b), `"secrets_set":["secret_key"]`) {
		t.Fatalf("视图：%s", b)
	}
	// 不传密钥：保持原值
	if err := f.s.UpdateTarget(ctx, tg, &TargetInput{Name: "s3-2", Kind: "s3", Config: map[string]any{"endpoint": "https://s3.example.com", "bucket": "lemi-bk", "access_key": "AKIDEXAMPLE"}}); err != nil {
		t.Fatal(err)
	}
	plain, _ := f.s.plainConfig(tg)
	if !strings.Contains(string(plain), "secret-example-value") {
		t.Fatal("没有保留原密钥")
	}
	// 换了地址：必须重新输入
	if err := f.s.UpdateTarget(ctx, tg, &TargetInput{Name: "s3-2", Kind: "s3", Config: map[string]any{"endpoint": "https://evil.example.com", "bucket": "lemi-bk", "access_key": "AKIDEXAMPLE"}}); err == nil {
		t.Fatal("换地址后应要求重新输入密钥")
	}
	// 客户端传来的密文不接受
	sealed := tg.Config
	var m map[string]any
	json.Unmarshal([]byte(sealed), &m)
	if err := f.s.UpdateTarget(ctx, tg, &TargetInput{Name: "s3-3", Kind: "s3", Config: map[string]any{"endpoint": "https://evil.example.com", "bucket": "lemi-bk", "access_key": "A", "secret_key": m["secret_key"]}}); err == nil {
		t.Fatal("密文不应被当作新密钥接受")
	}
	// 不能改类型
	if err := f.s.UpdateTarget(ctx, tg, &TargetInput{Name: "x", Kind: "local", Config: map[string]any{}}); err == nil {
		t.Fatal("不能改类型")
	}
}

func TestPlanValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := f.sqliteDB(t, "a.db", true)
	tg := f.localTarget(t, "nas")
	bad := []*PlanInput{
		{ConnectionID: src.ID, Name: "", Method: MethodLemi, Targets: []string{tg.ID}},
		{ConnectionID: src.ID, Name: "x", Method: MethodMysqldump, Targets: []string{tg.ID}},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{"nope"}},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Encrypt: true},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Encrypt: true, Passphrase: "short"},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Compression: "bz2"},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Compression: "zstd", CompressionLevel: 30},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Schedule: sched.Spec{Kind: "daily", Time: "25:00"}},
		{ConnectionID: src.ID, Name: "x", Method: MethodLemi, Targets: []string{tg.ID}, Retention: Retention{Mode: "gfs"}},
		{ConnectionID: "nope", Name: "x", Method: MethodLemi, Targets: []string{tg.ID}},
	}
	for i, in := range bad {
		if _, err := f.s.CreatePlan(ctx, f.ws, f.owner.ID, in); err == nil {
			t.Errorf("第 %d 个应被拒绝", i)
		}
	}
	p, err := f.s.CreatePlan(ctx, f.ws, f.owner.ID, &PlanInput{ConnectionID: src.ID, Name: "日备", Method: MethodSQLiteFile, Targets: []string{tg.ID},
		Encrypt: true, Passphrase: "一个足够长的备份密码-123", Schedule: sched.Spec{Kind: "daily", Time: "03:00"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextRunAt <= time.Now().UnixMilli() || p.Compression != "zstd" || p.CompressionLevel != 5 || strings.Contains(p.Passphrase, "备份密码") {
		t.Fatalf("计划：%+v", p)
	}
	v, _ := json.Marshal(f.s.PlanView(p))
	if strings.Contains(string(v), "备份密码") || !strings.Contains(string(v), `"passphrase_set":true`) {
		t.Fatalf("视图泄露了密码：%s", v)
	}
	// 修改时不传密码：保持原值
	old := p.Passphrase
	if err := f.s.UpdatePlan(ctx, p, &PlanInput{ConnectionID: src.ID, Name: "日备", Method: MethodSQLiteFile, Targets: []string{tg.ID}, Encrypt: true, Schedule: sched.Spec{Kind: "daily", Time: "03:00"}}); err != nil {
		t.Fatal(err)
	}
	if p.Passphrase != old {
		t.Fatal("没传密码时应保持原值")
	}
}
