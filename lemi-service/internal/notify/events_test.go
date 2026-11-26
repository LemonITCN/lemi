package notify

import (
	"context"
	"testing"
)

// 渠道只收订阅的事件；不订阅任何事件 = 收全部；测试消息总是收。
func TestEventSubscription(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	ctx := context.Background()
	rc := newRecorder(t, nil)
	only := []string{EvBackupFailed, EvConnDown}
	c, err := f.s.Create(ctx, f.ws, f.owner.ID, &Input{Name: "只收失败", Kind: KindWebhook, Config: map[string]any{"url": rc.srv.URL}, Events: &only})
	if err != nil {
		t.Fatal(err)
	}
	all := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL + "/all"})
	f.s.Send(ctx, f.ws, []string{c.ID, all.ID}, Event{Kind: EvBackupSuccess, Title: "成功"})
	f.s.wait()
	if n := len(rc.all()); n != 1 {
		t.Fatalf("备份成功只该发给不限事件的渠道，实际 %d 次", n)
	}
	f.s.Send(ctx, f.ws, []string{c.ID, all.ID}, Event{Kind: EvBackupFailed, Title: "失败"})
	f.s.wait()
	if n := len(rc.all()); n != 3 {
		t.Fatalf("备份失败两个渠道都该收到，实际累计 %d 次", n)
	}
	if v := f.s.View(c, false); len(v["events"].([]string)) != 2 {
		t.Fatalf("视图里的订阅：%v", v["events"])
	}
	// 修改时不传 events 保持原值；传空数组 = 全部
	if err := f.s.Update(ctx, c, &Input{Name: c.Name, Kind: c.Kind, Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if len(c.EventList()) != 2 {
		t.Fatal("没传 events 时应保持原值")
	}
	empty := []string{}
	if err := f.s.Update(ctx, c, &Input{Name: c.Name, Kind: c.Kind, Config: map[string]any{}, Events: &empty}); err != nil {
		t.Fatal(err)
	}
	if !c.Wants(EvRestoreDone) {
		t.Fatal("清空订阅后应收全部事件")
	}
	bad := []string{"nope"}
	if _, err := f.s.Create(ctx, f.ws, f.owner.ID, &Input{Name: "x", Kind: KindWebhook, Config: map[string]any{"url": rc.srv.URL}, Events: &bad}); err == nil {
		t.Fatal("未知事件应被拒绝")
	}
}
