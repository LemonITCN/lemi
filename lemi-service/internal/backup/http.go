package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/target"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载备份接口（docs/spec.md §23）。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := func(fn func(srv *web.Server, w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return srv.H(func(w http.ResponseWriter, r *http.Request) error { return fn(srv, w, r) })
	}
	r.Route("/workspaces/{ws}/backups", func(r chi.Router) {
		r.Get("/kinds", h(s.hKinds))
		r.Get("/methods", h(s.hMethods))
		r.Get("/settings", h(s.hSettingsGet))
		r.Put("/settings", h(s.hSettingsPut))
		r.Get("/targets", h(s.hTargetList))
		r.Post("/targets", h(s.hTargetCreate))
		r.Post("/targets/test", h(s.hTargetTestNew))
		r.Get("/targets/{id}", h(s.hTargetGet))
		r.Put("/targets/{id}", h(s.hTargetUpdate))
		r.Delete("/targets/{id}", h(s.hTargetDelete))
		r.Post("/targets/{id}/test", h(s.hTargetTest))
		r.Post("/targets/{id}/host-key", h(s.hTargetHostKey))
		r.Get("/targets/{id}/usage", h(s.hTargetUsage))
		r.Post("/targets/{id}/baidu/device", h(s.hBaiduDevice))
		r.Post("/targets/{id}/baidu/poll", h(s.hBaiduPoll))
		r.Get("/plans", h(s.hPlanList))
		r.Post("/plans", h(s.hPlanCreate))
		r.Get("/plans/{id}", h(s.hPlanGet))
		r.Put("/plans/{id}", h(s.hPlanUpdate))
		r.Delete("/plans/{id}", h(s.hPlanDelete))
		r.Post("/plans/{id}/run", h(s.hPlanRun))
		r.Get("/runs", h(s.hRunList))
		r.Get("/runs/{id}", h(s.hRunGet))
		r.Delete("/runs/{id}", h(s.hRunDelete))
		r.Post("/runs/{id}/cancel", h(s.hRunCancel))
		r.Post("/runs/{id}/download", h(s.hRunDownload))
		r.Post("/runs/{id}/verify", h(s.hRunVerify))
		r.Post("/runs/{id}/restore/precheck", h(s.hRestorePrecheck))
		r.Post("/runs/{id}/restore", h(s.hRestore))
		r.Get("/restores", h(s.hRestoreList))
		r.Get("/restores/{id}", h(s.hRestoreGet))
		r.Post("/tasks/{id}/cancel", h(s.hTaskCancel))
	})
	r.Route("/admin/dumptools", func(r chi.Router) {
		r.Get("/", h(s.hToolsStatus))
		r.Put("/custom", h(s.hToolsCustom))
		r.Post("/{version}/install", h(s.hToolsInstall))
		r.Delete("/{version}", h(s.hToolsRemove))
	})
}

func wsOf(r *http.Request) string { return chi.URLParam(r, "ws") }

// ---------------- 类型与方式 ----------------

func (s *Service) hKinds(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"kinds": target.Kinds(), "is_instance_admin": web.UserOf(r).IsAdmin()})
}

