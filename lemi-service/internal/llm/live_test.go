package llm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// 真模型联调：设置 LEMI_TEST_OPENCODE_KEY 才运行（密钥只放本机 .secrets/，绝不进仓库）。
// LEMI_TEST_MODELS 可指定逗号分隔的模型 ID。
func TestLiveOpenCode(t *testing.T) {
	key := os.Getenv("LEMI_TEST_OPENCODE_KEY")
	if key == "" {
		t.Skip("未设置 LEMI_TEST_OPENCODE_KEY")
	}
	p := &OpenCodeGo{APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	models := os.Getenv("LEMI_TEST_MODELS")
	if models == "" {
		ms, err := p.Models(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			t.Logf("模型 %s（%s）协议 %s 上下文 %d 推荐 %v", m.ID, m.Name, m.API, m.Context, m.Featured)
		}
		for _, m := range ms {
			if m.Featured && m.Type == "chat" {
				models = m.ID
				break
			}
		}
	}
	tool := ToolDef{Name: "run_query", Description: "执行只读 SQL", Schema: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}`)}
	for _, id := range strings.Split(models, ",") {
		t.Run(id, func(t *testing.T) {
			req := Request{Model: id, System: "你是测试助手。需要数据时必须调用工具。", Tools: []ToolDef{tool}, Effort: "low", SessionID: "lemi-live-test",
				Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: "text", Text: "orders 表有多少行？"}}}}}
			s, err := p.Stream(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			var call *Event
			for {
				e, err := s.Recv()
				if err != nil {
					break
				}
				if e.Type == EvToolCallEnd {
					ev := e
					call = &ev
				}
			}
			s.Close()
			if call == nil {
				t.Fatal("模型没有调用工具")
			}
			t.Logf("工具调用：%s %s", call.Name, call.Args)
		})
	}
}
