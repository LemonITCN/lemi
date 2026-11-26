package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载智能体、模型供应商、图表、结果接口（docs/spec.md §24）。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := srv.H
	wrap := func(fn func(srv *web.Server, w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return h(func(w http.ResponseWriter, r *http.Request) error { return fn(srv, w, r) })
	}
	// 模型
	r.Get("/workspaces/{ws}/ai/models", wrap(s.hModels))
	r.Get("/workspaces/{ws}/ai/settings", wrap(s.hWSSettingsGet))
	r.Put("/workspaces/{ws}/ai/settings", wrap(s.hWSSettingsPut))
	r.Get("/me/ai", wrap(s.hMeGet))
	r.Put("/me/ai", wrap(s.hMePut))
	// 会话
	r.Get("/workspaces/{ws}/agent/conversations", wrap(s.hList))
	r.Post("/workspaces/{ws}/agent/conversations", wrap(s.hCreate))
	r.Get("/agent/conversations/{id}", wrap(s.hGet))
	r.Patch("/agent/conversations/{id}", wrap(s.hPatch))
	r.Delete("/agent/conversations/{id}", wrap(s.hDelete))
	r.Post("/agent/conversations/{id}/messages", wrap(s.hSend))
	r.Get("/agent/runs/{id}", wrap(s.hRun))
	r.Get("/agent/runs/{id}/events", s.hEvents)
	r.Post("/agent/runs/{id}/stop", wrap(s.hStop))
	r.Post("/agent/runs/{id}/respond", wrap(s.hRespond))
	// 结果与图表
	r.Get("/agent/results/{id}", wrap(s.hResult))
	r.Get("/charts/{id}", wrap(s.hChart))
	r.Post("/charts/{id}/refresh", wrap(s.hChartRefresh))
	// 实例管理员：供应商
	r.Get("/admin/ai", wrap(s.hAdminGet))
	r.Post("/admin/ai/providers", wrap(s.hAdminCreate))
	r.Put("/admin/ai/providers/{id}", wrap(s.hAdminUpdate))
	r.Delete("/admin/ai/providers/{id}", wrap(s.hAdminDelete))
	r.Get("/admin/ai/providers/{id}/models", wrap(s.hAdminModels))
	r.Post("/admin/ai/providers/test", wrap(s.hAdminTest))
	r.Put("/admin/ai/settings", wrap(s.hAdminSettings))
}

// Bootstrap 给 /api/v1/bootstrap 的附加字段。
func (s *Service) Bootstrap(ctx context.Context, u *accounts.User, out map[string]any) {
	out["ai"] = map[string]any{"configured": s.Configured(ctx)}
}

func uid(r *http.Request) string { return web.UserOf(r).ID }

func (s *Service) hModels(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ms, def, errs := s.WorkspaceModels(ctx, ws)
	if r.URL.Query().Get("all") == "1" {
		// 空间设置页：列出全部可用模型（不按空间启用列表过滤）
		ms, errs = s.AllModels(ctx)
	}
	if ms == nil {
		ms = []ModelInfo{}
	}
	return web.OK(w, map[string]any{"configured": s.Configured(ctx), "models": ms, "default": def, "errors": errs})
}

func (s *Service) hWSSettingsGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	v := s.wsAI(r.Context(), ws)
	if v.Models == nil {
		v.Models = []string{}
	}
	return web.OK(w, v)
}

func (s *Service) hWSSettingsPut(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var in wsAISettings
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if len([]rune(in.Instructions)) > 8000 {
		return apperr.Bad("空间约定不超过 8000 字")
	}
	if len(in.Models) > 200 {
		return apperr.Bad("启用的模型太多")
	}
	if err := s.set.SetJSON(r.Context(), "ws:"+ws+":ai", in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{WorkspaceID: ws, Action: "ai.workspace_settings", OK: true}, map[string]any{"models": len(in.Models), "default": in.DefaultModel})
	return web.OK(w, in)
}

func (s *Service) hMeGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Instructions string `json:"instructions"`
	}
	_, _ = s.set.GetJSON(r.Context(), "user:"+uid(r)+":ai", &v)
	return web.OK(w, v)
}