// hMethods 某连接可用的备份方式（mysqldump 在本平台是否可用、MariaDB 警告等）。
func (s *Service) hMethods(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	c, _, err := s.Conns.Require(r.Context(), web.UserOf(r), r.URL.Query().Get("connection"), conns.LevelRead)
	if err != nil {
		return err
	}
	if c.WorkspaceID != wsOf(r) {
		return apperr.NotFound("连接")
	}
	type method struct {
		ID        string   `json:"id"`
		Available bool     `json:"available"`
		Default   bool     `json:"default,omitempty"`
		Reason    string   `json:"reason,omitempty"`
		Warnings  []string `json:"warnings,omitempty"`
	}
	var out []method
	switch c.Driver {
	case "mysql":
		md := method{ID: MethodMysqldump, Available: true}
		if s.Tools == nil {
			md.Available, md.Reason = false, "mysqldump 不可用"
		} else if ok, why := s.Tools.Available(); !ok {
			md.Available, md.Reason = false, why
		}
		lm := method{ID: MethodLemi, Available: true}
		mariadb := false
		if sc, err := s.Conns.SQL(r.Context(), c); err == nil {
			if info, err := sc.Server(r.Context()); err == nil {
				mariadb = info.Product == "MariaDB"
				if s.Tools != nil && md.Available && (s.Tools.Custom == nil || s.Tools.Custom().Mysqldump == "") {
					platform, _ := dumptools.Platform()
					if _, warns, err := s.Tools.Manifest.Pick(info.Product, info.Version, platform); err != nil {
						md.Available, md.Reason = false, err.Error()
					} else {
						md.Warnings = warns
					}
				}
			}
		}
		// MariaDB 默认用自研导出器（docs/spec.md §13）；mysqldump 不可用时也是
		md.Default = md.Available && !mariadb
		lm.Default = !md.Default
		out = append(out, md, lm)
	case "sqlite":
		out = append(out, method{ID: MethodSQLiteFile, Available: true, Default: true}, method{ID: MethodLemi, Available: true})
	default:
		return apperr.Bad("该数据库类型不支持备份")
	}
	return web.OK(w, map[string]any{"methods": out})
}

// ---------------- 空间设置 ----------------

func (s *Service) hSettingsGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	return web.OK(w, s.GetWSSettings(r.Context(), wsOf(r)))
}

func (s *Service) hSettingsPut(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSAdmin); err != nil {
		return err
	}
	var in WSSettings
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.SetWSSettings(r.Context(), wsOf(r), in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.settings_update", WorkspaceID: wsOf(r)}, in)
	return web.OK(w, in)
}

// ---------------- 存储目标 ----------------

func (s *Service) wsTarget(srv *web.Server, r *http.Request, min string) (*Target, error) {
	if _, err := srv.RequireWS(r, wsOf(r), min); err != nil {
		return nil, err
	}
	t, err := s.GetTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, err
	}
	if t.WorkspaceID != wsOf(r) {
		return nil, apperr.NotFound("存储目标")
	}
	return t, nil
}

func requireKindAllowed(r *http.Request, kind string) error {
	if info, ok := target.Info(kind); ok && info.AdminOnly && !web.UserOf(r).IsAdmin() {
		return apperr.Forbidden("本地目录类存储目标只能由实例管理员配置")
	}
	return nil
}

func (s *Service) hTargetList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	role, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer)
	if err != nil {
		return err
	}
	list, err := s.ListTargets(r.Context(), wsOf(r))
	if err != nil {
		return err
	}
	usage, _ := s.TargetUsage(r.Context(), wsOf(r))
	full := accounts.RoleRank(role) >= accounts.RoleRank(accounts.WSAdmin)
	out := []map[string]any{}
	for i := range list {
		v := s.TargetView(&list[i], full)
		v["usage"] = usage[list[i].ID]
		out = append(out, v)
	}
	return web.OK(w, map[string]any{"targets": out})
}

func (s *Service) hTargetGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	role, _ := srv.RequireWS(r, wsOf(r), accounts.WSViewer)
	return web.OK(w, s.TargetView(t, accounts.RoleRank(role) >= accounts.RoleRank(accounts.WSAdmin)))
}

func (s *Service) hTargetCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSAdmin); err != nil {
		return err
	}
	var in TargetInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := requireKindAllowed(r, in.Kind); err != nil {
		return err
	}
	t, err := s.CreateTarget(r.Context(), wsOf(r), web.UserOf(r).ID, &in)
	if err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.target_create", WorkspaceID: t.WorkspaceID, Target: t.Name}, map[string]any{"id": t.ID, "kind": t.Kind})
	return web.OK(w, s.TargetView(t, true))
}

func (s *Service) hTargetUpdate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var in TargetInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = t.Kind
	}
	if err := requireKindAllowed(r, t.Kind); err != nil {
		return err
	}
	if err := s.UpdateTarget(r.Context(), t, &in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.target_update", WorkspaceID: t.WorkspaceID, Target: t.Name}, map[string]any{"id": t.ID, "kind": t.Kind})
	return web.OK(w, s.TargetView(t, true))
}

