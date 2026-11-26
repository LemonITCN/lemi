// Package aitest 是智能体相关测试共用的夹具：临时元数据库、用户与工作空间、SQLite 连接与示例数据、工具服务。
// 只在测试里引用。
package aitest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/sqlite"
	"github.com/LemonITCN/lemi/lemi-service/internal/reports"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// Fixture 是测试环境。
type Fixture struct {
	Store    *store.Store
	Accounts *accounts.Service
	Settings *settings.Service
	Conns    *conns.Service
	Audit    *audit.Log
	Box      *crypt.Box
	Log      *slog.Logger
	Tools    *tools.Services
	Owner    *accounts.User
	Member   *accounts.User
	Viewer   *accounts.User
	WS       string
	Dir      string
}

// New 建一个测试环境。
func New(t *testing.T) *Fixture {
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
	set := settings.New(st)
	f := &Fixture{Store: st, Accounts: acc, Settings: set, Audit: au, Box: box, Log: log, Dir: dir}
	mk := func(name string) *accounts.User {
		u := &accounts.User{Username: name, PasswordHash: "x"}
		if err := acc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	f.Owner, f.Member, f.Viewer = mk("owner"), mk("member"), mk("viewer")
	ws, err := acc.CreateWorkspace(ctx, "测试空间", "", f.Owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.WS = ws.ID
	if err := acc.SetMember(ctx, ws.ID, f.Member.ID, accounts.WSMember); err != nil {
		t.Fatal(err)
	}
	if err := acc.SetMember(ctx, ws.ID, f.Viewer.ID, accounts.WSViewer); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed")
	_ = os.MkdirAll(allowed, 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "sqlite"), 0o700)
	f.Conns = conns.New(st, acc, set, box, au, log, driver.SQLiteEnv{AllowedDirs: []string{allowed}, UploadDir: filepath.Join(dir, "sqlite")})
	t.Cleanup(func() { f.Conns.Close() })
	res := results.New(st, filepath.Join(dir, "results"))
	ch := charts.New(st)
	rp := reports.New(st, set, acc, ch, res)
	f.Tools = &tools.Services{Conns: f.Conns, Settings: set, Accounts: acc, Audit: au, Results: res, Charts: ch, Reports: rp, Log: log}
	return f
}

// AllowFull 打开或关闭「允许完全放开档位」。
func (f *Fixture) AllowFull(t *testing.T, on bool) {
	t.Helper()
	sys := f.Settings.System(context.Background())
	sys.AllowAIFullAccess = on
	if err := f.Settings.SetSystem(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
}

// SQLite 建一个带示例数据的 SQLite 连接：users(id, name, email, city)、orders(id, user_id, amount, status, created_at)。
func (f *Fixture) SQLite(t *testing.T, name, aiLevel string, allow []string) *conns.Connection {
	t.Helper()
	path := filepath.Join(f.Dir, "allowed", name+".db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT, city TEXT)`,
		`CREATE TABLE IF NOT EXISTS orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), amount REAL NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n == 0 {
		cities := []string{"北京", "上海", "深圳"}
		for i := 1; i <= 30; i++ {
			if _, err := db.Exec(`INSERT INTO users (id, name, email, city) VALUES (?,?,?,?)`, i, fmt.Sprintf("用户%d", i), fmt.Sprintf("u%d@example.com", i), cities[i%3]); err != nil {
				t.Fatal(err)
			}
		}
		for i := 1; i <= 120; i++ {
			st := "paid"
			if i%7 == 0 {
				st = "refunded"
			}
			if _, err := db.Exec(`INSERT INTO orders (id, user_id, amount, status, created_at) VALUES (?,?,?,?,?)`, i, i%30+1, float64(i*10)+0.5, st, fmt.Sprintf("2026-%02d-%02d", i%12+1, i%28+1)); err != nil {
				t.Fatal(err)
			}
		}
	}
	db.Close()
	if aiLevel == conns.AIFull {
		f.AllowFull(t, true)
		defer f.AllowFull(t, false)
	}
	c, err := f.Conns.Create(context.Background(), f.WS, f.Owner.ID, &conns.Input{Name: name, Driver: "sqlite", Params: map[string]any{"file": path}, AILevel: aiLevel, AIAllow: allow})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Env 构造一个工具环境（默认：所有者、内置智能体、交互审批但没有审批函数）。
func (f *Fixture) Env(u *accounts.User) *tools.Env {
	return &tools.Env{Ctx: context.Background(), Svc: f.Tools, User: u, WorkspaceID: f.WS, Source: audit.SrcAgent, Approval: tools.ApprovalInteractive}
}
