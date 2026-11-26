package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesWorkspaces(r chi.Router) {
	r.Get("/workspaces", s.H(s.handleWSList))
	r.Post("/workspaces", s.H(s.handleWSCreate))
	r.Get("/workspaces/{ws}", s.H(s.handleWSGet))
	r.Patch("/workspaces/{ws}", s.H(s.handleWSUpdate))
	r.Delete("/workspaces/{ws}", s.H(s.handleWSDelete))
	r.Get("/workspaces/{ws}/members", s.H(s.handleMembers))
	r.Put("/workspaces/{ws}/members/{user}", s.H(s.handleMemberSet))
	r.Delete("/workspaces/{ws}/members/{user}", s.H(s.handleMemberRemove))
	r.Get("/workspaces/{ws}/user-search", s.H(s.handleUserSearch))
	r.Get("/workspaces/{ws}/audit", s.H(s.handleWSAudit))
}

func (s *Server) handleWSList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Accounts.MyWorkspaces(r.Context(), UserOf(r))
	if err != nil {
		return err
	}
	if list == nil {
		list = []accounts.Workspace{}
	}
	return OK(w, map[string]any{"workspaces": list})
}

func (s *Server) handleWSCreate(w http.ResponseWriter, r *http.Request) error {
	u := UserOf(r)
	if !u.IsAdmin() && !s.Settings.System(r.Context()).UsersCanCreateWS {
		return apperr.Forbidden("只有实例管理员可以创建工作空间")
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	ws, err := s.Accounts.CreateWorkspace(r.Context(), req.Name, req.Description, u.ID)
	if err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "workspace.create", WorkspaceID: ws.ID, Target: ws.Name}, nil)
	return OK(w, ws)
}

func (s *Server) handleWSGet(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	role, err := s.RequireWS(r, id, accounts.WSViewer)
	if err != nil {
		return err
	}
	ws, err := s.Accounts.Workspace(r.Context(), id)
	if err != nil {
		return err
	}
	ws.MyRole = role
	return OK(w, ws)
}

func (s *Server) handleWSUpdate(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	if _, err := s.RequireWS(r, id, accounts.WSAdmin); err != nil {
		return err
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if err := s.Accounts.UpdateWorkspace(r.Context(), id, req.Name, req.Description); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "workspace.update", WorkspaceID: id, Target: req.Name}, nil)
	return s.handleWSGet(w, r)
}

func (s *Server) handleWSDelete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	if _, err := s.RequireWS(r, id, accounts.WSOwner); err != nil && !UserOf(r).IsAdmin() {
		return err
	}
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	ws, err := s.Accounts.Workspace(r.Context(), id)
	if err != nil {
		return err
	}
	var req struct {
		ConfirmName string `json:"confirm_name"`
	}
	_ = ReadJSON(r, &req)
	if strings.TrimSpace(req.ConfirmName) != ws.Name {
		return apperr.Bad("请输入工作空间名称确认删除")
	}
	if err := s.Accounts.DeleteWorkspace(r.Context(), id); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "workspace.delete", WorkspaceID: id, Target: ws.Name}, nil)
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	if _, err := s.RequireWS(r, id, accounts.WSViewer); err != nil {
		return err
	}
	list, err := s.Accounts.Members(r.Context(), id)
	if err != nil {
		return err
	}
	if list == nil {
		list = []accounts.Member{}
	}
	return OK(w, map[string]any{"members": list})
}

func (s *Server) handleMemberSet(w http.ResponseWriter, r *http.Request) error {
	id, uid := chi.URLParam(r, "ws"), chi.URLParam(r, "user")
	myRole, err := s.RequireWS(r, id, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if !accounts.ValidWSRole(req.Role) {
		return apperr.Bad("角色不正确")
	}
	cur := s.Accounts.RoleIn(r.Context(), id, uid)
	// 只有所有者能任免所有者；管理员不能改比自己高的人
	if (req.Role == accounts.WSOwner || cur == accounts.WSOwner) && myRole != accounts.WSOwner && !UserOf(r).IsAdmin() {
		return apperr.Forbidden("只有所有者可以任免所有者")
	}
	if _, err := s.Accounts.UserByID(r.Context(), uid); err != nil {
		return apperr.NotFound("用户")
	}
	if err := s.Accounts.SetMember(r.Context(), id, uid, req.Role); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "workspace.member_set", WorkspaceID: id, Target: uid}, map[string]any{"role": req.Role, "from": cur})
	return s.handleMembers(w, r)
}

func (s *Server) handleMemberRemove(w http.ResponseWriter, r *http.Request) error {
	id, uid := chi.URLParam(r, "ws"), chi.URLParam(r, "user")
	me := UserOf(r)
	if uid != me.ID { // 自己可以退出
		myRole, err := s.RequireWS(r, id, accounts.WSAdmin)
		if err != nil {
			return err
		}
		if s.Accounts.RoleIn(r.Context(), id, uid) == accounts.WSOwner && myRole != accounts.WSOwner && !me.IsAdmin() {
			return apperr.Forbidden("只有所有者可以移除所有者")
		}
	}
	if err := s.Accounts.RemoveMember(r.Context(), id, uid); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "workspace.member_remove", WorkspaceID: id, Target: uid}, nil)
	return OK(w, map[string]any{"ok": true})
}