func (s *Service) hTargetDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	if err := requireKindAllowed(r, t.Kind); err != nil {
		return err
	}
	if err := s.DeleteTarget(r.Context(), t); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.target_delete", WorkspaceID: t.WorkspaceID, Target: t.Name}, map[string]any{"id": t.ID})
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hTargetTest(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	start := time.Now()
	if err := s.TestTarget(r.Context(), t); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"ok": true, "latency_ms": time.Since(start).Milliseconds()})
}

// hTargetTestNew 用还没保存的表单测试（带 id 时沿用已保存的密钥）。
func (s *Service) hTargetTestNew(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSAdmin); err != nil {
		return err
	}
	var in struct {
		TargetInput
		ID string `json:"id"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if in.Name == "" {
		in.Name = "测试"
	}
	var old *Target
	if in.ID != "" {
		t, err := s.GetTarget(r.Context(), in.ID)
		if err != nil || t.WorkspaceID != wsOf(r) {
			return apperr.NotFound("存储目标")
		}
		old = t
		in.Kind = t.Kind
	}
	if err := requireKindAllowed(r, in.Kind); err != nil {
		return err
	}
	id := ids.New()
	if old != nil {
		id = old.ID
	}
	cfg, err := s.prepareTarget(r.Context(), id, &in.TargetInput, old)
	if err != nil {
		return err
	}
	tmp := &Target{ID: id, WorkspaceID: wsOf(r), Name: in.Name, Kind: in.Kind, Config: cfg}
	start := time.Now()
	if err := s.TestTarget(r.Context(), tmp); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"ok": true, "latency_ms": time.Since(start).Milliseconds()})
}

func (s *Service) hTargetHostKey(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if !strings.HasPrefix(in.Fingerprint, "SHA256:") || len(in.Fingerprint) > 100 {
		return apperr.Bad("指纹格式应为 SHA256:...")
	}
	if err := s.SetHostKey(r.Context(), t, in.Fingerprint); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.target_host_key", WorkspaceID: t.WorkspaceID, Target: t.Name}, map[string]any{"fingerprint": in.Fingerprint})
	return web.OK(w, map[string]any{"ok": true})
}

// hTargetUsage 占用：Lemi 记录的汇总；actual=1 时实际列目录统计（可能较慢）。
func (s *Service) hTargetUsage(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	usage, err := s.TargetUsage(r.Context(), t.WorkspaceID)
	if err != nil {
		return err
	}
	out := map[string]any{"recorded": usage[t.ID]}
	if r.URL.Query().Get("actual") == "1" {
		tg, err := s.OpenTarget(r.Context(), t)
		if err != nil {
			return err
		}
		defer tg.Close()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		files, bytes, err := target.Usage(ctx, tg, t.WorkspaceID+"/")
		if err != nil {
			return targetErr(t.Name, err)
		}
		out["actual"] = map[string]int64{"files": int64(files), "bytes": bytes}
	}
	return web.OK(w, out)
}

func (s *Service) baiduApp(t *Target) (appKey, secret string, plain []byte, err error) {
	if t.Kind != target.KindBaidu {
		return "", "", nil, apperr.Bad("只有百度网盘需要扫码授权")
	}
	plain, err = s.plainConfig(t)
	if err != nil {
		return "", "", nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(plain, &m)
	appKey, _ = m["app_key"].(string)
	secret, _ = m["secret_key"].(string)
	if appKey == "" || secret == "" {
		return "", "", nil, apperr.Bad("请先填写百度网盘开放平台应用的 AppKey 与 SecretKey")
	}
	return appKey, secret, plain, nil
}

func (s *Service) hBaiduDevice(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	appKey, _, _, err := s.baiduApp(t)
	if err != nil {
		return err
	}
	d, err := target.BaiduDeviceCode(r.Context(), s.targetDeps(r.Context(), t).HTTPClient, appKey)
	if err != nil {
		return apperr.Wrap("baidu_failed", err)
	}
	return web.OK(w, d)
}

func (s *Service) hBaiduPoll(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	t, err := s.wsTarget(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	appKey, secret, plain, err := s.baiduApp(t)
	if err != nil {
		return err
	}
	deps := s.targetDeps(r.Context(), t)
	tok, err := target.BaiduPollToken(r.Context(), deps.HTTPClient, appKey, secret, in.DeviceCode)
	if errors.Is(err, target.ErrBaiduAuthPending) {
		return web.OK(w, map[string]any{"authorized": false, "pending": true, "slow_down": errors.Is(err, target.ErrBaiduSlowDown)})
	}
	if err != nil {
		return apperr.Wrap("baidu_failed", err)
	}
	newCfg, err := target.BaiduApplyToken(plain, tok)
	if err != nil {
		return err
	}
	if err := deps.SaveConfig(newCfg); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.target_authorize", WorkspaceID: t.WorkspaceID, Target: t.Name}, map[string]any{"id": t.ID})
	return web.OK(w, map[string]any{"authorized": true})
}

// ---------------- 计划 ----------------

func (s *Service) wsPlan(srv *web.Server, r *http.Request, min string) (*Plan, error) {
	if _, err := srv.RequireWS(r, wsOf(r), min); err != nil {
		return nil, err
	}
	p, err := s.GetPlan(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, err
	}
	if p.WorkspaceID != wsOf(r) {
		return nil, apperr.NotFound("备份计划")
	}
	return p, nil
}

// canSeeConn 用户能否看到某连接的备份（连接被个人收紧为 none 时看不到；连接已删除时只有空间管理员能看）。
func (s *Service) canSeeConn(ctx context.Context, r *http.Request, srv *web.Server, connID string) bool {
	u := web.UserOf(r)
	c, err := s.Conns.Get(ctx, connID)
	if err != nil {
		role := s.Accounts.EffectiveRole(ctx, wsOf(r), u)
		return accounts.RoleRank(role) >= accounts.RoleRank(accounts.WSAdmin)
	}
	return s.Conns.Access(ctx, u, c) != conns.LevelNone
}

func (s *Service) hPlanList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	list, err := s.ListPlans(r.Context(), wsOf(r), r.URL.Query().Get("connection"))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for i := range list {
		if s.canSeeConn(r.Context(), r, srv, list[i].ConnectionID) {
			v := s.PlanView(&list[i])
			v["busy"] = s.Busy(list[i].ConnectionID)
			out = append(out, v)
		}
	}
	return web.OK(w, map[string]any{"plans": out})
}

func (s *Service) hPlanGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	p, err := s.wsPlan(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	if !s.canSeeConn(r.Context(), r, srv, p.ConnectionID) {
		return apperr.NotFound("备份计划")
	}
	return web.OK(w, s.PlanView(p))
}

func (s *Service) hPlanCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSAdmin); err != nil {
		return err
	}
	var in PlanInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	p, err := s.CreatePlan(r.Context(), wsOf(r), web.UserOf(r).ID, &in)
	if err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.plan_create", WorkspaceID: p.WorkspaceID, ConnectionID: p.ConnectionID, Target: p.Name}, planAuditDetail(p))
	return web.OK(w, s.PlanView(p))
}

func planAuditDetail(p *Plan) map[string]any {
	return map[string]any{"id": p.ID, "method": p.Method, "databases": p.DatabaseList(), "targets": p.TargetIDs(), "schedule": json.RawMessage(p.Schedule),
		"retention": json.RawMessage(p.Retention), "encrypt": p.Encrypt, "compression": p.Compression, "enabled": p.Enabled}
}

func (s *Service) hPlanUpdate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	p, err := s.wsPlan(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var in PlanInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if in.ConnectionID == "" {
		in.ConnectionID = p.ConnectionID
	}
	if err := s.UpdatePlan(r.Context(), p, &in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.plan_update", WorkspaceID: p.WorkspaceID, ConnectionID: p.ConnectionID, Target: p.Name}, planAuditDetail(p))
	return web.OK(w, s.PlanView(p))
}

func (s *Service) hPlanDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	p, err := s.wsPlan(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	if err := s.DeletePlan(r.Context(), p); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.plan_delete", WorkspaceID: p.WorkspaceID, ConnectionID: p.ConnectionID, Target: p.Name}, map[string]any{"id": p.ID})
	return web.OK(w, map[string]any{"ok": true})
}

// hPlanRun 立即备份：空间成员及以上，且对连接有写权限。
func (s *Service) hPlanRun(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	p, err := s.wsPlan(srv, r, accounts.WSMember)
	if err != nil {
		return err
	}
	if _, _, err := s.Conns.Require(r.Context(), web.UserOf(r), p.ConnectionID, conns.LevelWrite); err != nil {
		return err
	}
	run, err := s.StartBackup(r.Context(), p, "manual", 0, web.UserOf(r).ID)
	if err != nil {
		return err
	}
	return web.OK(w, s.RunView(r.Context(), run, false))
}

// ---------------- 运行记录 ----------------

func (s *Service) wsRun(srv *web.Server, r *http.Request, min string) (*Run, error) {
	if _, err := srv.RequireWS(r, wsOf(r), min); err != nil {
		return nil, err
	}
	run, err := s.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, err
	}
	if run.WorkspaceID != wsOf(r) || !s.canSeeConn(r.Context(), r, srv, run.ConnectionID) {
		return nil, apperr.NotFound("备份记录")
	}
	return run, nil
}

func (s *Service) hRunList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	q := r.URL.Query()
	list, next, err := s.ListRuns(r.Context(), RunQuery{WorkspaceID: wsOf(r), PlanID: q.Get("plan"), ConnectionID: q.Get("connection"), Status: q.Get("status"),
		Cursor: q.Get("cursor"), Limit: web.QueryInt(r, "limit", 50)})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	for i := range list {
		cid := list[i].ConnectionID
		ok, cached := seen[cid]
		if !cached {
			ok = s.canSeeConn(r.Context(), r, srv, cid)
			seen[cid] = ok
		}
		if ok {
			out = append(out, s.RunView(r.Context(), &list[i], false))
		}
	}
	return web.OK(w, map[string]any{"runs": out, "next_cursor": next})
}

func (s *Service) hRunGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, err := s.wsRun(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	return web.OK(w, s.RunView(r.Context(), run, true))
}

func (s *Service) hRunDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, err := s.wsRun(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	if err := s.DeleteRun(r.Context(), run); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "backup.run_delete", WorkspaceID: run.WorkspaceID, ConnectionID: run.ConnectionID, Target: run.FileName}, map[string]any{"run_id": run.ID})
	return web.OK(w, map[string]any{"ok": true})
}

// requireConnWrite 发起人对连接有写权限（连接已删除时要求空间管理员）。
func (s *Service) requireConnWrite(srv *web.Server, r *http.Request, connID string) error {
	if _, err := s.Conns.Get(r.Context(), connID); err != nil {
		_, err := srv.RequireWS(r, wsOf(r), accounts.WSAdmin)
		return err
	}
	_, _, err := s.Conns.Require(r.Context(), web.UserOf(r), connID, conns.LevelWrite)
	return err
}

func (s *Service) hRunCancel(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, err := s.wsRun(srv, r, accounts.WSMember)
	if err != nil {
		return err
	}
	if err := s.requireConnWrite(srv, r, run.ConnectionID); err != nil {
		return err
	}
	if !s.Cancel(run.ID) {
		return apperr.Bad("这份备份没有在运行")
	}
	srv.AuditR(r, audit.Entry{Action: "backup.cancel", WorkspaceID: run.WorkspaceID, ConnectionID: run.ConnectionID, Target: run.FileName}, map[string]any{"run_id": run.ID})
	return web.OK(w, map[string]any{"ok": true})
}

// hRunDownload 下载：空间 viewer 及以上（对连接至少有读权限），走一次性下载令牌。
func (s *Service) hRunDownload(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, err := s.wsRun(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	var in struct {
		Format   string `json:"format"`
		TargetID string `json:"target_id"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if s.Downloads == nil {
		return apperr.Bad("下载服务不可用")
	}
	u := web.UserOf(r)
	job, err := s.DownloadJob(r.Context(), run, u.ID, in.Format, in.TargetID)
	if err != nil {
		return err
	}
	tok := s.Downloads.Add(job)
	srv.AuditR(r, audit.Entry{Action: "backup.download", WorkspaceID: run.WorkspaceID, ConnectionID: run.ConnectionID, Target: job.Filename},
		map[string]any{"run_id": run.ID, "format": in.Format, "target_id": in.TargetID})
	return web.OK(w, map[string]any{"url": "/api/v1/downloads/" + tok, "filename": job.Filename})
}

