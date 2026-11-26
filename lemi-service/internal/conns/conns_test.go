package conns

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver/sqlite"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

type fixture struct {
	s      *Service
	owner  *accounts.User
	viewer *accounts.User
	ws     string
	dir    string
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
	box, _ := crypt.New(make([]byte, 32))
	acc := accounts.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := audit.New(st, log)
	owner := &accounts.User{Username: "owner", PasswordHash: "x"}
	viewer := &accounts.User{Username: "viewer", PasswordHash: "x"}
	if err := acc.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := acc.CreateUser(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	ws, err := acc.CreateWorkspace(ctx, "测试", "", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := acc.SetMember(ctx, ws.ID, viewer.ID, accounts.WSViewer); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed")
	os.MkdirAll(allowed, 0o700)
	os.MkdirAll(filepath.Join(dir, "sqlite"), 0o700)
	s := New(st, acc, settings.New(st), box, au, log, driver.SQLiteEnv{AllowedDirs: []string{allowed}, UploadDir: filepath.Join(dir, "sqlite")})
	t.Cleanup(func() { s.Close() })
	return &fixture{s: s, owner: owner, viewer: viewer, ws: ws.ID, dir: dir}
}

func (f *fixture) sqliteConn(t *testing.T, file string) *Connection {
	t.Helper()
	path := filepath.Join(f.dir, "allowed", file)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE IF NOT EXISTS seed (x INTEGER)")
	db.Close()
	c, err := f.s.Create(context.Background(), f.ws, f.owner.ID, &Input{Name: "本地", Driver: "sqlite", Params: map[string]any{"file": path}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSQLiteConsoleAndGrid(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "a.db")
	lvl := f.s.Access(ctx, f.owner, c)
	if lvl != LevelManage {
		t.Fatalf("所有者应为 manage，实际 %s", lvl)
	}
	cs, err := f.s.OpenConsole(ctx, f.owner, c, lvl, "", "ui", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	res := f.s.Exec(ctx, cs, `CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, note TEXT, data BLOB, score REAL);
		INSERT INTO users (name, note, data, score) VALUES ('alice', 'a;b', X'00FF', 1.5), ('bob', NULL, NULL, 2.25), ('carol', '中文', X'', 0.1);
		SELECT * FROM users ORDER BY id`, ExecOptions{StopOnError: true})
	if len(res) != 3 {
		t.Fatalf("应执行 3 条语句，实际 %d：%+v", len(res), res)
	}
	for _, r := range res {
		if r.Kind == "error" {
			t.Fatalf("执行出错：%s", r.Error)
		}
	}
	sel := res[2]
	if len(sel.Rows) != 3 || sel.Rows[0][1] != "alice" || sel.Rows[0][2] != "a;b" {
		t.Fatalf("查询结果不对：%+v", sel.Rows)
	}
	if b, ok := sel.Rows[0][3].(driver.BinValue); !ok || b.Len != 2 {
		t.Fatalf("二进制应编码为 $bin：%#v", sel.Rows[0][3])
	}
	if sel.Rows[1][4] != "2.25" || sel.Rows[2][4] != "0.1" {
		t.Fatalf("浮点编码不对：%v %v", sel.Rows[1][4], sel.Rows[2][4])
	}
	f.s.CloseConsole(cs.ID)

	// 表格页
	data, tinfo, err := f.s.TableData(ctx, f.owner, c, lvl, DataQuery{Database: "main", Table: "users", Where: "score > 1", OrderBy: "id DESC", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 2 || data.Editable == nil || !data.Editable.OK || strings.Join(tinfo.Key, ",") != "id" {
		t.Fatalf("表格数据或可编辑判断不对：%+v %+v", data.Rows, data.Editable)
	}
	if _, _, err := f.s.TableData(ctx, f.owner, c, lvl, DataQuery{Database: "main", Table: "users", Where: "1=1; DROP TABLE users"}); err == nil {
		t.Fatal("筛选条件带第二条语句应被拒绝")
	}

	// 提交修改：改、增、删
	var key1 map[string]any
	json.Unmarshal([]byte(`{"id":"1"}`), &key1)
	req := &ApplyRequest{Database: "main", Table: "users", Changes: []Change{
		{Op: "update", Key: map[string]any{"id": "1"}, Set: map[string]any{"name": "alice2", "data": map[string]any{"$bin": "AAEC"}}, Old: map[string]any{"name": "alice"}},
		{Op: "insert", Values: map[string]any{"name": "dave", "note": nil}},
		{Op: "delete", Key: map[string]any{"id": "2"}},
	}}
	ar, err := f.s.Apply(ctx, f.owner, c, lvl, req, "127.0.0.1")
	if err != nil || ar.FailedAt != -1 || ar.Applied != 3 {
		t.Fatalf("提交失败：%+v %v", ar, err)
	}
	// 乐观并发：旧值不对时整体回滚
	req2 := &ApplyRequest{Database: "main", Table: "users", Changes: []Change{
		{Op: "update", Key: map[string]any{"id": "3"}, Set: map[string]any{"name": "x"}, Old: map[string]any{"name": "carol"}},
		{Op: "update", Key: map[string]any{"id": "1"}, Set: map[string]any{"name": "y"}, Old: map[string]any{"name": "alice"}},
	}}
	ar2, err := f.s.Apply(ctx, f.owner, c, lvl, req2, "127.0.0.1")
	if err != nil || ar2.FailedAt != 1 || !strings.Contains(ar2.Error, "已被别人修改") {
		t.Fatalf("应因第二处冲突失败：%+v %v", ar2, err)
	}
	data, _, _ = f.s.TableData(ctx, f.owner, c, lvl, DataQuery{Database: "main", Table: "users", OrderBy: "id"})
	names := []string{}
	for _, r := range data.Rows {
		names = append(names, r[1].(string))
	}
	if strings.Join(names, ",") != "alice2,carol,dave" {
		t.Fatalf("回滚后数据不对：%v", names)
	}

	// 查看者：只读会话拦截写入（数据库层只读打开）
	vlvl := f.s.Access(ctx, f.viewer, c)
	if vlvl != LevelRead {
		t.Fatalf("查看者应为 read，实际 %s", vlvl)
	}
	vcs, err := f.s.OpenConsole(ctx, f.viewer, c, vlvl, "", "ui", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := f.s.Exec(ctx, vcs, "DELETE FROM users", ExecOptions{})
	if r[0].Kind != "error" {
		t.Fatal("只读会话不应能删除")
	}
	// 即使绕过分类器（直接用底层连接写），只读打开的文件也写不进去
	if _, err := vcs.S.Conn().ExecContext(ctx, "DELETE FROM users"); err == nil {
		t.Fatal("只读打开的连接不应能写入")
	}
	f.s.CloseConsole(vcs.ID)
	if _, err := f.s.Apply(ctx, f.viewer, c, vlvl, req, "127.0.0.1"); err == nil {
		t.Fatal("查看者不能提交修改")
	}

	// ATTACH 一律禁止
	cs2, _ := f.s.OpenConsole(ctx, f.owner, c, lvl, "", "ui", false, 0)
	r = f.s.Exec(ctx, cs2, "ATTACH DATABASE '/etc/passwd' AS x", ExecOptions{})
	if r[0].Kind != "error" || !strings.Contains(r[0].Error, "ATTACH") {
		t.Fatalf("ATTACH 应被拒绝：%+v", r[0])
	}
	r = f.s.Exec(ctx, cs2, "VACUUM INTO '/tmp/x.db'", ExecOptions{})
	if r[0].Kind != "error" {
		t.Fatal("VACUUM INTO 应被拒绝")
	}
	// 查询结果可编辑判断
	r = f.s.Exec(ctx, cs2, "SELECT id, name AS n FROM users WHERE id > 0", ExecOptions{})
	f.s.EditableForQuery(ctx, c, cs2, r[0])
	if r[0].Editable == nil || !r[0].Editable.OK || r[0].Columns[1].Origin != "name" {
		t.Fatalf("单表带主键的查询应可编辑：%+v", r[0].Editable)
	}
	r = f.s.Exec(ctx, cs2, "SELECT name, count(*) FROM users GROUP BY name", ExecOptions{})
	f.s.EditableForQuery(ctx, c, cs2, r[0])
	if r[0].Editable == nil || r[0].Editable.OK {
		t.Fatal("聚合查询不应可编辑")
	}
	f.s.CloseConsole(cs2.ID)
}

func TestSQLitePathEscape(t *testing.T) {
	f := setup(t)
	env := f.s.SQLiteEnv()
	outside := filepath.Join(f.dir, "outside.db")
	os.WriteFile(outside, []byte("SQLite format 3\x00"), 0o600)
	if _, err := sqlite.ResolvePath(env, outside); err == nil {
		t.Fatal("白名单外的文件应被拒绝")
	}
	if _, err := sqlite.ResolvePath(env, filepath.Join(env.AllowedDirs[0], "..", "outside.db")); err == nil {
		t.Fatal(".. 跳出应被拒绝")
	}
	link := filepath.Join(env.AllowedDirs[0], "link.db")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := sqlite.ResolvePath(env, link); err == nil {
			t.Fatal("经符号链接跳出应被拒绝")
		}
	}
	if _, err := sqlite.ResolvePath(env, "upload:../x"); err == nil {
		t.Fatal("非法上传标识应被拒绝")
	}
	if _, err := sqlite.ResolvePath(env, "relative.db"); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
}

func TestSecretsWriteOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := &Input{Name: "m", Driver: "mysql", Params: map[string]any{"host": "db.example.com", "port": 3306, "user": "root", "password": "s3cret!"},
		Network: map[string]map[string]any{"ssh": {"enabled": true, "host": "jump", "port": 22, "user": "u", "password": "sshpw"}}}
	c, err := f.s.Create(ctx, f.ws, f.owner.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Params, "s3cret") || strings.Contains(c.Network, "sshpw") {
		t.Fatal("密钥必须加密保存")
	}
	v := f.s.View(c, LevelManage)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "enc:v1") {
		t.Fatalf("接口不应返回密钥或密文：%s", b)
	}
	// 不传口令 → 保留
	in2 := *in
	in2.Params = map[string]any{"host": "db.example.com", "port": 3306, "user": "root"}
	in2.Network = map[string]map[string]any{"ssh": {"enabled": true, "host": "jump", "port": 22, "user": "u"}}
	if err := f.s.Update(ctx, c, &in2); err != nil {
		t.Fatal(err)
	}
	p, _ := f.s.DecryptParams(c)
	if p["password"] != "s3cret!" {
		t.Fatal("没传口令时应保留原值")
	}
	nc, _ := f.s.DecryptNetwork(c)
	if nc.SSH.Password != "sshpw" {
		t.Fatal("没传 SSH 口令时应保留原值")
	}
	// 主机改了 → 旧口令作废
	in3 := in2
	in3.Params = map[string]any{"host": "evil.example.com", "port": 3306, "user": "root"}
	if err := f.s.Update(ctx, c, &in3); err != nil {
		t.Fatal(err)
	}
	p, _ = f.s.DecryptParams(c)
	if p["password"] != nil && p["password"] != "" {
		t.Fatal("主机变了以后不能把旧口令带去新地址")
	}
	// 密文挪到别的记录上解不开
	other, _ := f.s.Create(ctx, f.ws, f.owner.ID, &Input{Name: "o", Driver: "mysql", Params: map[string]any{"host": "x", "port": 3306, "user": "r"}})
	other.Params = strings.Replace(other.Params, `"host"`, `"password":"`+mustSealed(t, f, c)+`","host"`, 1)
	if _, err := f.s.DecryptParams(other); err == nil {
		t.Fatal("密文绑定了记录 ID，挪到别的记录应解密失败")
	}
}

func mustSealed(t *testing.T, f *fixture, c *Connection) string {
	var m map[string]any
	c2, _ := f.s.Get(context.Background(), c.ID)
	json.Unmarshal([]byte(c2.Params), &m)
	in := &Input{Name: "z", Driver: "mysql", Params: map[string]any{"host": "evil.example.com", "port": 3306, "user": "root", "password": "again"}}
	f.s.Update(context.Background(), c2, in)
	json.Unmarshal([]byte(c2.Params), &m)
	return m["password"].(string)
}
