package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
)

func TestAggregate(t *testing.T) {
	base := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	bounds := []time.Time{base, base.Add(time.Hour), base.Add(2 * time.Hour), base.Add(3 * time.Hour)}
	ms := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	var pts []checkPoint
	// 第 1 小时：20 次成功，延迟 1..20
	for i := 1; i <= 20; i++ {
		pts = append(pts, checkPoint{At: ms(time.Duration(i) * time.Minute), OK: true, LatencyMS: int64(i)})
	}
	// 第 2 小时：没有检查；第 3 小时：2 成功 2 失败
	pts = append(pts,
		checkPoint{At: ms(2*time.Hour + time.Minute), OK: true, LatencyMS: 10},
		checkPoint{At: ms(2*time.Hour + 2*time.Minute), OK: false, LatencyMS: 10000},
		checkPoint{At: ms(2*time.Hour + 3*time.Minute), OK: false, LatencyMS: 10000},
		checkPoint{At: ms(2*time.Hour + 4*time.Minute), OK: true, LatencyMS: 30},
		checkPoint{At: ms(3 * time.Hour), OK: false}, // 落在范围外（End 不含）
	)
	bks, sum := aggregate(pts, bounds)
	if len(bks) != 3 {
		t.Fatalf("格子数 %d", len(bks))
	}
	b0 := bks[0]
	if b0.Checks != 20 || b0.Failures != 0 || *b0.Uptime != 1 || *b0.AvgLatencyMS != 10 || *b0.P95LatencyMS != 19 || *b0.MaxLatencyMS != 20 || b0.Status != "up" {
		t.Fatalf("第 1 格不对：%+v avg=%d p95=%d", b0, *b0.AvgLatencyMS, *b0.P95LatencyMS)
	}
	if b1 := bks[1]; b1.Checks != 0 || b1.Uptime != nil || b1.AvgLatencyMS != nil || b1.Status != "none" {
		t.Fatalf("空格子不对：%+v", b1)
	}
	b2 := bks[2]
	if b2.Checks != 4 || b2.Failures != 2 || *b2.Uptime != 0.5 || *b2.AvgLatencyMS != 20 || *b2.MaxLatencyMS != 30 || b2.Status != "partial" {
		t.Fatalf("第 3 格不对（延迟只算成功的检查）：%+v", b2)
	}
	if sum.Checks != 24 || sum.Failures != 2 || *sum.MaxLatencyMS != 30 || sum.Start != bounds[0].UnixMilli() || sum.End != bounds[3].UnixMilli() {
		t.Fatalf("汇总不对：%+v", sum)
	}
	if u := *sum.Uptime; u < 0.916 || u > 0.917 {
		t.Fatalf("汇总可用率 %v", u)
	}
	// 全部失败
	bks, _ = aggregate([]checkPoint{{At: ms(time.Minute), OK: false}}, bounds[:2])
	if bks[0].Status != "down" || *bks[0].Uptime != 0 || bks[0].P95LatencyMS != nil {
		t.Fatalf("全部失败的格子：%+v", bks[0])
	}
}

func TestTimelineBounds(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 26, 1, 30, 0, 0, time.UTC) // 上海 09:30
	b, step, err := timelineBounds("day", now, sh)
	if err != nil || step != "hour" || len(b) != 25 || !b[0].Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, sh)) || !b[24].Equal(time.Date(2026, 11, 27, 0, 0, 0, 0, sh)) {
		t.Fatalf("今天：%v %s %v", err, step, b)
	}
	b, step, _ = timelineBounds("7d", now, sh)
	if step != "day" || len(b) != 8 || !b[0].Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, sh)) || !b[7].Equal(time.Date(2026, 11, 27, 0, 0, 0, 0, sh)) {
		t.Fatalf("7 天：%v", b)
	}
	if b, _, _ = timelineBounds("30d", now, sh); len(b) != 31 {
		t.Fatalf("30 天：%d", len(b))
	}
	if _, _, err := timelineBounds("1y", now, sh); err == nil {
		t.Fatal("不支持的范围应报错")
	}
}

func TestTimelineFromDB(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.sqliteConn(t, "t.db")
	f.clock.Store(time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC).UnixMilli())
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	ins := func(at time.Time, ok bool, lat int64) {
		if _, err := f.st.Exec(ctx, `INSERT INTO health_checks (connection_id, at, ok, latency_ms) VALUES (?,?,?,?)`, c.ID, at.UnixMilli(), ok, lat); err != nil {
			t.Fatal(err)
		}
	}
	ins(day.Add(9*time.Hour+5*time.Minute), true, 7)
	ins(day.Add(9*time.Hour+6*time.Minute), false, 0)
	ins(day.Add(-2*time.Hour), true, 3)    // 昨天
	ins(day.Add(-8*24*time.Hour), true, 3) // 8 天前
	tl, err := f.s.Timeline(ctx, c.ID, "day", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Buckets) != 24 || tl.Buckets[9].Checks != 2 || tl.Summary.Checks != 2 || tl.TZ != "UTC" || tl.Interval != 60 {
		t.Fatalf("今天的时间线不对：%+v", tl.Summary)
	}
	tl, _ = f.s.Timeline(ctx, c.ID, "7d", nil)
	if len(tl.Buckets) != 7 || tl.Summary.Checks != 3 || tl.Buckets[5].Checks != 1 || tl.Buckets[6].Checks != 2 {
		t.Fatalf("7 天的时间线不对：%+v", tl.Summary)
	}
	tl, _ = f.s.Timeline(ctx, c.ID, "30d", nil)
	if len(tl.Buckets) != 30 || tl.Summary.Checks != 4 {
		t.Fatalf("30 天的时间线不对：%+v", tl.Summary)
	}
	list, next, err := f.s.Checks(ctx, c.ID, 0, 3, false)
	if err != nil || len(list) != 3 || next == 0 || list[0].ID < list[1].ID {
		t.Fatalf("检查记录分页：%v %d", err, next)
	}
	list, _, _ = f.s.Checks(ctx, c.ID, next, 3, false)
	if len(list) != 1 {
		t.Fatalf("第二页：%d", len(list))
	}
	list, _, _ = f.s.Checks(ctx, c.ID, 0, 10, true)
	if len(list) != 1 || list[0].OK {
		t.Fatal("只看失败的记录不对")
	}
}

