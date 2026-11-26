package app

import (
	"context"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/backup"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
)

// Backup 备份服务。
func (a *App) Backup() *backup.Service { s, _ := a.Get("backup").(*backup.Service); return s }

func init() {
	Register(Module{Name: "backup", Order: 220, Init: func(a *App) error {
		loc := time.Local
		if a.Cfg.Timezone != "" {
			if l, err := time.LoadLocation(a.Cfg.Timezone); err == nil {
				loc = l
			}
		}
		tools := dumptools.NewManager(a.Cfg.Path("tools"), a.Cfg.Tools.BaseURL)
		d := backup.Deps{Store: a.Store, Conns: a.Conns(), Accounts: a.Accounts, Settings: a.Settings, Box: a.Box, Audit: a.Audit, Hub: a.Hub,
			Log: a.Log, Tools: tools, Downloads: a.Downloads(), DataDir: a.Cfg.DataDir, Location: loc}
		if n := a.Notify(); n != nil {
			d.Notify = n
		}
		s := backup.New(d)
		tools.Custom = s.CustomTools
		a.Set("backup", s)
		a.Web.AddMount(s.Routes)
		return nil
	}, Start: func(ctx context.Context, a *App) { a.Backup().Run(ctx) }})
}
