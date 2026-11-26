package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Todo 是计划清单的一项。
type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"` // pending | in_progress | completed | cancelled
	Priority string `json:"priority,omitempty"`
}

// TaskInput 是派子智能体的参数。
type TaskInput struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"` // explore | analyst
	TaskID       string `json:"task_id"`       // 续接之前的子智能体
}

// Question 是向用户提的一个问题（选择题或填空）。
type Question struct {
	Question string           `json:"question"`
	Header   string           `json:"header,omitempty"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multiple bool             `json:"multiple,omitempty"`
}

// QuestionOption 是选项。
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func init() {
	r := Default
	r.Register(&Tool{
		Name: "todo_write", Title: "计划清单", Kind: KindAgent,
		Description: "维护本次任务的计划清单（每次传完整清单，整份替换）。3 步以上的任务开始前先列计划；执行中及时更新：同一时间只有一项 in_progress，" +
			"做完一项立刻标 completed，不再需要的标 cancelled。简单问答不需要计划。",
		Schema: obj(map[string]any{
			"todos": arr(obj(map[string]any{
				"content":  str("这一步要做什么（简短）"),
				"status":   enum("状态", "pending", "in_progress", "completed", "cancelled"),
				"priority": enum("优先级", "high", "medium", "low"),
			}, "content", "status"), "完整的计划清单"),
		}, "todos"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Todos []Todo `json:"todos"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if env.Agentic == nil {
				return nil, fail("当前环境不支持计划清单")
			}
			inProgress, done := 0, 0
			for i := range in.Todos {
				in.Todos[i].Content = strings.TrimSpace(in.Todos[i].Content)
				switch in.Todos[i].Status {
				case "in_progress":
					inProgress++
				case "completed":
					done++
				}
			}
			if len(in.Todos) > 50 {
				return nil, fail("计划清单最多 50 项")
			}
			if err := env.Agentic.Todo(env.Ctx, in.Todos); err != nil {
				return nil, err
			}
			msg := fmt.Sprintf("计划已更新（%d/%d 完成）。", done, len(in.Todos))
			if inProgress > 1 {
				msg += "注意：同一时间应该只有一项 in_progress。"
			}
			return &Result{Text: msg, Data: map[string]any{"todos": in.Todos}}, nil
		},
	})
	r.Register(&Tool{
		Name: "task", Title: "子智能体", Kind: KindAgent,
		Description: "派一个独立上下文的子智能体完成一项可以单独完成的子任务，它做完只把结论交回来，不占用你的上下文。" +
			"subagent_type：explore（只读，摸清结构与数据分布：有哪些表、关键列、数据量、取值分布、数据质量）；analyst（只读，完成一个独立的分析小任务，返回结论与结果 ID / 图表 ID）。" +
			"子智能体不能写入、不能再派子智能体、不能问用户。同一轮里调用多次会并行执行。prompt 要写清楚目标、范围（连接、库、表）、要交回什么。" +
			"返回 <task_result task_id=\"…\">…</task_result>；要让同一个子智能体继续之前的工作，传它的 task_id。",
		Schema: obj(map[string]any{
			"description":   str("3–8 个字的任务名"),
			"prompt":        str("给子智能体的完整任务说明"),
			"subagent_type": enum("子智能体类型", "explore", "analyst"),
			"task_id":       str("可选：续接之前的子智能体"),
		}, "description", "prompt", "subagent_type"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in TaskInput
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if env.Agentic == nil || env.Sub {
				return nil, fail("子智能体不能再派出子智能体")
			}
			if strings.TrimSpace(in.Prompt) == "" {
				return nil, fail("prompt 不能为空")
			}
			out, err := env.Agentic.Task(env.Ctx, in)
			if err != nil {
				return nil, err
			}
			return &Result{Text: out}, nil
		},
	})
	r.Register(&Tool{
		Name: "ask_user", Title: "向用户提问", Kind: KindAgent,
		Description: "需求不明确、有多种合理做法、或者要做影响较大的改动前，向用户提 1–4 个问题并等待回答。每个问题可以给 2–5 个选项（选择题，用户也能自己输入），" +
			"不给选项就是填空题。能从数据或常识判断的事不要问。",
		Schema: obj(map[string]any{
			"questions": arr(obj(map[string]any{
				"question": str("完整的问题"),
				"header":   str("很短的标签（不超过 8 个字）"),
				"multiple": boolean("是否可多选"),
				"options": arr(obj(map[string]any{
					"label":       str("选项（简短）"),
					"description": str("选项说明"),
				}, "label"), "可选：2–5 个选项"),
			}, "question"), "问题列表"),
		}, "questions"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Questions []Question `json:"questions"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if env.Agentic == nil || env.Sub {
				return nil, fail("当前环境不能向用户提问，请根据已有信息自行判断")
			}
			if len(in.Questions) == 0 || len(in.Questions) > 4 {
				return nil, fail("一次提 1–4 个问题")
			}
			ans, err := env.Agentic.Ask(env.Ctx, in.Questions)
			if err != nil {
				return nil, err
			}
			return &Result{Text: "用户的回答：\n" + ans}, nil
		},
	})
	r.Alias("todowrite", "todo_write")
	r.Alias("todo", "todo_write")
	r.Alias("subagent", "task")
	r.Alias("agent", "task")
	r.Alias("ask", "ask_user")
	r.Alias("question", "ask_user")
}
