package health

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载健康检查接口（/api/v1 下，已登录）。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := func(fn func(srv *web.Server, w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return srv.H(func(w http.ResponseWriter, r *http.Request) error { return fn(srv, w, r) })
	}
	r.Get("/workspaces/{ws}/health", h(s.hList))
	r.Get("/workspaces/{ws}/health/{cid}/config", h(s.hConfigGet))
	r.Put("/workspaces/{ws}/health/{cid}/config", h(s.hConfigPut))
	r.Get("/workspaces/{ws}/health/{cid}/timeline", h(s.hTimeline))
	r.Get("/workspaces/{ws}/health/{cid}/checks", h(s.hChecks))
	r.Post("/workspaces/{ws}/health/{cid}/check", h(s.hCheck))
}

// conn 要求空间角色至少 minRole、对连接至少可读，并确认连接属于 {ws}。
func (s *Service) conn(srv *web.Server, r *http.Request, minRole string) (*conns.Connection, error) {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, minRole); err != nil {
		return nil, err
	}
	c, _, err := s.conns.Require(r.Context(), web.UserOf(r), chi.URLParam(r, "cid"), conns.LevelRead)
	if err != nil {
		return nil, err
	}
	if c.WorkspaceID != ws {
		return nil, apperr.NotFound("连接")
	}
	return c, nil
}

// Item 是连接列表里一个连接的健康状态。
type Item struct {
	State
	Name        string `json:"name"`
	Driver      string `json:"driver"`
	Env         string `json:"env"`
	Enabled     bool   `json:"enabled"`
	IntervalSec int    `json:"interval_sec"`
}

// List 空间里用户能看到的全部连接的当前状态。
func (s *Service) List(ctx context.Context, wsID string, u *accounts.User) ([]Item, error) {
	list, err := s.conns.List(ctx, wsID)
	if err != nil {
		return nil, err
	}
	var rows []configRow
	if err := s.st.Select(ctx, &rows, `SELECT h.* FROM health_configs h JOIN connections c ON c.id = h.connection_id WHERE c.workspace_id = ?`, wsID); err != nil {
		return nil, err
	}
	cfgs := map[string]Config{}
	for _, r := range rows {
		cfgs[r.ConnectionID] = r.config()
	}
	var states []State
	if err := s.st.Select(ctx, &states, `SELECT h.* FROM health_state h JOIN connections c ON c.id = h.connection_id WHERE c.workspace_id = ?`, wsID); err != nil {
		return nil, err
	}
	st := map[string]State{}
	for _, x := range states {
		st[x.ConnectionID] = x
	}
	out := []Item{}
	for i := range list {
		c := &list[i]
		if s.conns.Access(ctx, u, c) == conns.LevelNone {
			continue
		}
		cfg, ok := cfgs[c.ID]
		if !ok {
			cfg = DefaultConfig()
		}
		it := Item{State: State{ConnectionID: c.ID, Status: StatusUnknown}, Name: c.Name, Driver: c.Driver, Env: c.Env,
			Enabled: cfg.Enabled, IntervalSec: cfg.IntervalSec}
		if x, ok := st[c.ID]; ok {
			it.State = x
		}
		if !cfg.Enabled {
			it.Status = StatusDisabled
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Service) hList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	items, err := s.List(r.Context(), ws, web.UserOf(r))
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"connections": items})
}

func (s *Service) configView(ctx context.Context, c *conns.Connection) (map[string]any, error) {
	cfg, custom, err := s.Config(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	if s.notify != nil {
		cfg.Channels = s.notify.FilterChannels(ctx, c.WorkspaceID, cfg.Channels)
	}
	return map[string]any{"connection_id": c.ID, "custom": custom, "config": cfg, "defaults": DefaultConfig()}, nil
}

func (s *Service) hConfigGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.conn(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	v, err := s.configView(r.Context(), c)
	if err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) hConfigPut(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.conn(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var cfg Config
	if err := web.ReadJSON(r, &cfg); err != nil {
		return err
	}
	before, _, _ := s.Config(r.Context(), c.ID)
	if err := s.SetConfig(r.Context(), c, cfg, web.UserOf(r).ID); err != nil {
		return err
	}
	after, _, _ := s.Config(r.Context(), c.ID)
	srv.AuditR(r, audit.Entry{Action: "health.config_update", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: c.Name},
		map[string]any{"before": before, "after": after})
	v, err := s.configView(r.Context(), c)
	if err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) hTimeline(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.conn(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	var loc *time.Location
	if tz := r.URL.Query().Get("tz"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil || len(tz) > 64 {
			return apperr.Bad("时区不正确")
		}
		loc = l
	}
	tl, err := s.Timeline(r.Context(), c.ID, r.URL.Query().Get("range"), loc)
	if err != nil {
		return err
	}
	return web.OK(w, tl)
}

func (s *Service) hChecks(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.conn(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	cursor, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	list, next, err := s.Checks(r.Context(), c.ID, cursor, web.QueryInt(r, "limit", 100), q.Get("failed") == "1" || q.Get("failed") == "true")
	if err != nil {
		return err
	}
	nc := ""
	if next > 0 {
		nc = strconv.FormatInt(next, 10)
	}
	return web.OK(w, map[string]any{"checks": list, "next_cursor": nc})
}

func (s *Service) hCheck(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, err := s.conn(srv, r, accounts.WSMember)
	if err != nil {
		return err
	}
	res, err := s.CheckNow(r.Context(), c.ID)
	if err != nil {
		return err
	}
	return web.OK(w, res)
}