// ---------- HTTP ----------

type apiClient struct {
	t    *testing.T
	base string
	tok  string
	csrf string
}

func (c *apiClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	req.Header.Set("X-Lemi-CSRF", c.csrf)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: c.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestHTTP(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := auth.New(f.st, f.acc, f.set, f.box)
	srv := web.NewServer(web.Deps{Log: log, Cfg: &config.Config{DataDir: f.dir}, Store: f.st, Auth: au, Accounts: f.acc, Settings: f.set,
		Audit: f.au, Hub: realtime.New(log), Box: f.box, Started: time.Now(), SetupToken: func() string { return "" }, OnSetupDone: func() {}})
	srv.AddMount(f.s.Routes)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	login := func(u *accounts.User) *apiClient {
		tok, sess, err := au.CreateSession(ctx, u, auth.StageDone, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		return &apiClient{t: t, base: ts.URL, tok: tok, csrf: sess.CSRF}
	}
	owner, viewer, other := login(f.owner), login(f.viewer), login(f.other)
	c := f.sqliteConn(t, "h.db")
	hidden := f.sqliteConn(t, "hidden.db")
	if err := f.cs.SetLimit(ctx, hidden.ID, f.viewer.ID, conns.LevelNone); err != nil {
		t.Fatal(err)
	}
	base := "/workspaces/" + f.ws + "/health"

	code, out := viewer.do("GET", base, nil)
	if code != 200 {
		t.Fatalf("列表：%d %v", code, out)
	}
	items := out["connections"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["connection_id"] != c.ID || items[0].(map[string]any)["status"] != "unknown" {
		t.Fatalf("查看者只能看到自己能访问的连接：%v", items)
	}
	if code, _ := other.do("GET", base, nil); code != 404 {
		t.Fatalf("非成员应 404：%d", code)
	}
	if code, _ := viewer.do("GET", base+"/"+hidden.ID+"/config", nil); code != 404 {
		t.Fatalf("被收紧为 none 的连接应 404：%d", code)
	}
	code, out = viewer.do("GET", base+"/"+c.ID+"/config", nil)
	if code != 200 || out["custom"] != false || out["config"].(map[string]any)["interval_sec"].(float64) != 60 {
		t.Fatalf("读取配置：%d %v", code, out)
	}
	put := map[string]any{"enabled": true, "interval_sec": 30, "timeout_sec": 5, "fail_threshold": 2, "channels": []string{}}
	if code, _ := viewer.do("PUT", base+"/"+c.ID+"/config", put); code != 403 {
		t.Fatalf("查看者不能改配置：%d", code)
	}
	if code, out = owner.do("PUT", base+"/"+c.ID+"/config", put); code != 200 || out["custom"] != true {
		t.Fatalf("改配置：%d %v", code, out)
	}
	put["channels"] = []string{"bad"}
	if code, _ := owner.do("PUT", base+"/"+c.ID+"/config", put); code != 400 {
		t.Fatalf("无效渠道应 400：%d", code)
	}
	if code, _ := viewer.do("POST", base+"/"+c.ID+"/check", nil); code != 403 {
		t.Fatalf("查看者不能立即检查：%d", code)
	}
	code, out = owner.do("POST", base+"/"+c.ID+"/check", nil)
	if code != 200 || out["ok"] != true || out["status"] != "up" {
		t.Fatalf("立即检查：%d %v", code, out)
	}
	code, out = viewer.do("GET", base+"/"+c.ID+"/timeline?range=7d&tz=Asia/Shanghai", nil)
	if code != 200 || len(out["buckets"].([]any)) != 7 || out["tz"] != "Asia/Shanghai" {
		t.Fatalf("时间线：%d %v", code, out)
	}
	if code, _ := viewer.do("GET", base+"/"+c.ID+"/timeline?range=day&tz=Nowhere/City", nil); code != 400 {
		t.Fatal("无效时区应 400")
	}
	code, out = viewer.do("GET", base+"/"+c.ID+"/checks?limit=10", nil)
	if code != 200 || len(out["checks"].([]any)) != 1 || out["next_cursor"] != "" {
		t.Fatalf("检查记录：%d %v", code, out)
	}
	// 连接不属于路径里的空间
	ws2, _ := f.acc.CreateWorkspace(ctx, "二", "", f.owner.ID)
	if code, _ := owner.do("GET", "/workspaces/"+ws2.ID+"/health/"+c.ID+"/config", nil); code != 404 {
		t.Fatalf("连接不属于该空间应 404：%d", code)
	}
	f.au.Flush()
	var n int
	_ = f.st.Get(ctx, &n, `SELECT COUNT(*) FROM audit_log WHERE action = 'health.config_update'`)
	if n != 1 {
		t.Fatalf("改配置应记审计：%d", n)
	}
}
