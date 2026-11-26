package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/jmoiron/sqlx"
)

// convRow 是 agent_conversations 的一行。
type convRow struct {
	ID           string `db:"id"`
	WorkspaceID  string `db:"workspace_id"`
	UserID       string `db:"user_id"`
	Kind         string `db:"kind"`
	TaskID       string `db:"task_id"`
	ParentID     string `db:"parent_id"`
	Title        string `db:"title"`
	Settings     string `db:"settings"`
	State        string `db:"state"`
	Usage        string `db:"usage"`
	SystemPrompt string `db:"system_prompt"`
	CreatedAt    int64  `db:"created_at"`
	UpdatedAt    int64  `db:"updated_at"`
	ArchivedAt   int64  `db:"archived_at"`
}

// Conversation 是会话及其解析后的设置。
type Conversation struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	UserID      string       `json:"user_id"`
	Kind        string       `json:"kind"`
	TaskID      string       `json:"task_id,omitempty"`
	ParentID    string       `json:"parent_id,omitempty"`
	Title       string       `json:"title"`
	Cfg         ConvSettings `json:"settings"`
	St          ConvState    `json:"-"`
	Usage       ConvUsage    `json:"usage"`
	System      string       `json:"-"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	ArchivedAt  int64        `json:"archived_at,omitempty"`
}

func decodeConv(r *convRow) *Conversation {
	c := &Conversation{ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID, Kind: r.Kind, TaskID: r.TaskID, ParentID: r.ParentID, Title: r.Title,
		System: r.SystemPrompt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt}
	_ = json.Unmarshal([]byte(r.Settings), &c.Cfg)
	_ = json.Unmarshal([]byte(r.State), &c.St)
	_ = json.Unmarshal([]byte(r.Usage), &c.Usage)
	if c.Cfg.Mode == "" {
		c.Cfg.Mode = ModeNormal
	}
	return c
}

func (c *Conversation) row() *convRow {
	sb, _ := json.Marshal(c.Cfg)
	st, _ := json.Marshal(c.St)
	us, _ := json.Marshal(c.Usage)
	return &convRow{ID: c.ID, WorkspaceID: c.WorkspaceID, UserID: c.UserID, Kind: c.Kind, TaskID: c.TaskID, ParentID: c.ParentID, Title: c.Title,
		Settings: string(sb), State: string(st), Usage: string(us), SystemPrompt: c.System, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, ArchivedAt: c.ArchivedAt}
}

// Message 是解析后的消息。
type Message struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	Role      string          `json:"role"` // user | assistant | tool
	Parts     []StoredPart    `json:"parts"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	CreatedAt int64           `json:"created_at"`
}

type msgRow struct {
	ID             string `db:"id"`
	ConversationID string `db:"conversation_id"`
	Seq            int64  `db:"seq"`
	Role           string `db:"role"`
	Parts          string `db:"parts"`
	Meta           string `db:"meta"`
	CreatedAt      int64  `db:"created_at"`
}

// ErrNoConv 会话不存在。
var ErrNoConv = errors.New("会话不存在")

func (s *Service) getConv(ctx context.Context, id string) (*Conversation, error) {
	var r convRow
	if err := s.st.GetW(ctx, &r, `SELECT * FROM agent_conversations WHERE id = ?`, id); err != nil {
		return nil, ErrNoConv
	}
	return decodeConv(&r), nil
}

// userConv 取用户自己的会话。
func (s *Service) userConv(ctx context.Context, userID, id string) (*Conversation, error) {
	c, err := s.getConv(ctx, id)
	if err != nil || c.UserID != userID {
		return nil, ErrNoConv
	}
	return c, nil
}

// saveState 保存会话的上下文状态与用量（不动标题与设置，避免覆盖用户在运行中的修改）。
func (s *Service) saveState(ctx context.Context, c *Conversation) error {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	r := c.row()
	c.UpdatedAt = store.Now()
	_, err := s.st.Exec(ctx, `UPDATE agent_conversations SET state = ?, usage = ?, system_prompt = ?, updated_at = ? WHERE id = ?`, r.State, r.Usage, r.SystemPrompt, c.UpdatedAt, c.ID)
	return err
}

// saveMeta 保存标题、设置与归档状态。
func (s *Service) saveMeta(ctx context.Context, c *Conversation) error {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	r := c.row()
	c.UpdatedAt = store.Now()
	_, err := s.st.Exec(ctx, `UPDATE agent_conversations SET title = ?, settings = ?, archived_at = ?, updated_at = ? WHERE id = ?`, r.Title, r.Settings, r.ArchivedAt, c.UpdatedAt, c.ID)
	return err
}

func (s *Service) createConv(ctx context.Context, c *Conversation) error {
	now := store.Now()
	if c.ID == "" {
		c.ID = ids.New()
	}
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Cfg.Mode == "" {
		c.Cfg.Mode = ModeNormal
	}
	if c.Kind == "" {
		c.Kind = KindChat
	}
	return s.st.Add(ctx, "agent_conversations", c.row())
}

func (s *Service) loadMessages(ctx context.Context, convID string) ([]Message, error) {
	var rows []msgRow
	if err := s.st.Select(ctx, &rows, `SELECT * FROM agent_messages WHERE conversation_id = ? ORDER BY seq`, convID); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		m := Message{ID: r.ID, Seq: r.Seq, Role: r.Role, CreatedAt: r.CreatedAt}
		_ = json.Unmarshal([]byte(r.Parts), &m.Parts)
		if r.Meta != "" && r.Meta != "{}" && r.Meta != "null" {
			m.Meta = json.RawMessage(r.Meta)
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) appendMessage(ctx context.Context, convID, role string, parts []StoredPart, meta any) (*Message, error) {
	pb, _ := json.Marshal(parts)
	mb := []byte("{}")
	if meta != nil {
		mb, _ = json.Marshal(meta)
	}
	var msg *Message
	err := s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		var seq int64
		if err := tx.GetContext(ctx, &seq, `SELECT COALESCE(MAX(seq), 0) FROM agent_messages WHERE conversation_id = ?`, convID); err != nil {
			return err
		}
		row := msgRow{ID: ids.New(), ConversationID: convID, Seq: seq + 1, Role: role, Parts: string(pb), Meta: string(mb), CreatedAt: store.Now()}
		msg = &Message{ID: row.ID, Seq: row.Seq, Role: role, Parts: parts, Meta: mb, CreatedAt: row.CreatedAt}
		return store.Insert(ctx, tx, "agent_messages", &row)
	})
	return msg, err
}

// runRow 是 agent_runs 的一行。
type runRow struct {
	ID             string `db:"id"`
	ConversationID string `db:"conversation_id"`
	UserID         string `db:"user_id"`
	Status         string `db:"status"`
	Error          string `db:"error"`
	Steps          int    `db:"steps"`
	StartedAt      int64  `db:"started_at"`
	FinishedAt     int64  `db:"finished_at"`
}

// autoTitle 第一条消息的前 30 个字作为临时标题。
func autoTitle(text string) string {
	t := strings.TrimSpace(strings.Split(strings.TrimSpace(text), "\n")[0])
	if utf8.RuneCountInString(t) > 30 {
		t = string([]rune(t)[:30]) + "…"
	}
	if t == "" {
		t = "新对话"
	}
	return t
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
