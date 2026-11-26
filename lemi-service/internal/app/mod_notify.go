package app

import (
	"context"

	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/tasks"
)

// 通知服务给定时智能体任务发结果通知。
var _ tasks.TaskNotifier = (*notify.Service)(nil)

// Notify 通知服务。
func (a *App) Notify() *notify.Service { s, _ := a.Get("notify").(*notify.Service); return s }

func init() {
	Register(Module{Name: "notify", Order: 200, Init: func(a *App) error {
		s := notify.New(notify.Deps{Store: a.Store, Accounts: a.Accounts, Settings: a.Settings, Box: a.Box, Audit: a.Audit,
			Log: a.Log, PublicURL: a.Cfg.PublicURL})
		// 首次启动写入系统邮件服务器（LEMI_SMTP_*）：设置项从未保存过才写，之后以界面为准
		if err := s.SeedSystemSMTP(context.Background(), a.Cfg.SMTP); err != nil {
			a.Log.Warn("没有写入系统邮件服务器", "err", err)
		}
		a.Set("notify", s)
		a.Web.AddMount(s.Routes)
		// 安全事件（登录锁定、新设备登录、改口令、关闭两步验证）给用户本人发邮件
		a.Auth.OnEvent = s.SecurityEvent
		return nil
	}, Start: func(ctx context.Context, a *App) { a.Notify().Run(ctx) }})
}
