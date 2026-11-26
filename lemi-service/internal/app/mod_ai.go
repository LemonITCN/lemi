package app

import (
	"context"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/agent"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/mcpserver"
	"github.com/LemonITCN/lemi/lemi-service/internal/oauth"
	"github.com/LemonITCN/lemi/lemi-service/internal/reports"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/tasks"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// Agent 智能体服务。
func (a *App) Agent() *agent.Service { s, _ := a.Get("agent").(*agent.Service); return s }

// 智能体、MCP、图表、报告、定时任务（docs/spec.md §16、§17、§24）。
// 备份模块（a.Get("backup") 实现 tools.BackupInfo）与通知模块（a.Get("notify") 实现 tasks.TaskNotifier）在 200 段装配，
// 存在时自动接上，不存在时对应工具不出现、任务通知只记日志。
func init() {
	Register(Module{Name: "ai", Order: 300, Init: func(a *App) error {
		ctx := context.Background()
		cs := a.Conns()
		res := results.New(a.Store, a.Cfg.Path("results"))
		ch := charts.New(a.Store)
		rp := reports.New(a.Store, a.Settings, a.Accounts, ch, res)
		rp.BaseURL = func() string { return a.Cfg.PublicURL }
		ts := &tools.Services{Conns: cs, Settings: a.Settings, Accounts: a.Accounts, Audit: a.Audit, Results: res, Charts: ch, Reports: rp, Log: a.Log}
		if b, ok := a.Get("backup").(tools.BackupInfo); ok {
			ts.Backup = b
		}
		// 图表「重新执行刷新」：用户在界面上的操作，按用户自己的权限在只读会话里执行
		ch.Runner = func(ctx context.Context, u *accounts.User, src charts.Source) ([]driver.Column, [][]any, bool, error) {
			c, lvl, err := cs.Require(ctx, u, src.ConnectionID, conns.LevelRead)
			if err != nil {
				return nil, nil, false, err
			}
			env := &tools.Env{Ctx: ctx, Svc: ts, User: u, WorkspaceID: c.WorkspaceID, Source: audit.SrcUI}
			out, err := env.RunSelect(&tools.ConnAccess{Conn: c, UserLevel: lvl, AI: conns.AIRead}, src.Database, src.SQL)
			if err != nil {
				return nil, nil, false, err
			}
			return out.Columns, out.Rows, out.Truncated, nil
		}
		ag := agent.New(a.Store, a.Settings, a.Accounts, a.Box, a.Hub, a.Log, ts)
		if a.Cfg.Timezone != "" {
			if loc, err := time.LoadLocation(a.Cfg.Timezone); err == nil {
				ag.TZ = loc
			}
		}
		if err := ag.SeedOpenCode(ctx, a.Cfg.OpenCodeKey, a.Cfg.DefaultModel); err != nil {
			a.Log.Warn("写入 OpenCode Go 供应商失败", "err", err)
		}
		ag.Recover(ctx)
		a.Set("agent", ag)
		a.Set("ai", ag) // 退出时由 App.Close 调用 Close
		a.Web.AddMount(ag.Routes)
		a.Web.AddMount(rp.Routes)
		a.Web.AddPublic(rp.Public(a.Web))
		a.Web.BootstrapExtra = append(a.Web.BootstrapExtra, ag.Bootstrap)

		oa := oauth.New(a.Store, a.Accounts, cs, a.Settings, a.Log, oauth.BaseURL(a.Cfg.PublicURL))
		a.Set("oauth", oa)
		a.Web.AddPublic(oa.Public)
		a.Web.AddMount(oa.Routes)
		a.Web.MCP = mcpserver.Handler(mcpserver.Config{OAuth: oa, Tools: ts, Log: a.Log, PublicURL: a.Cfg.PublicURL})

		tk := tasks.New(a.Store, a.Accounts, cs, a.Settings, ag, a.Audit, a.Log)
		tk.BaseURL = a.Cfg.PublicURL
		if n, ok := a.Get("notify").(tasks.TaskNotifier); ok {
			tk.Notifier = n
		}
		a.Set("tasks", tk)
		a.Web.AddMount(tk.Routes)
		a.Set("results", res)
		return nil
	}, Start: func(ctx context.Context, a *App) {
		if tk, ok := a.Get("tasks").(*tasks.Service); ok {
			go tk.Run(ctx)
		}
		res, _ := a.Get("results").(*results.Store)
		oa, _ := a.Get("oauth").(*oauth.Service)
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if res != nil {
				res.Cleanup(ctx)
			}
			if oa != nil {
				oa.Cleanup(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}})
}