func (s *Service) hMePut(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Instructions string `json:"instructions"`
	}
	if err := web.ReadJSON(r, &v); err != nil {
		return err
	}
	if len([]rune(v.Instructions)) > 8000 {
		return apperr.Bad("个人指令不超过 8000 字")
	}
	if err := s.set.SetJSON(r.Context(), "user:"+uid(r)+":ai", v); err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) convView(c *Conversation) map[string]any {
	out := map[string]any{"id": c.ID, "workspace_id": c.WorkspaceID, "kind": c.Kind, "title": c.Title, "settings": c.Cfg, "usage": c.Usage,
		"created_at": c.CreatedAt, "updated_at": c.UpdatedAt, "archived_at": c.ArchivedAt, "task_id": c.TaskID, "parent_id": c.ParentID}
	if r := s.ActiveRun(c.ID); r != nil {
		out["running"] = r.State() == RunRunning || r.State() == RunWaiting
		out["run_id"] = r.ID
	}
	return out
}

func (s *Service) hList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSMember); err != nil {
		return err
	}
	kind := r.URL.Query().Get("kind")
	if kind != KindTask {
		kind = KindChat
	}
	q := `SELECT * FROM agent_conversations WHERE user_id = ? AND workspace_id = ? AND kind = ? AND archived_at = 0 ORDER BY updated_at DESC LIMIT ?`
	if r.URL.Query().Get("archived") == "1" {
		q = `SELECT * FROM agent_conversations WHERE user_id = ? AND workspace_id = ? AND kind = ? AND archived_at > 0 ORDER BY updated_at DESC LIMIT ?`
	}
	limit := web.QueryInt(r, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []convRow
	if err := s.st.Select(r.Context(), &rows, q, uid(r), ws, kind, limit); err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, s.convView(decodeConv(&rows[i])))
	}
	return web.OK(w, map[string]any{"conversations": out})
}

func (s *Service) hCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSMember); err != nil {
		return err
	}
	var in struct {
		Settings ConvSettings `json:"settings"`
	}
	if r.ContentLength != 0 {
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
	}
	c, err := s.CreateConversation(r.Context(), ws, web.UserOf(r), in.Settings)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"conversation": s.convView(c)})
}

// Transcript 会话的完整过程（定时任务运行记录用，调用方负责权限）。
func (s *Service) Transcript(ctx context.Context, convID string) (map[string]any, error) {
	c, err := s.getConv(ctx, convID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.loadMessages(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversation": s.convView(c), "messages": viewMessages(msgs), "todos": c.St.Todos}, nil
}

// viewMessages 给界面的消息：隐藏片段去掉、思考签名去掉。
func viewMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		var kept []StoredPart
		for _, p := range m.Parts {
			if p.Hidden {
				continue
			}
			p.Signature = ""
			kept = append(kept, p)
		}
		if len(kept) == 0 {
			continue
		}
		m.Parts = kept
		out = append(out, m)
	}
	return out
}

func (s *Service) hGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.userConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("会话")
	}
	msgs, err := s.loadMessages(r.Context(), c.ID)
	if err != nil {
		return err
	}
	out := map[string]any{"conversation": s.convView(c), "messages": viewMessages(msgs), "todos": c.St.Todos, "summary": c.St.Summary,
		"summary_upto": c.St.SummaryUpTo, "context": c.St.LastContext}
	if c.St.Todos == nil {
		out["todos"] = []any{}
	}
	if run := s.ActiveRun(c.ID); run != nil {
		_, st := run.Since(0)
		from, pend := run.Resume()
		out["active_run"] = map[string]any{"id": run.ID, "state": st, "resume_from": from, "pending": pend}
	}
	return web.OK(w, out)
}

