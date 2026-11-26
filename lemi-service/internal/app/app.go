// Package app 装配全部服务并管理生命周期。
//
// 业务模块在自己的文件（mod_<名称>.go）里用 Register 注册装配函数，按 Order 从小到大执行，
// 这样增加模块不需要改动本文件。模块之间通过 App.Set / App.Get 共享服务实例。
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/logx"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/gofrs/flock"
	"golang.org/x/crypto/acme/autocert"
)

// Module 是一个业务模块的装配函数。
type Module struct {
	Name  string
	Order int
	// Init 创建服务、注册路由（a.Web.AddMount）、放进 a.Set。
	Init func(a *App) error
	// Start 启动后台任务（可为空），ctx 结束时应退出。
	Start func(ctx context.Context, a *App)
}

var (
	modMu   sync.Mutex
	modules []Module
)

// Register 注册模块（在各 mod_*.go 的 init 里调用）。
func Register(m Module) {
	modMu.Lock()
	defer modMu.Unlock()
	modules = append(modules, m)
}

// App 是整个服务。
type App struct {
	Cfg        *config.Config
	Log        *slog.Logger
	Store      *store.Store
	Box        *crypt.Box
	Settings   *settings.Service
	Accounts   *accounts.Service
	Auth       *auth.Service
	Audit      *audit.Log
	Hub        *realtime.Hub
	Web        *web.Server
	FirstStart bool // 本次启动时数据库里还没有任何用户

	lock       *flock.Flock
	logCloser  io.Closer
	setupToken string
	svcMu      sync.RWMutex
	services   map[string]any
	mods       []Module
}

// Set 登记一个服务实例。
func (a *App) Set(name string, v any) {
	a.svcMu.Lock()
	a.services[name] = v
	a.svcMu.Unlock()
}

// Get 取服务实例（不存在为 nil）。
func (a *App) Get(name string) any {
	a.svcMu.RLock()
	defer a.svcMu.RUnlock()
	return a.services[name]
}

// New 打开数据目录并装配全部服务。
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	a := &App{Cfg: cfg, services: map[string]any{}}
	a.Log, a.logCloser = logx.Setup(cfg.Log.Level, cfg.Path("logs"), true)
	a.lock = flock.New(cfg.Path("lemi.lock"))
	ok, err := a.lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("锁定数据目录失败：%w", err)
	}
	if !ok {
		return nil, fmt.Errorf("数据目录 %s 正在被另一个 Lemi 进程使用", cfg.DataDir)
	}
	key, created, err := crypt.LoadOrCreateKey(cfg.MasterKey, cfg.Path("master.key"))
	if err != nil {
		a.Close()
		return nil, err
	}
	if created {
		a.Log.Info("已生成主密钥", "file", cfg.Path("master.key"))
	}
	if a.Box, err = crypt.New(key); err != nil {
		a.Close()
		return nil, err
	}
	if !created && cfg.MasterKey == "" {
		if fi, err := os.Stat(cfg.Path("master.key")); err == nil && fi.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(cfg.Path("master.key"), 0o600)
		}
	}
	dbPath := cfg.Path("lemi.db")
	a.Store, err = store.Open(ctx, dbPath)
	if err != nil {
		snap := store.LatestSnapshot(cfg.Path("snapshots"))
		a.Close()
		if snap != "" {
			return nil, fmt.Errorf("%w\n元数据库可能已损坏。可以停止服务后用最新快照恢复：把 %s 复制为 %s（并删除同目录的 lemi.db-wal、lemi.db-shm）", err, snap, dbPath)
		}
		return nil, err
	}
	a.Settings = settings.New(a.Store)
	a.Accounts = accounts.New(a.Store)
	a.Auth = auth.New(a.Store, a.Accounts, a.Settings, a.Box)
	a.Audit = audit.New(a.Store, a.Log)
	a.Hub = realtime.New(a.Log)
	netsafe.AllowPrivate = a.Settings.System(ctx).AllowPrivateEgress

	n, err := a.Accounts.CountUsers(ctx)
	if err != nil {
		a.Close()
		return nil, err
	}
	a.FirstStart = n == 0
	if a.FirstStart {
		if err := a.firstStart(ctx); err != nil {
			a.Close()
			return nil, err
		}
	}
	a.Web = web.NewServer(web.Deps{Log: a.Log, Cfg: cfg, Store: a.Store, Auth: a.Auth, Accounts: a.Accounts, Settings: a.Settings,
		Audit: a.Audit, Hub: a.Hub, Box: a.Box, Started: time.Now(),
		SetupToken:  func() string { return a.setupToken },
		OnSetupDone: func() { a.setupToken = ""; _ = os.Remove(cfg.Path("setup-token")) }})

	modMu.Lock()
	a.mods = append([]Module(nil), modules...)
	modMu.Unlock()
	sort.SliceStable(a.mods, func(i, j int) bool { return a.mods[i].Order < a.mods[j].Order })
	for _, m := range a.mods {
		if m.Init == nil {
			continue
		}
		if err := m.Init(a); err != nil {
			a.Close()
			return nil, fmt.Errorf("初始化模块 %s 失败：%w", m.Name, err)
		}
	}
	return a, nil
}