func (s *Service) hRunVerify(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, err := s.wsRun(srv, r, accounts.WSMember)
	if err != nil {
		return err
	}
	task, err := s.StartVerify(r.Context(), run, web.UserOf(r).ID)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"task_id": task})
}

func (s *Service) restoreTarget(srv *web.Server, r *http.Request, in *RestoreInput) (*Run, *conns.Connection, error) {
	run, err := s.wsRun(srv, r, accounts.WSMember)
	if err != nil {
		return nil, nil, err
	}
	if err := web.ReadJSON(r, in); err != nil {
		return nil, nil, err
	}
	if in.ConnectionID == "" {
		in.ConnectionID = run.ConnectionID
	}
	c, _, err := s.Conns.Require(r.Context(), web.UserOf(r), in.ConnectionID, conns.LevelWrite)
	if err != nil {
		return nil, nil, err
	}
	if c.WorkspaceID != wsOf(r) {
		return nil, nil, apperr.NotFound("连接")
	}
	return run, c, nil
}

func (s *Service) hRestorePrecheck(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var in RestoreInput
	run, c, err := s.restoreTarget(srv, r, &in)
	if err != nil {
		return err
	}
	items, _, err := s.Precheck(r.Context(), run, c, in.DatabaseMap)
	if err != nil {
		return err
	}
	method, merr := s.restoreMethod(run, in.Method)
	out := map[string]any{"items": items, "method": method}
	if merr != nil {
		out["method_error"] = merr.Error()
	}
	return web.OK(w, out)
}

