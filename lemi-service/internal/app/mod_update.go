package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/selfupdate"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// 在线升级（docs/spec.md §20）：每天检查一次 GitHub 最新正式版；实例管理员在界面上一键升级。
//
//	GET  /api/v1/admin/update   当前版本、最新版本、发布说明、能否自动升级、升级任务状态（?refresh=1 立即重新检查）
//	POST /api/v1/admin/update   开始升级（需要 10 分钟内验证过身份，记审计）。{"version": "x.y.z"} 可选，用于确认目标版本
//
// bootstrap 里给实例管理员附带 update（有新版本时）。

// Updater 在线升级器。
func (a *App) Updater() *selfupdate.Updater { s, _ := a.Get("update").(*selfupdate.Updater); return s }

// updateJob 是界面发起的升级任务状态（同一时间只有一个）。
type updateJob struct {
	mu        sync.Mutex
	state     string // idle | running | restarting | failed
	to        string
	errMsg    string
	startedAt time.Time
	by        string
}

func (j *updateJob) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.state
	if st == "" {
		st = "idle"
	}
	out := map[string]any{"state": st}
	if st != "idle" {
		out["to"], out["error"], out["started_at"], out["by"] = j.to, j.errMsg, j.startedAt.UnixMilli(), j.by
	}
	return out
}

const (
	updateFirstCheck = time.Minute
	updateInterval   = 24 * time.Hour
	// 界面上「立即检查」的最短间隔：GitHub 未登录接口每小时只给 60 次
	updateRefreshMin = time.Minute
)

func init() {
	Register(Module{Name: "update", Order: 900, Init: func(a *App) error {
		u := selfupdate.New()
		a.Set("update", u)
		job := &updateJob{}
		a.Web.AddMount(func(s *web.Server, r chi.Router) {
			r.Get("/admin/update", s.H(func(w http.ResponseWriter, r *http.Request) error { return a.hUpdateGet(s, job, w, r) }))
			r.Post("/admin/update", s.H(func(w http.ResponseWriter, r *http.Request) error { return a.hUpdatePost(s, job, w, r) }))
		})
		a.Web.BootstrapExtra = append(a.Web.BootstrapExtra, func(ctx context.Context, user *accounts.User, out map[string]any) {
			if !user.IsAdmin() {
				return
			}
			if rel, _, _ := u.Cached(); u.Newer(rel) {
				ok, _, _ := a.canAutoUpdate()
				out["update"] = map[string]any{"version": rel.Version, "url": rel.URL, "can_auto_update": ok}
			}
		})
		return nil
	}, Start: func(ctx context.Context, a *App) { a.updateLoop(ctx) }})
}

// canAutoUpdate 能否在线升级；不能时返回原因代码与说明。
func (a *App) canAutoUpdate() (bool, string, string) {
	u := a.Updater()
	switch {
	case a.Cfg.InDocker:
		return false, "docker", "Docker 部署不在容器里自升级：拉取新镜像后重建容器（docker compose pull && docker compose up -d）"
	case !selfupdate.Supported(u.GOOS, u.GOARCH):
		return false, "unsupported_platform", "当前平台（" + u.Platform() + "）没有官方发布包，请从源码构建"
	}
	if err := u.CanReplace(); err != nil {
		return false, "no_permission", err.Error()
	}
	return true, "", ""
}

// updateLoop 启动 1 分钟后检查一次，之后每天一次；关闭检查（LEMI_UPDATE_CHECK=false）时只清理旧文件。
func (a *App) updateLoop(ctx context.Context) {
	u := a.Updater()
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	if !wait(updateFirstCheck) {
		return
	}
	u.CleanupOld()
	if !a.Cfg.UpdateCheck() {
		return
	}
	notified := ""
	for {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		rel, err := u.Check(cctx)
		cancel()
		switch {
		case err != nil:
			a.Log.Debug("检查新版本失败", "err", err)
		case u.Newer(rel) && rel.Version != notified:
			notified = rel.Version
			a.Log.Info("发现 Lemi 新版本", "current", version.Version, "latest", rel.Version, "url", rel.URL)
		}
		if !wait(updateInterval) {
			return
		}
		u.CleanupOld()
	}
}

func requireInstanceAdmin(r *http.Request) error {
	if u := web.UserOf(r); u == nil || !u.IsAdmin() {
		return apperr.Forbidden("需要实例管理员权限")
	}
	return nil
}

