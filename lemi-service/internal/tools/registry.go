// Package tools 是工具注册表：同一份实现同时给内置智能体、定时任务与 MCP 使用。
// 权限（docs/spec.md §16.4）在这里统一计算：实际权限 = min(用户对连接的权限, 连接 AI 档位, 授权档位, 会话模式)。
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/reports"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
)

// 工具输出给模型的上限（超过即截断，完整内容存结果集）。
const (
	MaxOutputLines = 2000
	MaxOutputBytes = 50 << 10
)

// BackupInfo 由备份模块实现：装配时 a.Get("backup") 实现了它就注册 list_backups / backup_status / health_status。
// connIDs 是本次调用可见的连接（已按智能体权限过滤），返回值序列化成 JSON 给模型。
type BackupInfo interface {
	AIListBackups(ctx context.Context, u *accounts.User, workspaceID string, connIDs []string, limit int) (any, error)
	AIBackupStatus(ctx context.Context, u *accounts.User, workspaceID string, connIDs []string) (any, error)
	AIHealthStatus(ctx context.Context, u *accounts.User, workspaceID string, connIDs []string) (any, error)
}

// Services 是工具用到的服务。
type Services struct {
	Conns    *conns.Service
	Settings *settings.Service
	Accounts *accounts.Service
	Audit    *audit.Log
	Results  *results.Store
	Charts   *charts.Service
	Reports  *reports.Service
	Backup   BackupInfo // 可为空
	Log      *slog.Logger
}

// ClientBridge 让工具在用户的浏览器里执行（workspace_* 工具）。
type ClientBridge interface {
	Online() bool
	Call(ctx context.Context, tool string, args any) (json.RawMessage, error)
}

// AgentHooks 智能体专用工具（计划清单、子智能体、提问）的实现。
type AgentHooks interface {
	Todo(ctx context.Context, items []Todo) error
	Task(ctx context.Context, in TaskInput) (string, error)
	Ask(ctx context.Context, questions []Question) (string, error)
}

// 审批方式。
const (
	ApprovalInteractive = "interactive" // 内置智能体：弹审批卡片
	ApprovalDelegated   = "delegated"   // MCP：由外部智能体自己的确认机制把关（逐条确认档位不可用）
	ApprovalUnattended  = "unattended"  // 定时任务：需要确认的一律拒绝
)

// Grant 是授权对连接档位的上限（MCP 授权、定时任务的连接设置）。没有列出的连接为不可见。
type Grant map[string]string

// Env 是一次工具调用的上下文。
type Env struct {
	Ctx         context.Context
	Svc         *Services
	User        *accounts.User
	WorkspaceID string
	Source      string // audit.SrcAgent | audit.SrcMCP | audit.SrcTask
	Plan        bool   // 计划模式 / 子智能体：只读
	Grant       Grant  // nil = 只看连接上的 AI 档位
	Approval    string
	// Approve 审批（interactive 时由智能体实现）：返回 nil 表示允许
	Approve     func(ctx context.Context, req *ApprovalRequest) error
	Client      ClientBridge
	Agentic     AgentHooks
	IP          string
	PreviewRows int
	ConvID      string
	TaskID      string
	Sub         bool // 子智能体
}

// Result 是工具结果。
type Result struct {
	Text    string     `json:"text"`
	Data    any        `json:"data,omitempty"` // 给界面的结构化数据（结果 ID、图表 ID、预览行等）
	IsError bool       `json:"is_error,omitempty"`
	Images  []llm.Part `json:"-"`
	// Truncated 输出被截断、完整内容存为结果集
	Truncated string `json:"truncated_result_id,omitempty"`
	// Rejected 用户拒绝了审批；Halt 拒绝时没有说明，智能体应结束本步
	Rejected bool `json:"-"`
	Halt     bool `json:"-"`
}

