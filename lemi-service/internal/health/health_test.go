package health

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	_ "github.com/LemonITCN/lemi/lemi-service/internal/driver/sqlite"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// fakeNotifier 记录发出的通知。
type fakeNotifier struct {
	mu   sync.Mutex
	sent []sentEvent
}

type sentEvent struct {
	ws       string
	channels []string
	ev       notify.Event
}

func (n *fakeNotifier) Send(_ context.Context, ws string, ch []string, ev notify.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, sentEvent{ws: ws, channels: ch, ev: ev})
}

func (n *fakeNotifier) CheckChannels(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		if id == "bad" {
			return apperr.Bad("通知渠道不存在或不属于该工作空间")
		}
	}
	return nil
}

func (n *fakeNotifier) FilterChannels(_ context.Context, _ string, ids []string) []string { return ids }

func (n *fakeNotifier) events() []sentEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]sentEvent(nil), n.sent...)
}

// fakeHub 记录推送，并按 filter 计算谁能收到。
type fakeHub struct {
	mu    sync.Mutex
	msgs  []any
	users []string // 用来测试 filter 的候选用户
	got   [][]string
}

func (h *fakeHub) Broadcast(t string, data any, filter func(string) bool) {
	var to []string
	for _, u := range h.users {
		if filter == nil || filter(u) {
			to = append(to, u)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if t == "health" {
		h.msgs = append(h.msgs, data)
		h.got = append(h.got, to)
	}
}

func (h *fakeHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.msgs)
}

type fixture struct {
	s      *Service
	cs     *conns.Service
	st     *store.Store
	acc    *accounts.Service
	set    *settings.Service
	box    *crypt.Box
	au     *audit.Log
	n      *fakeNotifier
	hub    *fakeHub
	owner  *accounts.User
	viewer *accounts.User
	other  *accounts.User
	ws     string
	dir    string
	clock  atomic.Int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "lemi.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, _ := crypt.New(make([]byte, 32))
	acc := accounts.New(st)
	set := settings.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := audit.New(st, log)
	f := &fixture{st: st, acc: acc, set: set, box: box, au: au, dir: dir, n: &fakeNotifier{}}
	f.owner = &accounts.User{Username: "owner", PasswordHash: "x"}
	f.viewer = &accounts.User{Username: "viewer", PasswordHash: "x"}
	f.other = &accounts.User{Username: "other", PasswordHash: "x"}
	for _, u := range []*accounts.User{f.owner, f.viewer, f.other} {
		if err := acc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := acc.CreateWorkspace(ctx, "测试", "", f.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws.ID
	if err := acc.SetMember(ctx, f.ws, f.viewer.ID, accounts.WSViewer); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed")
	os.MkdirAll(allowed, 0o700)
	os.MkdirAll(filepath.Join(dir, "sqlite"), 0o700)
	f.cs = conns.New(st, acc, set, box, au, log, driver.SQLiteEnv{AllowedDirs: []string{allowed}, UploadDir: filepath.Join(dir, "sqlite")})
	f.hub = &fakeHub{users: []string{f.owner.ID, f.viewer.ID, f.other.ID}}
	f.s = New(Deps{Store: st, Conns: f.cs, Accounts: acc, Notify: f.n, Hub: f.hub, Log: log, Location: time.UTC})
	f.clock.Store(time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC).UnixMilli())
	f.s.now = func() time.Time { return time.UnixMilli(f.clock.Load()) }
	t.Cleanup(func() {
		f.cs.Close()
		st.Close()
	})
	return f
}

func (f *fixture) advance(d time.Duration) { f.clock.Add(d.Milliseconds()) }

func (f *fixture) sqliteConn(t *testing.T, name string) *conns.Connection {
	t.Helper()
	path := filepath.Join(f.dir, "allowed", name)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE IF NOT EXISTS seed (x INTEGER)")
	db.Close()
	c, err := f.cs.Create(context.Background(), f.ws, f.owner.ID, &conns.Input{Name: "本地 " + name, Driver: "sqlite", Params: map[string]any{"file": path}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// scripted 返回按顺序给出结果的检查函数。
func scripted(results ...bool) (Checker, *atomic.Int32) {
	var n atomic.Int32
	return func(ctx context.Context, c *conns.Connection) error {
		i := int(n.Add(1)) - 1
		if i < len(results) && results[i] {
			return nil
		}
		return errors.New("连接失败：connection refused")
	}, &n
}

func TestStateMachine(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "a.db")
	if err := f.s.SetConfig(ctx, c, Config{Enabled: true, IntervalSec: 30, TimeoutSec: 5, FailThreshold: 3, Channels: []string{"ch1", "ch1"}}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	f.s.Checker, _ = scripted(true, false, false, false, false, true, true, false, false, false)
	type step struct {
		status  string
		changed bool
		fails   int
	}
	want := []step{
		{StatusUp, true, 0},    // unknown → up（不是恢复，不通知）
		{StatusUp, false, 1},   // 失败 1 次
		{StatusUp, false, 2},   // 失败 2 次
		{StatusDown, true, 3},  // 连续 3 次 → 下线，通知
		{StatusDown, false, 4}, // 仍然下线，不重复通知
		{StatusUp, true, 0},    // 一次成功即恢复，通知
		{StatusUp, false, 0},
		{StatusUp, false, 1},
		{StatusUp, false, 2},
		{StatusDown, true, 3},
	}
	for i, w := range want {
		f.advance(time.Minute)
		res, err := f.s.CheckNow(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != w.status || res.Changed != w.changed || res.Fails != w.fails {
			t.Fatalf("第 %d 次检查：%+v，应为 %+v", i+1, res, w)
		}
	}
	ev := f.n.events()
	if len(ev) != 3 || ev[0].ev.Kind != notify.EvConnDown || ev[1].ev.Kind != notify.EvConnUp || ev[2].ev.Kind != notify.EvConnDown {
		t.Fatalf("只在下线与恢复时通知，实际 %+v", ev)
	}
	if ev[0].ws != f.ws || len(ev[0].channels) != 1 || ev[0].channels[0] != "ch1" || ev[0].ev.Level != notify.LevelError ||
		!strings.Contains(ev[0].ev.Message, "connection refused") || !strings.HasPrefix(ev[0].ev.Link, "/w/"+f.ws+"/") {
		t.Fatalf("下线通知内容不对：%+v", ev[0])
	}
	if !strings.Contains(ev[1].ev.Message, "下线持续了 2 分钟") {
		t.Fatalf("恢复通知应带下线时长：%s", ev[1].ev.Message)
	}
	if f.hub.count() != len(want) {
		t.Fatalf("每次检查都应推送，实际 %d", f.hub.count())
	}
	// 推送只给空间成员（other 不是成员）
	f.hub.mu.Lock()
	to := f.hub.got[0]
	f.hub.mu.Unlock()
	if len(to) != 2 || to[0] != f.owner.ID || to[1] != f.viewer.ID {
		t.Fatalf("推送对象不对：%v", to)
	}
	// 被收紧为 none 的成员收不到
	if err := f.cs.SetLimit(ctx, c.ID, f.viewer.ID, conns.LevelNone); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	if _, err := f.s.CheckNow(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	f.hub.mu.Lock()
	to = f.hub.got[len(f.hub.got)-1]
	f.hub.mu.Unlock()
	if len(to) != 1 || to[0] != f.owner.ID {
		t.Fatalf("对连接没有权限的成员不应收到推送：%v", to)
	}
	st, at, _ := f.s.Status(ctx, c.ID)
	if st != StatusDown || at != f.clock.Load() {
		t.Fatalf("Status 不对：%s %d", st, at)
	}
	// 关闭检查：状态回到未知，Status 报 disabled
	if err := f.s.SetConfig(ctx, c, Config{Enabled: false, IntervalSec: 60, TimeoutSec: 10, FailThreshold: 3}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := f.s.Status(ctx, c.ID); st != StatusDisabled {
		t.Fatalf("关闭后应为 disabled：%s", st)
	}
	var n int
	_ = f.st.Get(ctx, &n, `SELECT COUNT(*) FROM health_checks WHERE connection_id = ?`, c.ID)
	if n != len(want)+1 {
		t.Fatalf("检查记录数 %d", n)
	}
}

func TestUnknownToDownAndNoChannels(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "b.db")
	f.s.Checker, _ = scripted(false, false, false)
	for i := 0; i < 3; i++ {
		res, err := f.s.CheckNow(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && res.Status != StatusUnknown {
			t.Fatalf("失败不足 3 次应保持 unknown：%+v", res)
		}
		if i == 2 && (res.Status != StatusDown || !res.Changed) {
			t.Fatalf("从未成功过、连续失败 3 次应判定下线：%+v", res)
		}
	}
	if len(f.n.events()) != 0 {
		t.Fatal("没有配置通知渠道时不应发通知")
	}
}

func TestConfigValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "c.db")
	cfg, custom, err := f.s.Config(ctx, c.ID)
	if err != nil || custom || !cfg.Enabled || cfg.IntervalSec != 60 || cfg.TimeoutSec != 10 || cfg.FailThreshold != 3 {
		t.Fatalf("没有配置行时应为默认值：%+v %v %v", cfg, custom, err)
	}
	bad := []Config{
		{Enabled: true, IntervalSec: 5, TimeoutSec: 1, FailThreshold: 3},
		{Enabled: true, IntervalSec: 60, TimeoutSec: 0, FailThreshold: 3},
		{Enabled: true, IntervalSec: 10, TimeoutSec: 20, FailThreshold: 3},
		{Enabled: true, IntervalSec: 60, TimeoutSec: 10, FailThreshold: 0},
		{Enabled: true, IntervalSec: 60, TimeoutSec: 10, FailThreshold: 3, Channels: []string{"bad"}},
	}
	for _, b := range bad {
		if err := f.s.SetConfig(ctx, c, b, f.owner.ID); err == nil {
			t.Fatalf("应拒绝 %+v", b)
		}
	}
	if err := f.s.SetConfig(ctx, c, Config{Enabled: true, IntervalSec: 10, TimeoutSec: 3, FailThreshold: 1}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	if cfg, custom, _ := f.s.Config(ctx, c.ID); !custom || cfg.IntervalSec != 10 || cfg.Channels == nil {
		t.Fatalf("保存后读回不对：%+v", cfg)
	}
}

func TestRealSQLiteCheckAndTimeout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "real.db")
	res, err := f.s.CheckNow(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Status != StatusUp || res.Error != "" {
		t.Fatalf("真实 SQLite 检查应成功：%+v", res)
	}
	// 文件被删掉后检查失败，错误被记录
	if err := os.Remove(filepath.Join(f.dir, "allowed", "real.db")); err != nil {
		t.Fatal(err)
	}
	res, err = f.s.CheckNow(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error == "" || res.Fails != 1 {
		t.Fatalf("文件不存在时检查应失败：%+v", res)
	}
	// 检查函数不理会超时也按时返回
	if err := f.s.SetConfig(ctx, c, Config{Enabled: true, IntervalSec: 10, TimeoutSec: 1, FailThreshold: 3}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	f.s.Checker = func(ctx context.Context, c *conns.Connection) error { <-release; return nil }
	start := time.Now()
	res, err = f.s.CheckNow(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Error, "超时") || time.Since(start) > 3*time.Second {
		t.Fatalf("超时处理不对：%+v，用时 %v", res, time.Since(start))
	}
}

func TestRedactSecrets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// MySQL 连接的口令出现在错误里时被替换
	c, err := f.cs.Create(ctx, f.ws, f.owner.ID, &conns.Input{Name: "my", Driver: "mysql", Params: map[string]any{"host": "db.invalid", "port": 3306, "user": "u", "password": "TopSecret99"}})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Checker = func(ctx context.Context, c *conns.Connection) error {
		return errors.New("dial failed for u:TopSecret99@tcp(db.invalid:3306)")
	}
	res, err := f.s.CheckNow(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Error, "TopSecret99") {
		t.Fatalf("错误文本不应含口令：%s", res.Error)
	}
	var stored string
	_ = f.st.Get(ctx, &stored, `SELECT error FROM health_checks WHERE connection_id = ?`, c.ID)
	if strings.Contains(stored, "TopSecret99") {
		t.Fatal("检查记录不应含口令")
	}
}

func TestSchedulerAndRun(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := f.sqliteConn(t, "s1.db")
	b := f.sqliteConn(t, "s2.db")
	var calls sync.Map
	f.s.Checker = func(ctx context.Context, c *conns.Connection) error {
		v, _ := calls.LoadOrStore(c.ID, new(atomic.Int32))
		v.(*atomic.Int32).Add(1)
		return nil
	}
	count := func(id string) int32 {
		if v, ok := calls.Load(id); ok {
			return v.(*atomic.Int32).Load()
		}
		return 0
	}
	// b 关闭检查
	if err := f.s.SetConfig(ctx, b, Config{Enabled: false, IntervalSec: 60, TimeoutSec: 10, FailThreshold: 3}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	f.s.reload(ctx)
	f.s.mu.Lock()
	_, hasA := f.s.sched[a.ID]
	_, hasB := f.s.sched[b.ID]
	f.s.mu.Unlock()
	if !hasA || hasB {
		t.Fatalf("没有配置的连接应默认启用、关闭的不调度：a=%v b=%v", hasA, hasB)
	}
	f.advance(11 * time.Second) // 越过启动时的随机分散
	f.s.dispatchDue(ctx)
	f.s.wg.Wait()
	if count(a.ID) != 1 || count(b.ID) != 0 {
		t.Fatalf("到期应检查一次：a=%d b=%d", count(a.ID), count(b.ID))
	}
	f.s.dispatchDue(ctx) // 没到下一次
	f.s.wg.Wait()
	if count(a.ID) != 1 {
		t.Fatal("未到间隔不应重复检查")
	}
	f.advance(60 * time.Second)
	f.s.dispatchDue(ctx)
	f.s.wg.Wait()
	if count(a.ID) != 2 {
		t.Fatalf("到下一个间隔应再检查：%d", count(a.ID))
	}
	// 删除连接后移出调度
	if err := f.cs.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	f.s.reload(ctx)
	f.s.mu.Lock()
	n := len(f.s.sched)
	f.s.mu.Unlock()
	if n != 0 {
		t.Fatalf("删除的连接应移出调度：%d", n)
	}

	// 真实的 Run 循环：重新打开 b 的检查后立即生效
	oldTick, oldReload := TickInterval, ReloadInterval
	TickInterval, ReloadInterval = 5*time.Millisecond, 50*time.Millisecond
	defer func() { TickInterval, ReloadInterval = oldTick, oldReload }()
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { f.s.Run(rctx); close(done) }()
	if err := f.s.SetConfig(ctx, b, Config{Enabled: true, IntervalSec: 10, TimeoutSec: 5, FailThreshold: 3}, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for count(b.ID) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if count(b.ID) == 0 {
		t.Fatal("重新打开检查后应立即调度")
	}
	if st, _, _ := f.s.Status(ctx, b.ID); st != StatusUp {
		t.Fatalf("Run 检查后的状态：%s", st)
	}
}

func TestConcurrentManualChecksShareOneFlight(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "f.db")
	var calls atomic.Int32
	gate := make(chan struct{})
	f.s.Checker = func(ctx context.Context, c *conns.Connection) error {
		calls.Add(1)
		<-gate
		return nil
	}
	var wg sync.WaitGroup
	results := make([]*CheckResult, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = f.s.CheckNow(ctx, c.ID)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("同一连接同时只应有一个检查，实际 %d", calls.Load())
	}
	for _, r := range results {
		if r == nil || !r.OK {
			t.Fatal("等待中的请求应拿到同一个结果")
		}
	}
}

func TestPrune(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "p.db")
	now := f.clock.Load()
	for _, at := range []int64{now - int64(31*24*time.Hour/time.Millisecond), now - int64(29*24*time.Hour/time.Millisecond), now} {
		if _, err := f.st.Exec(ctx, `INSERT INTO health_checks (connection_id, at, ok, latency_ms) VALUES (?,?,1,5)`, c.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	f.s.Prune(ctx)
	var n int
	_ = f.st.Get(ctx, &n, `SELECT COUNT(*) FROM health_checks`)
	if n != 2 {
		t.Fatalf("应只删掉超过 30 天的记录，剩 %d", n)
	}
	// 删除连接时记录随之删除（外键级联）
	if err := f.cs.Delete(ctx, c); err != nil {
		t.Fatal(err)
	}
	_ = f.st.Get(ctx, &n, `SELECT COUNT(*) FROM health_checks`)
	if n != 0 {
		t.Fatalf("删除连接后记录应级联删除，剩 %d", n)
	}
}