// firstStart 首次启动：按环境变量直接建管理员，否则生成一次性初始化令牌。
func (a *App) firstStart(ctx context.Context) error {
	if a.Cfg.AdminUsername != "" && a.Cfg.AdminPassword != "" {
		if err := auth.CheckPasswordPolicy(a.Cfg.AdminPassword, a.Cfg.AdminUsername); err != nil {
			return fmt.Errorf("LEMI_ADMIN_PASSWORD 不符合口令要求：%w", err)
		}
		hash, err := auth.HashPassword(a.Cfg.AdminPassword)
		if err != nil {
			return err
		}
		u := &accounts.User{Username: a.Cfg.AdminUsername, Email: a.Cfg.AdminEmail, Role: accounts.RoleAdmin, PasswordHash: hash}
		if err := a.Accounts.CreateUser(ctx, u); err != nil {
			return fmt.Errorf("创建初始管理员失败：%w", err)
		}
		name := a.Cfg.WorkspaceName
		if name == "" {
			name = "默认空间"
		}
		if _, err := a.Accounts.CreateWorkspace(ctx, name, "", u.ID); err != nil {
			return err
		}
		a.Audit.Record(audit.Entry{UserID: u.ID, Username: u.Username, Action: "setup.admin_created", Source: audit.SrcSystem}, map[string]any{"from": "env"})
		a.Log.Info("已按环境变量创建初始管理员（首次登录需绑定两步验证）", "username", u.Username)
		return nil
	}
	tokPath := a.Cfg.Path("setup-token")
	if b, err := os.ReadFile(tokPath); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		a.setupToken = strings.TrimSpace(string(b))
	} else {
		a.setupToken = ids.RandomToken(40)
		if err := os.WriteFile(tokPath, []byte(a.setupToken+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// SetupToken 返回初始化令牌（已初始化时为空）。
func (a *App) SetupToken() string { return a.setupToken }

// Run 启动 HTTP 服务与后台任务，直到 ctx 结束。
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	bg := func(fn func(ctx context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
		}()
	}
	bg(func(ctx context.Context) { a.Store.RunSnapshots(ctx, a.Cfg.Path("snapshots"), a.Log) })
	bg(a.housekeeping)
	for _, m := range a.mods {
		if m.Start != nil {
			m := m
			bg(func(ctx context.Context) { m.Start(ctx, a) })
		}
	}

	srv := &http.Server{
		Handler:           a.Web.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", a.Cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听 %s 失败：%w", a.Cfg.Listen, err)
	}
	var redirect *http.Server
	scheme := "http"
	switch a.Cfg.TLS.Mode {
	case "manual":
		cert, err := tls.LoadX509KeyPair(a.Cfg.TLS.Cert, a.Cfg.TLS.Key)
		if err != nil {
			ln.Close()
			return fmt.Errorf("加载 TLS 证书失败：%w", err)
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		ln = tls.NewListener(ln, srv.TLSConfig)
		scheme = "https"
	case "acme":
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(a.Cfg.Path("acme")),
			HostPolicy: autocert.HostWhitelist(a.Cfg.TLS.ACMEDomains...), Email: a.Cfg.TLS.ACMEEmail}
		srv.TLSConfig = m.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS12
		ln = tls.NewListener(ln, srv.TLSConfig)
		scheme = "https"
		redirect = &http.Server{Addr: a.Cfg.TLS.HTTPRedirect, Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.Log.Warn("HTTP 跳转与证书验证端口启动失败（自动证书需要 80 端口可达）", "addr", a.Cfg.TLS.HTTPRedirect, "err", err)
			}
		}()
	}
	a.printBanner(scheme)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			wg.Wait()
			return err
		}
	}
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	if redirect != nil {
		_ = redirect.Shutdown(sctx)
	}
	cancel()
	wg.Wait()
	return nil
}

func (a *App) printBanner(scheme string) {
	host := a.Cfg.Listen
	if h, p, err := net.SplitHostPort(host); err == nil && (h == "" || h == "0.0.0.0" || h == "::") {
		host = "localhost:" + p
	}
	url := scheme + "://" + host
	if a.Cfg.PublicURL != "" {
		url = a.Cfg.PublicURL
	}
	a.Log.Info("Lemi 已启动", "version", version.Version, "url", url, "data", a.Cfg.DataDir)
	if a.setupToken != "" {
		fmt.Fprintf(os.Stderr, "\n================ Lemi 首次启动 ================\n打开 %s 完成初始化，初始化令牌：\n\n    %s\n\n（令牌同时保存在 %s，初始化完成后自动删除）\n==============================================\n\n",
			url, a.setupToken, filepath.Join(a.Cfg.DataDir, "setup-token"))
	}
	if scheme == "http" {
		if h, _, err := net.SplitHostPort(a.Cfg.Listen); err == nil && h != "127.0.0.1" && h != "localhost" && h != "::1" && !a.Cfg.InDocker && len(a.Cfg.TrustedProxies) == 0 {
			a.Log.Warn("当前以 HTTP 监听非本机地址：若直接暴露在公网，登录口令与数据库内容会明文传输。请启用 TLS（LEMI_TLS_MODE=acme 或 manual）或放在 HTTPS 反向代理之后")
		}
	}
}

// housekeeping 定期清理：过期会话、审计保留期。
func (a *App) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.Auth.CleanupExpired(ctx)
		a.Audit.Prune(ctx, a.Settings.System(ctx).AuditRetentionDays)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Close 释放资源。
func (a *App) Close() {
	if a.Audit != nil {
		a.Audit.Flush()
	}
	for i := len(a.mods) - 1; i >= 0; i-- {
		if c, ok := a.Get(a.mods[i].Name).(io.Closer); ok {
			_ = c.Close()
		}
	}
	if a.Store != nil {
		_ = a.Store.Close()
	}
	if a.lock != nil {
		_ = a.lock.Unlock()
	}
	if a.logCloser != nil {
		_ = a.logCloser.Close()
	}
}