// 工具种类标记。
const (
	KindRead   = "read"   // 只读
	KindWrite  = "write"  // 会写数据库（按档位确认）
	KindLemi   = "lemi"   // 只改 Lemi 自己的数据（报告）
	KindClient = "client" // 浏览器端执行
	KindAgent  = "agent"  // 智能体专用（计划清单、子智能体、提问）
)

// Tool 是一个工具。
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	Kind        string
	Destructive bool // MCP 注解：可能破坏数据
	// Available 可选：返回 false 时不出现在工具列表
	Available func(env *Env) bool
	Handler   func(env *Env, args json.RawMessage) (*Result, error)
}

// ReadOnly 是否不修改数据库。
func (t *Tool) ReadOnly() bool { return t.Kind != KindWrite }

// Registry 是工具注册表。
type Registry struct {
	tools   map[string]*Tool
	order   []string
	aliases map[string]string
}

// Default 全局注册表。
var Default = &Registry{tools: map[string]*Tool{}, aliases: map[string]string{}}

// Register 注册工具。
func (r *Registry) Register(t *Tool) {
	if _, dup := r.tools[t.Name]; dup {
		panic("工具重名：" + t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

// Alias 登记别名（模型常写错的名字）。
func (r *Registry) Alias(alias, name string) { r.aliases[normName(alias)] = name }

// Get 取工具。
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Profile 决定给哪类调用方哪些工具。
type Profile string

const (
	ProfileAgent Profile = "agent" // 内置智能体主会话
	ProfileSub   Profile = "sub"   // 子智能体：只读，不能派子智能体、不能提问、不能写
	ProfileTask  Profile = "task"  // 定时任务：没有浏览器工具与提问
	ProfileMCP   Profile = "mcp"   // MCP：没有浏览器工具与智能体专用工具
)

// List 列出某类调用方可用的工具（按注册顺序）。
func (r *Registry) List(env *Env, p Profile) []*Tool {
	var out []*Tool
	for _, n := range r.order {
		t := r.tools[n]
		switch p {
		case ProfileMCP:
			if t.Kind == KindAgent || t.Kind == KindClient {
				continue
			}
		case ProfileTask:
			if t.Kind == KindClient || t.Name == "ask_user" {
				continue
			}
		case ProfileSub:
			if t.Kind != KindRead {
				continue
			}
		}
		if p == ProfileSub && (t.Name == "task" || t.Name == "ask_user" || t.Name == "todo_write") {
			continue
		}
		if t.Kind == KindClient && (env.Client == nil || !env.Client.Online()) {
			continue
		}
		if t.Available != nil && !t.Available(env) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Defs 返回给模型的工具定义。
func Defs(ts []*Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(ts))
	for _, t := range ts {
		b, _ := json.Marshal(t.Schema)
		out = append(out, llm.ToolDef{Name: t.Name, Description: t.Description, Schema: b})
	}
	return out
}

func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Resolve 把模型给的工具名修正为可用工具：先精确匹配，再忽略大小写与下划线，再查别名。
func Resolve(name string, allowed []*Tool) (*Tool, bool) {
	for _, t := range allowed {
		if t.Name == name {
			return t, true
		}
	}
	n := normName(name)
	for _, t := range allowed {
		if normName(t.Name) == n {
			return t, true
		}
	}
	if target, ok := Default.aliases[n]; ok {
		for _, t := range allowed {
			if t.Name == target {
				return t, true
			}
		}
	}
	return nil, false
}

// ToolError 是给模型看的工具错误（不是系统错误）。
type ToolError struct{ Msg string }

func (e *ToolError) Error() string { return e.Msg }

func fail(format string, a ...any) error { return &ToolError{Msg: fmt.Sprintf(format, a...)} }

// ErrRejected 用户拒绝且没有说明：智能体结束本步。
var ErrRejected = errors.New("用户拒绝了这次操作")

// RejectedError 用户拒绝并给了说明：作为工具错误回灌，循环继续。
type RejectedError struct{ Message string }

func (e *RejectedError) Error() string { return "用户拒绝了这次操作，用户的说明：" + e.Message }

// Call 执行工具：参数按 schema 修复，所有错误都转成给模型看的结果，过长输出截断并存为结果集。
func (r *Registry) Call(env *Env, t *Tool, args json.RawMessage) (res *Result) {
	defer func() {
		if v := recover(); v != nil {
			if env.Svc != nil && env.Svc.Log != nil {
				env.Svc.Log.Error("工具执行崩溃", "tool", t.Name, "panic", v)
			}
			res = &Result{Text: "错误：工具执行时发生内部错误", IsError: true}
		}
	}()
	fixed, problems := Repair(t.Schema, args)
	if len(problems) > 0 {
		orig := strings.TrimSpace(string(args))
		if len(orig) > 2000 {
			orig = orig[:2000] + "…"
		}
		return &Result{Text: "错误：工具 " + t.Name + " 的参数不正确：\n- " + strings.Join(problems, "\n- ") +
			"\n原参数：" + orig + "\n请按工具定义改正后重新调用。", IsError: true}
	}
	out, err := t.Handler(env, fixed)
	if err != nil {
		var re *RejectedError
		switch {
		case errors.Is(err, ErrRejected):
			return &Result{Text: "用户拒绝了这次操作。", IsError: true, Rejected: true, Halt: true}
		case errors.As(err, &re):
			return &Result{Text: re.Error(), IsError: true, Rejected: true}
		case errors.Is(err, context.Canceled):
			return &Result{Text: "已中断（用户停止）", IsError: true}
		}
		return &Result{Text: "错误：" + errText(err), IsError: true}
	}
	if out == nil {
		out = &Result{Text: "完成"}
	}
	return env.truncate(t.Name, out)
}

func errText(err error) string {
	var te *ToolError
	if errors.As(err, &te) {
		return te.Msg
	}
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	msg := err.Error()
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	return msg
}

// truncate 超过 2000 行或 50 KB 时截断，完整内容存为文本结果。
func (env *Env) truncate(tool string, res *Result) *Result {
	text := res.Text
	lines := strings.Count(text, "\n") + 1
	if len(text) <= MaxOutputBytes && lines <= MaxOutputLines {
		return res
	}
	cut := text
	if len(cut) > MaxOutputBytes {
		cut = cut[:MaxOutputBytes]
		for len(cut) > 0 && !utf8Start(cut[len(cut)-1]) {
			cut = cut[:len(cut)-1]
		}
	}
	if ls := strings.SplitN(cut, "\n", MaxOutputLines+1); len(ls) > MaxOutputLines {
		cut = strings.Join(ls[:MaxOutputLines], "\n")
	}
	shown := strings.Count(cut, "\n") + 1
	if env.Svc == nil || env.Svc.Results == nil || env.User == nil {
		res.Text = cut + fmt.Sprintf("\n…（输出共 %d 行，已截断）", lines)
		return res
	}
	m, err := env.Svc.Results.SaveText(env.Ctx, results.Meta{WorkspaceID: env.WorkspaceID, UserID: env.User.ID, Title: tool + " 的完整输出", Source: env.Source}, text)
	if err != nil {
		res.Text = cut + fmt.Sprintf("\n…（输出共 %d 行，已截断）", lines)
		return res
	}
	res.Text = cut + fmt.Sprintf("\n\n…（输出共 %d 行、%d 字节，这里只显示前 %d 行。完整内容已保存为结果 %s：用 read_result 按行分页读取（offset/limit），或用 task 交给子智能体处理。）",
		lines, len(text), shown, m.ID)
	res.Truncated = m.ID
	return res
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// ---------- schema 构造辅助 ----------

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolean(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func arr(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
}
func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}
func anyObj(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc, "additionalProperties": true}
}

// bind 解析（已修复的）参数。
func bind(args json.RawMessage, dst any) error {
	if err := json.Unmarshal(args, dst); err != nil {
		return fail("参数格式不正确：%v", err)
	}
	return nil
}