// handleUserSearch 空间管理员按用户名或显示名找人加入空间，只返回最少的字段。
func (s *Server) handleUserSearch(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	if _, err := s.RequireWS(r, id, accounts.WSAdmin); err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		return OK(w, map[string]any{"users": []any{}})
	}
	type row struct {
		ID          string `db:"id" json:"id"`
		Username    string `db:"username" json:"username"`
		DisplayName string `db:"display_name" json:"display_name"`
	}
	var out []row
	p := strings.ToLower(q) + "%"
	err := s.Store.Select(r.Context(), &out, `SELECT id, username, display_name FROM users WHERE status = 'active' AND (username LIKE ? OR lower(display_name) LIKE ?) ORDER BY username LIMIT 20`, p, p)
	if err != nil {
		return err
	}
	if out == nil {
		out = []row{}
	}
	return OK(w, map[string]any{"users": out})
}

func (s *Server) handleWSAudit(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "ws")
	if _, err := s.RequireWS(r, id, accounts.WSAdmin); err != nil {
		return err
	}
	q := auditQuery(r)
	q.WorkspaceID = id
	list, err := s.Audit.List(r.Context(), q)
	if err != nil {
		return err
	}
	return OK(w, map[string]any{"entries": list})
}

// ---------- 邀请 ----------

type invite struct {
	ID            string `db:"id"`
	CreatedBy     string `db:"created_by"`
	Role          string `db:"role"`
	WorkspaceID   string `db:"workspace_id"`
	WorkspaceRole string `db:"workspace_role"`
	Note          string `db:"note"`
	CreatedAt     int64  `db:"created_at"`
	ExpiresAt     int64  `db:"expires_at"`
	UsedAt        int64  `db:"used_at"`
	UsedBy        string `db:"used_by"`
}

// createInvite 实例管理员生成一次性邀请链接（24 小时有效）。
func (s *Server) createInvite(r *http.Request, role, wsID, wsRole, note string) (string, error) {
	tok := ids.RandomToken(40)
	now := time.Now()
	inv := invite{ID: auth.HashToken(tok), CreatedBy: UserOf(r).ID, Role: role, WorkspaceID: wsID, WorkspaceRole: wsRole, Note: note,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(24 * time.Hour).UnixMilli()}
	if err := s.Store.Add(r.Context(), "invites", &inv); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Server) findInvite(r *http.Request, tok string) (*invite, error) {
	var inv invite
	if err := s.Store.GetW(r.Context(), &inv, `SELECT * FROM invites WHERE id = ?`, auth.HashToken(strings.TrimSpace(tok))); err != nil {
		return nil, apperr.New(404, "invite_invalid", "邀请链接无效或已过期")
	}
	if inv.UsedAt > 0 || inv.ExpiresAt < time.Now().UnixMilli() {
		return nil, apperr.New(404, "invite_invalid", "邀请链接无效或已过期")
	}
	return &inv, nil
}

func (s *Server) handleInviteCheck(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Token string `json:"token"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	inv, err := s.findInvite(r, req.Token)
	if err != nil {
		time.Sleep(300 * time.Millisecond)
		return err
	}
	out := map[string]any{"ok": true, "expires_at": inv.ExpiresAt}
	if inv.WorkspaceID != "" {
		if ws, err := s.Accounts.Workspace(r.Context(), inv.WorkspaceID); err == nil {
			out["workspace"] = ws.Name
		}
	}
	return OK(w, out)
}

func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Token       string `json:"token"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	inv, err := s.findInvite(r, req.Token)
	if err != nil {
		time.Sleep(300 * time.Millisecond)
		return err
	}
	if err := auth.CheckPasswordPolicy(req.Password, req.Username); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	ctx := r.Context()
	u := &accounts.User{Username: req.Username, DisplayName: strings.TrimSpace(req.DisplayName), Email: strings.TrimSpace(req.Email), Role: inv.Role, PasswordHash: hash}
	// 先占用邀请（防并发重复使用）
	res, err := s.Store.Exec(ctx, `UPDATE invites SET used_at = ? WHERE id = ? AND used_at = 0`, store.Now(), inv.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return apperr.New(404, "invite_invalid", "邀请链接无效或已过期")
	}
	if err := s.Accounts.CreateUser(ctx, u); err != nil {
		_, _ = s.Store.Exec(ctx, `UPDATE invites SET used_at = 0 WHERE id = ?`, inv.ID)
		return apperr.Bad("%s", err.Error())
	}
	_, _ = s.Store.Exec(ctx, `UPDATE invites SET used_by = ? WHERE id = ?`, u.ID, inv.ID)
	if inv.WorkspaceID != "" && accounts.ValidWSRole(inv.WorkspaceRole) {
		_ = s.Accounts.SetMember(ctx, inv.WorkspaceID, u.ID, inv.WorkspaceRole)
	}
	s.Audit.Record(audit.Entry{UserID: u.ID, Username: u.Username, Action: "user.invite_accepted", IP: ClientIP(r), WorkspaceID: inv.WorkspaceID, Source: audit.SrcUI}, nil)
	return OK(w, map[string]any{"ok": true})
}