func (s *Service) hRestore(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var in RestoreInput
	run, c, err := s.restoreTarget(srv, r, &in)
	if err != nil {
		return err
	}
	rec, err := s.StartRestore(r.Context(), run, c, &in, web.UserOf(r).ID)
	if err != nil {
		srv.AuditR(r, audit.Entry{Action: "backup.restore_rejected", WorkspaceID: run.WorkspaceID, ConnectionID: c.ID, Target: run.FileName, OK: false, Error: err.Error()},
			map[string]any{"run_id": run.ID})
		return err
	}
	return web.OK(w, rec.View())
}

func (s *Service) hRestoreList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	list, next, err := s.ListRestores(r.Context(), wsOf(r), r.URL.Query().Get("cursor"), web.QueryInt(r, "limit", 50))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for i := range list {
		if s.canSeeConn(r.Context(), r, srv, list[i].ConnectionID) {
			v := list[i].View()
			if p := s.progressOf(list[i].ID); p != nil {
				v["progress"] = p.snapshot()
			}
			out = append(out, v)
		}
	}
	return web.OK(w, map[string]any{"restores": out, "next_cursor": next})
}

func (s *Service) hRestoreGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSViewer); err != nil {
		return err
	}
	rec, err := s.GetRestore(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	if rec.WorkspaceID != wsOf(r) || !s.canSeeConn(r.Context(), r, srv, rec.ConnectionID) {
		return apperr.NotFound("还原记录")
	}
	v := rec.View()
	if p := s.progressOf(rec.ID); p != nil {
		v["progress"] = p.snapshot()
	}
	return web.OK(w, v)
}

