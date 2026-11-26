package tasks

import (
	"net/http"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载定时任务接口：查看要求空间成员，管理要求空间管理员（docs/spec.md §4.2）。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := srv.H
	r.Get("/workspaces/{ws}/agent-tasks", h(func(w http.ResponseWriter, r *http.Request) error {
		ws := chi.URLParam(r, "ws")
		if _, err := srv.RequireWS(r, ws, accounts.WSMember); err != nil {
			return err
		}
		list, err := s.List(r.Context(), ws)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(list))
		for i := range list {
			v := list[i].View()
			v["running"] = s.Running(list[i].ID)
			out = append(out, v)
		}
		return web.OK(w, map[string]any{"tasks": out})
	}))
	r.Post("/workspaces/{ws}/agent-tasks", h(func(w http.ResponseWriter, r *http.Request) error {
		ws := chi.URLParam(r, "ws")
		if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
			return err
		}
		var in Input
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		t, err := s.Create(r.Context(), ws, web.UserOf(r), in)
		if err != nil {
			return apperr.Bad("%s", err.Error())
		}
		srv.AuditR(r, audit.Entry{WorkspaceID: ws, Action: "task.create", Target: t.ID, OK: true}, map[string]any{"name": t.Name, "connections": t.Levels()})
		return web.OK(w, map[string]any{"task": t.View()})
	}))
	r.Route("/agent-tasks/{id}", func(r chi.Router) {
		r.Get("/", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			v := t.View()
			v["running"] = s.Running(t.ID)
			return web.OK(w, map[string]any{"task": v})
		}))
		r.Put("/", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSAdmin)
			if err != nil {
				return err
			}
			var in Input
			if err := web.ReadJSON(r, &in); err != nil {
				return err
			}
			if err := s.Update(r.Context(), t, web.UserOf(r), in); err != nil {
				return apperr.Bad("%s", err.Error())
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: t.WorkspaceID, Action: "task.update", Target: t.ID, OK: true}, map[string]any{"name": t.Name, "connections": t.Levels()})
			return web.OK(w, map[string]any{"task": t.View()})
		}))
		r.Delete("/", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSAdmin)
			if err != nil {
				return err
			}
			if err := s.Delete(r.Context(), t.ID); err != nil {
				return err
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: t.WorkspaceID, Action: "task.delete", Target: t.ID, OK: true}, map[string]any{"name": t.Name})
			return web.OK(w, map[string]any{"ok": true})
		}))
		r.Post("/run", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSAdmin)
			if err != nil {
				return err
			}
			id, err := s.Start(r.Context(), t, "manual")
			if err != nil {
				return apperr.Conflict(err.Error())
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: t.WorkspaceID, Action: "task.run_now", Target: t.ID, OK: true}, map[string]any{"run": id})
			return web.OK(w, map[string]any{"run_id": id})
		}))
		r.Post("/stop", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSAdmin)
			if err != nil {
				return err
			}
			return web.OK(w, map[string]any{"stopped": s.Stop(t.ID)})
		}))
		r.Get("/runs", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			list, err := s.Runs(r.Context(), t.ID, web.QueryInt(r, "limit", 50))
			if err != nil {
				return err
			}
			if list == nil {
				list = []RunRecord{}
			}
			return web.OK(w, map[string]any{"runs": list})
		}))
		r.Get("/runs/{run}/transcript", h(func(w http.ResponseWriter, r *http.Request) error {
			t, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			var rec RunRecord
			if err := s.st.Get(r.Context(), &rec, `SELECT * FROM agent_task_runs WHERE id = ? AND task_id = ?`, chi.URLParam(r, "run"), t.ID); err != nil || rec.ConversationID == "" {
				return apperr.NotFound("运行记录")
			}
			out, err := s.agent.Transcript(r.Context(), rec.ConversationID)
			if err != nil {
				return apperr.NotFound("运行记录")
			}
			out["run"] = rec
			return web.OK(w, out)
		}))
	})
}

func (s *Service) access(srv *web.Server, r *http.Request, min string) (*Task, error) {
	t, err := s.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, apperr.NotFound("任务")
	}
	if _, err := srv.RequireWS(r, t.WorkspaceID, min); err != nil {
		return nil, err
	}
	return t, nil
}
