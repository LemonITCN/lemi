package app

import (
	"context"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/health"
)

// Health 健康检查服务。
func (a *App) Health() *health.Service { s, _ := a.Get("health").(*health.Service); return s }

func init() {
	Register(Module{Name: "health", Order: 210, Init: func(a *App) error {
		loc := time.Local
		if a.Cfg.Timezone != "" {
			if l, err := time.LoadLocation(a.Cfg.Timezone); err == nil {
				loc = l
			} else {
				a.Log.Warn("时区配置不正确，健康检查时间线使用本机时区", "tz", a.Cfg.Timezone, "err", err)
			}
		}
		d := health.Deps{Store: a.Store, Conns: a.Conns(), Accounts: a.Accounts, Hub: a.Hub, Log: a.Log, Location: loc}
		if n := a.Notify(); n != nil {
			d.Notify = n
		}
		s := health.New(d)
		a.Set("health", s)
		a.Web.AddMount(s.Routes)
		return nil
	}, Start: func(ctx context.Context, a *App) { a.Health().Run(ctx) }})
}
