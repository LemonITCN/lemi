package app

import (
	"context"
	"os"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/backup"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// 备份服务给智能体与 MCP 提供 list_backups / backup_status / health_status。
var _ tools.BackupInfo = (*backup.Service)(nil)

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
		tm := dumptools.NewManager(a.Cfg.Path("tools"), a.Cfg.Tools.BaseURL)
		// Docker 镜像里预装的客户端（/data/tools 会被数据卷覆盖，所以预装在别处）
		tm.Preinstalled = os.Getenv("LEMI_TOOLS_PREINSTALLED")
		d := backup.Deps{Store: a.Store, Conns: a.Conns(), Accounts: a.Accounts, Settings: a.Settings, Box: a.Box, Audit: a.Audit, Hub: a.Hub,
			Log: a.Log, Tools: tm, Downloads: a.Downloads(), DataDir: a.Cfg.DataDir, Location: loc}
		if n := a.Notify(); n != nil {
			d.Notify = n
		}
		if h := a.Health(); h != nil {
			d.Health = h
		}
		s := backup.New(d)
		tm.Custom = s.CustomTools
		a.Set("backup", s)
		a.Web.AddMount(s.Routes)
		return nil
	}, Start: func(ctx context.Context, a *App) { a.Backup().Run(ctx) }})
}
