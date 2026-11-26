package app

import (
	"context"

	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/sqlite"
)

// Conns 连接服务。
func (a *App) Conns() *conns.Service { s, _ := a.Get("conns").(*conns.Service); return s }

// Downloads 一次性下载。
func (a *App) Downloads() *downloads.Store { s, _ := a.Get("downloads").(*downloads.Store); return s }

func init() {
	Register(Module{Name: "conns", Order: 100, Init: func(a *App) error {
		dl := downloads.New()
		a.Set("downloads", dl)
		a.Web.AddMount(dl.Routes)
		conns.Downloads = dl
		conns.TmpDir = a.Cfg.Path("tmp", "imports")
		s := conns.New(a.Store, a.Accounts, a.Settings, a.Box, a.Audit, a.Log, driver.SQLiteEnv{AllowedDirs: a.Cfg.SQLite.AllowedDirs, UploadDir: a.Cfg.Path("sqlite")})
		a.Set("conns", s)
		a.Web.AddMount(s.Routes)
		return nil
	}, Start: func(ctx context.Context, a *App) { a.Conns().Run(ctx) }})
}