// hTaskCancel 取消还原或校验任务（任务 ID 来自还原记录或校验接口）。
func (s *Service) hTaskCancel(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, wsOf(r), accounts.WSMember); err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	p := s.progressOf(id)
	if p == nil || p.WorkspaceID != wsOf(r) {
		return apperr.NotFound("任务")
	}
	if err := s.requireConnWrite(srv, r, p.ConnectionID); err != nil {
		return err
	}
	if !s.Cancel(id) {
		return apperr.Bad("任务没有在运行")
	}
	srv.AuditR(r, audit.Entry{Action: "backup.task_cancel", WorkspaceID: p.WorkspaceID, ConnectionID: p.ConnectionID}, map[string]any{"task_id": id, "kind": p.Kind})
	return web.OK(w, map[string]any{"ok": true})
}

// ---------------- mysqldump 工具（实例管理员） ----------------

const customToolsKey = "dumptools.custom"

// CustomTools 读管理员指定的客户端路径。
func (s *Service) CustomTools() dumptools.Custom {
	var c dumptools.Custom
	_, _ = s.Settings.GetJSON(context.Background(), customToolsKey, &c)
	return c
}

func requireInstanceAdmin(r *http.Request) error {
	if !web.UserOf(r).IsAdmin() {
		return apperr.Forbidden("需要实例管理员权限")
	}
	return nil
}

