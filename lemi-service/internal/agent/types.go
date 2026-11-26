// Package agent 是内置智能体：服务端 agent loop（流式请求、工具边流边执行、重试与错误分类、上下文压缩、
// 续写、步数上限、死循环检测、审批、插话、停止）、子智能体、会话存储、事件流与续传（docs/spec.md §16.2、§24）。
package agent

import (
	"encoding/json"

	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// 会话模式。
const (
	ModeNormal = "normal"
	ModePlan   = "plan" // 计划模式：只读分析，禁止一切写入与 DDL
)

// 会话种类。
const (
	KindChat = "chat"
	KindTask = "task"
	KindSub  = "sub"
)

// 工具结果状态。
const (
	StatusDone        = "done"
	StatusError       = "error"
	StatusDenied      = "denied"      // 用户拒绝
	StatusInterrupted = "interrupted" // 用户停止
	StatusSkipped     = "skipped"     // 前面的操作被拒绝，后面的没有执行
	StatusLost        = "lost"        // 服务重启时没执行完（回放时连同调用一起丢掉）
)

// StoredPart 是存库的消息片段：llm.Part 加界面需要的字段。
type StoredPart struct {
	llm.Part
	Hidden   bool            `json:"hidden,omitempty"`   // 不在界面显示（系统更新、续写提示等）
	Title    string          `json:"title,omitempty"`    // 工具的中文名
	Data     json.RawMessage `json:"data,omitempty"`     // 工具结果给界面的结构化数据
	Status   string          `json:"status,omitempty"`   // 工具结果状态
	Duration int64           `json:"duration,omitempty"` // 工具耗时毫秒
}

// ConvSettings 会话设置。
type ConvSettings struct {
	Model  string `json:"model"`  // <供应商 ID>/<模型 ID>
	Effort string `json:"effort"` // 推理档位
	Mode   string `json:"mode"`   // normal | plan
}

// EnvFacts 是环境块里会变化的部分；变化时以 <system-update> 消息插入，系统提示本身不变。
type EnvFacts struct {
	Date  string `json:"date"`
	Mode  string `json:"mode"`
	Conns string `json:"conns"`
	Tabs  string `json:"tabs"`
}

// ConvState 会话的上下文管理状态。
type ConvState struct {
	Summary        string       `json:"summary,omitempty"`      // 压缩后的摘要（<conversation-checkpoint>）
	SummaryUpTo    int64        `json:"summary_upto,omitempty"` // 摘要覆盖到的消息序号（含）
	LastContext    int          `json:"last_context,omitempty"` // 最近一次实测的上下文 token
	LastContextSeq int64        `json:"last_context_seq,omitempty"`
	AllowRules     []string     `json:"allow_rules,omitempty"` // 本会话「总是允许」：工具|连接|语句类别
	Todos          []tools.Todo `json:"todos,omitempty"`
	Env            EnvFacts     `json:"env"`
	Compacting     bool         `json:"compacting,omitempty"`
	OverflowLevel  int          `json:"overflow_level,omitempty"`
}

// ConvUsage 会话累计用量。
type ConvUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Reasoning  int `json:"reasoning"`
	Requests   int `json:"requests"`
}

// Event 是推给界面的事件。
type Event struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
	At   int64  `json:"at"`
}

// TabInfo 是浏览器上报的一个打开的标签。
type TabInfo struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // console | table | structure | er | monitor | report
	ConnectionID string `json:"connection_id,omitempty"`
	DB           string `json:"db,omitempty"`
	Table        string `json:"table,omitempty"`
	Title        string `json:"title,omitempty"`
}

// ClientContext 是发送消息时浏览器带来的工作区上下文。
type ClientContext struct {
	OpenTabs  []TabInfo `json:"open_tabs"`
	ActiveTab string    `json:"active_tab"`
}

// Reply 是用户对审批 / 提问 / 死循环确认的回应。
type Reply struct {
	Reply   string            `json:"reply"`   // once | always | reject（审批、死循环确认）
	Message string            `json:"message"` // 拒绝时的说明
	Answers []json.RawMessage `json:"answers"` // 提问的回答：每题一个字符串或字符串数组
}