func (s *Service) hPatch(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.userConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("会话")
	}
	var in struct {
		Title    *string       `json:"title"`
		Archived *bool         `json:"archived"`
		Settings *ConvSettings `json:"settings"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if in.Title != nil {
		c.Title = truncateRunes(strings.TrimSpace(*in.Title), 100)
	}
	if in.Archived != nil {
		c.ArchivedAt = 0
		if *in.Archived {
			c.ArchivedAt = store.Now()
		}
	}
	if in.Settings != nil {
		applyCfg(c, SendInput{Model: in.Settings.Model, Effort: in.Settings.Effort, Mode: in.Settings.Mode})
		if in.Settings.Effort == "" {
			c.Cfg.Effort = ""
		}
	}
	if err := s.saveMeta(r.Context(), c); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"conversation": s.convView(c)})
}

func (s *Service) hDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.userConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("会话")
	}
	if run := s.ActiveRun(c.ID); run != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-time.After(ForceStopAfter + 2*time.Second):
		}
	}
	// 子智能体会话一起删
	if _, err := s.st.Exec(r.Context(), `DELETE FROM agent_conversations WHERE parent_id = ? AND user_id = ?`, c.ID, c.UserID); err != nil {
		return err
	}
	if _, err := s.st.Exec(r.Context(), `DELETE FROM agent_conversations WHERE id = ?`, c.ID); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hSend(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var in SendInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	res, err := s.Send(r.Context(), web.UserOf(r), chi.URLParam(r, "id"), web.ConnID(r), in)
	if err != nil {
		if err == ErrNoConv {
			return apperr.NotFound("会话")
		}
		return apperr.Wrap("send_failed", err)
	}
	return web.OK(w, res)
}

func (s *Service) hRun(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	run, ok := s.GetRun(uid(r), chi.URLParam(r, "id"))
	if !ok {
		var row runRow
		if err := s.st.Get(r.Context(), &row, `SELECT * FROM agent_runs WHERE id = ? AND user_id = ?`, chi.URLParam(r, "id"), uid(r)); err != nil {
			return apperr.NotFound("运行")
		}
		return web.OK(w, map[string]any{"id": row.ID, "conversation_id": row.ConversationID, "state": row.Status, "error": row.Error, "steps": row.Steps,
			"started_at": row.StartedAt, "finished_at": row.FinishedAt})
	}
	evs, st := run.Since(0)
	from, pend := run.Resume()
	return web.OK(w, map[string]any{"id": run.ID, "conversation_id": run.ConvID, "state": st, "last_seq": len(evs), "resume_from": from, "pending": pend})
}

// hEvents 以 SSE 推送运行事件；after 指定从哪个序号之后开始（断线续传，也认 Last-Event-ID）。
func (s *Service) hEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.GetRun(uid(r), chi.URLParam(r, "id"))
	if !ok {
		web.WriteJSON(w, 404, map[string]any{"error": apperr.NotFound("运行")})
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if v, err := strconv.Atoi(le); err == nil {
			after = v
		}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		web.WriteJSON(w, 500, map[string]any{"error": apperr.New(500, "internal", "不支持流式响应")})
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	notify, unsub := run.Subscribe()
	defer unsub()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		evs, state := run.Since(after)
		for _, ev := range evs {
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, b)
			after = ev.Seq
		}
		if len(evs) > 0 {
			fl.Flush()
		}
		if state == RunDone || state == RunError || state == RunStopped {
			if len(evs) == 0 {
				return
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-notify:
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Service) hStop(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := s.Stop(uid(r), chi.URLParam(r, "id")); err != nil {
		return apperr.NotFound("运行")
	}
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hRespond(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ID string `json:"id"`
		Reply
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.Respond(uid(r), chi.URLParam(r, "id"), in.ID, in.Reply); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hResult(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	m, err := s.Tools.Results.Get(r.Context(), chi.URLParam(r, "id"), uid(r), "")
	if err != nil {
		return apperr.NotFound("结果")
	}
	if _, err := srv.RequireWS(r, m.WorkspaceID, accounts.WSViewer); err != nil {
		return err
	}
	d, err := s.Tools.Results.Load(m)
	if err != nil {
		return apperr.NotFound("结果")
	}
	if m.Kind == "text" {
		text, total := results.TextLines(d.Text, web.QueryInt(r, "offset", 0), web.QueryInt(r, "limit", 2000))
		return web.OK(w, map[string]any{"result": m, "text": text, "total": total})
	}
	limit := web.QueryInt(r, "limit", 500)
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	cols, rows, total, err := results.Apply(d, results.Query{Offset: web.QueryInt(r, "offset", 0), Limit: limit})
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	if rows == nil {
		rows = [][]any{}
	}
	return web.OK(w, map[string]any{"result": m, "columns": cols, "rows": rows, "total": total})
}

func (s *Service) chartAccess(srv *web.Server, r *http.Request, min string) (*charts.Chart, error) {
	c, err := s.Tools.Charts.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, apperr.NotFound("图表")
	}
	if _, err := srv.RequireWS(r, c.WorkspaceID, min); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) hChart(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.chartAccess(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"chart": s.Tools.Charts.View(c)})
}

func (s *Service) hChartRefresh(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.chartAccess(srv, r, accounts.WSMember)
	if err != nil {
		return err
	}
	if err := s.Tools.Charts.Refresh(r.Context(), web.UserOf(r), c); err != nil {
		return apperr.Wrap("refresh_failed", err)
	}
	return web.OK(w, map[string]any{"chart": s.Tools.Charts.View(c)})
}

// ---------- 实例管理员 ----------

func requireAdmin(r *http.Request) error {
	if u := web.UserOf(r); u == nil || !u.IsAdmin() {
		return apperr.Forbidden("需要实例管理员权限")
	}
	return nil
}

func (s *Service) hAdminGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	rows, err := s.Providers(r.Context())
	if err != nil {
		return err
	}
	list := make([]map[string]any, 0, len(rows))
	for i := range rows {
		list = append(list, rows[i].View())
	}
	return web.OK(w, map[string]any{"providers": list, "default_model": s.instanceAI(r.Context()).DefaultModel,
		"protocols": []string{llm.APIChat, llm.APIMessages, llm.APIResponses}})
}

func (s *Service) hAdminCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var in ProviderInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	p, err := s.SaveProvider(r.Context(), "", in)
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	srv.AuditR(r, audit.Entry{Action: "ai.provider_create", Target: p.ID, OK: true}, map[string]any{"kind": p.Kind, "name": p.Name})
	return web.OK(w, map[string]any{"provider": p.View()})
}

func (s *Service) hAdminUpdate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var in ProviderInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	old, err := s.providerRow(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("供应商")
	}
	in.Kind = old.Kind
	p, err := s.SaveProvider(r.Context(), old.ID, in)
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	srv.AuditR(r, audit.Entry{Action: "ai.provider_update", Target: p.ID, OK: true}, map[string]any{"kind": p.Kind, "name": p.Name, "key_changed": in.APIKey != ""})
	return web.OK(w, map[string]any{"provider": p.View()})
}

func (s *Service) hAdminDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if err := s.DeleteProvider(r.Context(), id); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "ai.provider_delete", Target: id, OK: true}, nil)
	return web.OK(w, map[string]any{"ok": true})
}

func (s *Service) hAdminModels(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	p, err := s.providerRow(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("供应商")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	prov, err := s.provider(ctx, p)
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	var ms []llm.Model
	if c, ok := prov.(*llm.Custom); ok && r.URL.Query().Get("fetch") == "1" {
		ms, err = c.FetchModels(ctx)
	} else {
		ms, err = prov.Models(ctx)
	}
	if err != nil {
		return apperr.Bad("获取模型列表失败：%s", fmtErr(err))
	}
	return web.OK(w, map[string]any{"models": ms})
}

// hAdminTest 不保存，测试一份供应商配置：拉模型列表，可选再发一句话。
func (s *Service) hAdminTest(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var in struct {
		ProviderInput
		ProviderID string `json:"provider_id"` // 修改时不重输密钥：用已保存的
		Model      string `json:"model"`       // 可选：发一句话测试
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	key := in.APIKey
	if key == "" && in.ProviderID != "" {
		if p, err := s.providerRow(r.Context(), in.ProviderID); err == nil {
			key, _ = s.box.Open(p.APIKey, aadProvider(p.ID))
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var prov llm.Provider
	switch in.Kind {
	case ProviderOpenCode:
		prov = &llm.OpenCodeGo{APIKey: key, ModelsURL: in.BaseURL}
	case ProviderCustom:
		if !llm.ValidAPI(in.Protocol) {
			return apperr.Bad("协议应为 chat-completions、messages 或 responses")
		}
		base, err := s.checkCustomURL(ctx, in.BaseURL)
		if err != nil {
			return apperr.Bad("%s", err.Error())
		}
		prov = &llm.Custom{BaseURL: base, APIKey: key, Protocol: in.Protocol, Static: in.Models, AutoModels: in.AutoModels, Client: s.egressClient(ctx)}
	default:
		return apperr.Bad("未知的供应商种类")
	}
	ms, err := prov.Models(ctx)
	if err != nil {
		return web.OK(w, map[string]any{"ok": false, "error": fmtErr(err)})
	}
	out := map[string]any{"ok": true, "models": ms}
	if in.Model != "" {
		text, _, err := s.collectText(ctx, prov, llm.Request{Model: in.Model, MaxTokens: 50, SessionID: "lemi-test-" + uid(r),
			Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: "回复两个字：你好"}}}}})
		if err != nil {
			out["ok"], out["error"] = false, fmtErr(err)
		} else {
			out["reply"] = truncateRunes(text, 200)
		}
	}
	return web.OK(w, out)
}

func (s *Service) hAdminSettings(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var in aiSettings
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.set.SetJSON(r.Context(), "ai", in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "ai.settings", OK: true}, in)
	return web.OK(w, in)
}