func (s *Service) hToolsStatus(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	if s.Tools == nil {
		return apperr.Bad("mysqldump 管理不可用")
	}
	platform, perr, list := s.Tools.Status()
	out := map[string]any{"platform": platform, "versions": list, "custom": s.CustomTools(), "base_url": s.Tools.BaseURL}
	if list == nil {
		out["versions"] = []dumptools.VersionStatus{}
	}
	if perr != nil {
		out["platform_error"] = perr.Error()
	}
	if c := s.CustomTools(); c.Mysqldump != "" {
		if t, err := dumptools.CustomTool(r.Context(), c); err != nil {
			out["custom_error"] = err.Error()
		} else {
			out["custom_version"] = t.Version
		}
	}
	return web.OK(w, out)
}

func (s *Service) hToolsInstall(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	if s.Tools == nil {
		return apperr.Bad("mysqldump 管理不可用")
	}
	v := s.Tools.Find(chi.URLParam(r, "version"))
	if v == nil {
		return apperr.NotFound("版本")
	}
	platform, err := dumptools.Platform()
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	t, err := s.Tools.Install(ctx, v, platform, nil)
	srv.AuditR(r, audit.Entry{Action: "dumptools.install", Target: v.Version, OK: err == nil, Error: errText(err)}, map[string]any{"platform": platform})
	if err != nil {
		return apperr.Wrap("install_failed", err)
	}
	return web.OK(w, t)
}

func (s *Service) hToolsRemove(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	if s.Tools == nil {
		return apperr.Bad("mysqldump 管理不可用")
	}
	v := chi.URLParam(r, "version")
	if s.Tools.Find(v) == nil {
		return apperr.NotFound("版本")
	}
	if err := s.Tools.Remove(v); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "dumptools.remove", Target: v}, nil)
	return web.OK(w, map[string]any{"ok": true})
}

// hToolsCustom 指定服务器上已安装的 mysqldump/mysql（会被执行，所以只限实例管理员并要求二次验证）。两项都为空表示取消。
func (s *Service) hToolsCustom(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireInstanceAdmin(r); err != nil {
		return err
	}
	if err := srv.RequireReauth(r); err != nil {
		return err
	}
	var in dumptools.Custom
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	in.Mysqldump, in.Mysql = strings.TrimSpace(in.Mysqldump), strings.TrimSpace(in.Mysql)
	var ver string
	if in.Mysqldump != "" || in.Mysql != "" {
		t, err := dumptools.CustomTool(r.Context(), in)
		if err != nil {
			return apperr.Bad("%s", err.Error())
		}
		ver = t.Version
	}
	if err := s.Settings.SetJSON(r.Context(), customToolsKey, in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "dumptools.custom"}, in)
	return web.OK(w, map[string]any{"custom": in, "version": ver})
}