func (a *App) hUpdateGet(s *web.Server, job *updateJob, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	u := a.Updater()
	rel, checkedAt, checkErr := u.Cached()
	if r.URL.Query().Get("refresh") == "1" && time.Since(checkedAt) >= updateRefreshMin {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		rel, checkErr = u.Check(ctx)
		cancel()
		_, checkedAt, _ = u.Cached()
	}
	ok, code, text := a.canAutoUpdate()
	out := map[string]any{
		"current": version.Version, "commit": version.Commit, "build_date": version.Date,
		"platform": u.Platform(), "auto_check": a.Cfg.UpdateCheck(), "in_docker": a.Cfg.InDocker,
		"latest": nil, "available": u.Newer(rel), "checked_at": nil, "check_error": "",
		"can_auto_update": ok, "reason": code, "reason_text": text, "job": job.view(),
	}
	if !checkedAt.IsZero() {
		out["checked_at"] = checkedAt.UnixMilli()
	}
	if checkErr != nil {
		out["check_error"] = checkErr.Error()
	}
	if rel != nil {
		out["latest"] = map[string]any{"version": rel.Version, "name": rel.Name, "notes": rel.Notes, "url": rel.URL,
			"published_at": rel.PublishedAt.UnixMilli()}
	}
	return web.OK(w, out)
}

func (a *App) hUpdatePost(s *web.Server, job *updateJob, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	var req struct {
		Version string `json:"version"`
	}
	if r.ContentLength > 0 {
		if err := web.ReadJSON(r, &req); err != nil {
			return err
		}
	}
	if ok, _, text := a.canAutoUpdate(); !ok {
		return apperr.Bad("%s", text)
	}
	u := a.Updater()
	rel, checkedAt, _ := u.Cached()
	if rel == nil || time.Since(checkedAt) > 10*time.Minute {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		var err error
		rel, err = u.Check(ctx)
		cancel()
		if err != nil {
			return apperr.New(502, "update_check_failed", err.Error())
		}
	}
	if !u.Newer(rel) {
		return apperr.Conflict("已经是最新版本")
	}
	if req.Version != "" && req.Version != rel.Version {
		return apperr.Conflict("最新版本已变为 " + rel.Version + "，请刷新后确认")
	}

	user := web.UserOf(r)
	job.mu.Lock()
	if job.state == "running" || job.state == "restarting" {
		job.mu.Unlock()
		return apperr.Conflict("正在升级")
	}
	job.state, job.to, job.errMsg, job.startedAt, job.by = "running", rel.Version, "", time.Now(), user.Username
	job.mu.Unlock()
	s.AuditR(r, audit.Entry{Action: "system.update.start", Target: rel.Version, OK: true},
		map[string]any{"from": version.Version, "to": rel.Version})

	ip := web.ClientIP(r)
	go a.runUpdate(job, rel, user, ip)
	web.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job.view()})
	return nil
}

// runUpdate 在后台下载、校验、替换，成功后请求重启。
func (a *App) runUpdate(job *updateJob, rel *selfupdate.Release, user *accounts.User, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	started := time.Now()
	res, err := a.Updater().Apply(ctx, rel)
	entry := audit.Entry{UserID: user.ID, Username: user.Username, IP: ip, Source: audit.SrcUI, Action: "system.update",
		Target: rel.Version, OK: err == nil, DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		entry.Error = err.Error()
		a.Audit.Record(entry, map[string]any{"from": version.Version, "to": rel.Version})
		a.Log.Error("在线升级失败", "to", rel.Version, "err", err)
		job.mu.Lock()
		job.state, job.errMsg = "failed", err.Error()
		job.mu.Unlock()
		a.Hub.SendToUser(user.ID, "notice", map[string]any{"kind": "update", "state": "failed", "version": rel.Version, "error": err.Error()})
		return
	}
	a.Audit.Record(entry, map[string]any{"from": res.From, "to": res.To, "exe": res.Exe})
	a.Log.Info("在线升级完成，正在重启", "from", res.From, "to", res.To)
	job.mu.Lock()
	job.state = "restarting"
	job.mu.Unlock()
	a.Hub.SendToUser(user.ID, "notice", map[string]any{"kind": "update", "state": "restarting", "version": res.To})
	// 留一点时间让通知与审计写出去，再开始正常退出
	time.Sleep(1500 * time.Millisecond)
	if err := selfupdate.RequestRestart(res.Exe); err != nil {
		a.Log.Error("升级后重启失败，请手动重启 Lemi", "err", err)
		job.mu.Lock()
		job.state, job.errMsg = "failed", "新版本已安装，但自动重启失败（"+err.Error()+"），请手动重启 Lemi"
		job.mu.Unlock()
	}
}
