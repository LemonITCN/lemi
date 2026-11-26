package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/aitest"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/reports"
)

// TestLiveE2E 用真实模型跑一次完整任务：建表、写入、查询、画图、写报告。
// 设置 LEMI_TEST_OPENCODE_KEY 才运行（密钥只放本机 .secrets/，绝不进仓库）；LEMI_TEST_MODEL 可指定模型 ID。
func TestLiveE2E(t *testing.T) {
	key := os.Getenv("LEMI_TEST_OPENCODE_KEY")
	if key == "" {
		t.Skip("未设置 LEMI_TEST_OPENCODE_KEY")
	}
	f := aitest.New(t)
	f.SQLite(t, "shop", conns.AIWriteAuto, nil)
	s := New(f.Store, f.Settings, f.Accounts, f.Box, nil, f.Log, f.Tools)
	s.TitleGen = true
	ctx := context.Background()
	p, err := s.SaveProvider(ctx, "", ProviderInput{Kind: ProviderOpenCode, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	models, def, errs := s.WorkspaceModels(ctx, f.WS)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var names []string
	for _, m := range models {
		names = append(names, m.ID)
	}
	t.Logf("可用模型：%s；默认 %s", strings.Join(names, ", "), def)
	model := def
	if m := os.Getenv("LEMI_TEST_MODEL"); m != "" {
		model = p.ID + "/" + m
	}
	conv, err := s.CreateConversation(ctx, f.WS, f.Owner, ConvSettings{Model: model, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "请在连接「shop」上完成一次小分析：\n" +
		"1. 新建表 city_sales(city TEXT PRIMARY KEY, paid_amount REAL, refund_orders INTEGER, orders INTEGER)，用一条 INSERT ... SELECT 把 orders 关联 users 按城市汇总写进去（paid_amount 只算 status='paid' 的金额）；\n" +
		"2. 查询各城市的退款率；\n3. 画一张各城市 paid_amount 的柱状图；\n4. 写一份简短的报告（引用图表和退款率的查询结果），告诉我报告 ID。"
	res, err := s.Send(ctx, f.Owner, conv.ID, "", SendInput{Text: prompt})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.GetRun(f.Owner.ID, res.RunID)
	deadline := time.After(12 * time.Minute)
	approved := 0
	for {
		select {
		case <-run.Done():
			goto done
		case <-deadline:
			s.Stop(f.Owner.ID, run.ID)
			t.Fatal("超时")
		case <-time.After(200 * time.Millisecond):
		}
		_, pend := run.Resume()
		for _, pd := range pend {
			id, _ := pd["id"].(string)
			switch pd["kind"] {
			case "approval":
				t.Logf("审批：%v %v", pd["class"], pd["sql"])
				approved++
				_ = s.Respond(f.Owner.ID, run.ID, id, Reply{Reply: "always"})
			case "doom_loop":
				_ = s.Respond(f.Owner.ID, run.ID, id, Reply{Reply: "once"})
			case "question":
				_ = s.Respond(f.Owner.ID, run.ID, id, Reply{Reply: "once", Message: "按你的判断来"})
			}
		}
	}
done:
	t.Logf("运行结束：%s %s，审批 %d 次", run.State(), run.errMsg, approved)
	msgs, _ := s.loadMessages(ctx, conv.ID)
	for _, m := range msgs {
		for _, pt := range m.Parts {
			switch pt.Type {
			case "text":
				if !pt.Hidden {
					t.Logf("[%s] %s", m.Role, truncateRunes(pt.Text, 600))
				}
			case "tool_call":
				t.Logf("[调用] %s %s", pt.Name, truncateRunes(string(pt.Args), 300))
			case "tool_result":
				t.Logf("[结果 %s] %s", pt.Status, truncateRunes(pt.Text, 200))
			}
		}
	}
	c2, _ := s.getConv(ctx, conv.ID)
	t.Logf("用量：%+v，上下文 %d", c2.Usage, c2.St.LastContext)
	if run.State() != RunDone {
		t.Fatalf("运行没有正常结束：%s", run.errMsg)
	}
	// 数据库里真的建了表并写入
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(f.Dir, "allowed", "shop.db"))
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM city_sales`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("city_sales 应有 3 个城市：%d %v", n, err)
	}
	list, _ := f.Tools.Reports.List(ctx, f.WS, 10)
	if len(list) == 0 {
		t.Fatal("应写出报告")
	}
	rp, _ := f.Tools.Reports.Get(ctx, list[0].ID)
	sn := rp.Decode()
	t.Logf("报告「%s」：%d 个图表、%d 个表格、缺失 %v\n%s", rp.Title, len(sn.Charts), len(sn.Tables), sn.Missing, truncateRunes(rp.Body, 1500))
	if len(sn.Charts) == 0 || len(reports.ParseBlocks(rp.Body)) == 0 {
		t.Fatal("报告应引用图表")
	}
	time.Sleep(2 * time.Second)
	c3, _ := s.getConv(ctx, conv.ID)
	t.Logf("标题：%s", c3.Title)
}
