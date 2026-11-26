package backup

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/sched"
)

func joinIDs(list []string) string {
	sort.Strings(list)
	return strings.Join(list, ",")
}

func TestRetentionCountDays(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var items []Item
	for i := 0; i < 10; i++ { // 每 6 小时一份，b0 最新
		items = append(items, Item{ID: string(rune('a'+i)) + "", At: now.Add(-time.Duration(i) * 6 * time.Hour)})
	}
	del := Retention{Mode: "count", Count: 3}.Select(items, now, time.UTC)
	if joinIDs(del) != "d,e,f,g,h,i,j" {
		t.Fatalf("count：%v", del)
	}
	del = Retention{Mode: "days", Days: 1}.Select(items, now, time.UTC)
	if joinIDs(del) != "f,g,h,i,j" {
		t.Fatalf("days：%v", del)
	}
	// 1 小时之内的新备份永远不删
	fresh := []Item{{ID: "n1", At: now.Add(-10 * time.Minute)}, {ID: "n2", At: now.Add(-20 * time.Minute)}, {ID: "old", At: now.Add(-2 * time.Hour)}}
	del = Retention{Mode: "count", Count: 1}.Select(fresh, now, time.UTC)
	if joinIDs(del) != "old" {
		t.Fatalf("宽限期：%v", del)
	}
	if del := (Retention{}).Select(items, now, time.UTC); len(del) != 0 {
		t.Fatal("没有规则时不删")
	}
}

func TestRetentionGFS(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, loc)
	// 过去 400 天每天 02:00 一份，外加今天每小时一份
	var items []Item
	for d := 0; d < 400; d++ {
		at := time.Date(2026, 11, 26, 2, 0, 0, 0, loc).AddDate(0, 0, -d)
		items = append(items, Item{ID: at.Format("D2006-01-02"), At: at})
	}
	for h := 3; h <= 11; h++ {
		at := time.Date(2026, 11, 26, h, 0, 0, 0, loc)
		items = append(items, Item{ID: at.Format("H15"), At: at})
	}
	r := Retention{Mode: "gfs", Hourly: 4, Daily: 7, Weekly: 4, Monthly: 6, Yearly: 2}
	del := r.Select(items, now, loc)
	delSet := map[string]bool{}
	for _, id := range del {
		delSet[id] = true
	}
	keep := []string{}
	for _, it := range items {
		if !delSet[it.ID] {
			keep = append(keep, it.ID)
		}
	}
	sort.Strings(keep)
	// 时：最近 4 个小时段（11、10、9 时，外加 1 小时宽限内的 11 时本身） → H11 H10 H09 H08
	for _, want := range []string{"H11", "H10", "H09", "H08"} {
		if delSet[want] {
			t.Errorf("小时档应保留 %s", want)
		}
	}
	if !delSet["H07"] {
		t.Error("H07 超出小时档应删除")
	}
	// 日：每天最新一份；今天最新的是 H11，之前 6 天是各自 02:00
	for d := 1; d <= 6; d++ {
		id := time.Date(2026, 11, 26, 2, 0, 0, 0, loc).AddDate(0, 0, -d).Format("D2006-01-02")
		if delSet[id] {
			t.Errorf("日档应保留 %s", id)
		}
	}
	if !delSet["D2026-11-26"] {
		t.Error("今天 02:00 那份不是当天最新，日档不该保留它")
	}
	// 月：最近 6 个月每月最新一份（本月是 H11，之前各月是月末那天）
	for _, id := range []string{"D2026-10-31", "D2026-09-30", "D2026-08-31", "D2026-07-31", "D2026-06-30"} {
		if delSet[id] {
			t.Errorf("月档应保留 %s", id)
		}
	}
	if !delSet["D2026-05-31"] {
		t.Error("第 7 个月不在月档内")
	}
	// 年：2026 年最新（H11）与 2025 年最后一天
	if delSet["D2025-12-31"] {
		t.Error("年档应保留 2025-12-31")
	}
	if len(keep) < 15 || len(keep) > 25 {
		t.Fatalf("保留了 %d 份：%v", len(keep), keep)
	}
	if err := (&Retention{Mode: "gfs"}).Validate(); err == nil {
		t.Error("GFS 全为 0 应报错")
	}
	if err := (&Retention{Mode: "count"}).Validate(); err == nil {
		t.Error("count 为 0 应报错")
	}
}

const sampleMysqldump = "-- MySQL dump 10.13\n" +
	"/*!40101 SET NAMES utf8mb4 */;\n" +
	"--\n-- Current Database: `app`\n--\n\n" +
	"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `app` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n\n" +
	"USE `app`;\n" +
	"DROP TABLE IF EXISTS `users`;\n" +
	"CREATE TABLE `users` (\n  `id` int NOT NULL,\n  `name` varchar(50) DEFAULT NULL\n) ENGINE=InnoDB;\n" +
	"LOCK TABLES `users` WRITE;\n" +
	"INSERT INTO `users` VALUES (1,'a(b'),(2,'it\\'s (x)'),(3,'\\\\'),(4,NULL),(5,0x28292C);\n" +
	"INSERT INTO `users` VALUES (6,'\"(\"');\n" +
	"UNLOCK TABLES;\n" +
	"CREATE TABLE `we``ird` (\n  `x` int\n);\n" +
	"INSERT INTO `we``ird` VALUES (1),(2);\n" +
	"CREATE TABLE `empty` (`x` int);\n" +
	"--\n-- Current Database: `b2`\n--\n\n" +
	"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `b2`;\n\nUSE `b2`;\n" +
	"CREATE TABLE `t` (`x` int);\n" +
	"INSERT INTO `t` VALUES (1);\n" +
	"-- Dump completed\n"

func TestDumpObserver(t *testing.T) {
	// 任意切块写入结果都一样
	for _, chunk := range []int{1, 3, 7, 64, 1 << 20} {
		o := &dumpObserver{}
		data := []byte(sampleMysqldump)
		for i := 0; i < len(data); i += chunk {
			end := min(i+chunk, len(data))
			o.Write(data[i:end])
		}
		m := o.manifest()
		got := map[string]int64{}
		for _, d := range m {
			for _, tb := range d.Tables {
				got[d.Name+"."+tb.Name] = tb.Rows
			}
		}
		want := map[string]int64{"app.users": 6, "app.we`ird": 2, "app.empty": 0, "b2.t": 1}
		b1, _ := json.Marshal(got)
		b2, _ := json.Marshal(want)
		if string(b1) != string(b2) {
			t.Fatalf("块大小 %d：%s，应为 %s", chunk, b1, b2)
		}
	}
}

func TestDBRenamer(t *testing.T) {
	m := map[string]string{"app": "app_restore", "b2": "b`2"}
	out, err := io.ReadAll(newDBRenamer(strings.NewReader(sampleMysqldump), m))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `app_restore` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;", "USE `app_restore`;", "CREATE DATABASE /*!32312 IF NOT EXISTS*/ `b``2`;", "USE `b``2`;"} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %s", want)
		}
	}
	// 其余内容原样
	if strings.ReplaceAll(strings.ReplaceAll(s, "app_restore", "app"), "b``2", "b2") != sampleMysqldump {
		t.Fatal("改写了不该改的内容")
	}
	// 超长行（大于缓冲区）原样通过
	long := "INSERT INTO `t` VALUES ('" + strings.Repeat("x", 3<<20) + "');\nUSE `app`;\n"
	out, _ = io.ReadAll(newDBRenamer(strings.NewReader(long), m))
	if string(out) != strings.Replace(long, "USE `app`", "USE `app_restore`", 1) {
		t.Fatal("超长行处理不对")
	}
}

func TestBackupFileName(t *testing.T) {
	at := time.Date(2026, 11, 26, 8, 30, 5, 0, time.FixedZone("CST", 8*3600))
	cases := []struct {
		conn, driver string
		dbs          []string
		method, comp string
		enc          bool
		want         string
	}{
		{"生产库", "mysql", []string{"app"}, MethodMysqldump, "zstd", true, "生产库-app-20261126-003005.sql.zst.lemienc"},
		{"a/b:c", "mysql", nil, MethodLemi, "gzip", false, "a_b_c-all-20261126-003005.sql.gz"},
		{"x", "mysql", []string{"a", "b"}, MethodLemi, "none", false, "x-multi-20261126-003005.sql"},
		{"本地", "sqlite", nil, MethodSQLiteFile, "zstd", false, "本地-main-20261126-003005.db.zst"},
		{"..", "sqlite", nil, MethodLemi, "none", false, "db-main-20261126-003005.sql"},
	}
	for _, c := range cases {
		if got := backupFileName(c.conn, c.driver, c.dbs, c.method, c.comp, c.enc, at); got != c.want {
			t.Errorf("%s：%s，应为 %s", c.conn, got, c.want)
		}
	}
	if plainName("x-app-1.sql.zst.lemienc") != "x-app-1.sql" || plainName("a.db.gz") != "a.db" {
		t.Fatal("plainName")
	}
}

// 计划到期、失败后按 1/5/15 分钟重试 3 次、成功后清零。
func TestScheduleAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := f.sqliteDB(t, "a.db", true)
	tg := f.localTarget(t, "nas")
	clock := time.Date(2026, 11, 26, 10, 0, 0, 0, time.Local)
	f.s.now = func() time.Time { return clock }
	p, err := f.s.CreatePlan(ctx, f.ws, f.owner.ID, &PlanInput{ConnectionID: src.ID, Name: "每小时", Method: MethodLemi, Targets: []string{tg.ID},
		Schedule: sched.Spec{Kind: "hourly", Minute: 15}, Retention: Retention{Mode: "count", Count: 2}, NotifyFailure: []string{"ch"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 26, 10, 15, 0, 0, time.Local).UnixMilli(); p.NextRunAt != want {
		t.Fatalf("下次运行：%d，应为 %d", p.NextRunAt, want)
	}
	f.s.Tick(ctx) // 还没到点
	f.wait(t)
	if runs, _, _ := f.s.ListRuns(ctx, RunQuery{WorkspaceID: f.ws}); len(runs) != 0 {
		t.Fatal("没到点不该运行")
	}
	// 让目标失效：之后的运行都会失败
	var cfg map[string]any
	json.Unmarshal([]byte(tg.Config), &cfg)
	os.RemoveAll(cfg["path"].(string))
	clock = clock.Add(16 * time.Minute)
	f.s.Tick(ctx)
	f.wait(t)
	delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	for i, d := range delays {
		p, _ = f.s.GetPlan(ctx, p.ID)
		if p.RetryAttempt != i+1 || p.RetryAt != clock.Add(d).UnixMilli() {
			t.Fatalf("第 %d 次失败后：retry_attempt=%d retry_at=%d", i+1, p.RetryAttempt, p.RetryAt)
		}
		clock = clock.Add(d)
		f.s.Tick(ctx)
		f.wait(t)
	}
	p, _ = f.s.GetPlan(ctx, p.ID)
	if p.RetryAttempt != 0 || p.RetryAt != 0 {
		t.Fatalf("重试 3 次后应停止：%+v", p)
	}
	runs, _, _ := f.s.ListRuns(ctx, RunQuery{WorkspaceID: f.ws})
	if len(runs) != 4 {
		t.Fatalf("应有 4 次运行（1 次计划 + 3 次重试），实际 %d", len(runs))
	}
	triggers := []string{}
	for _, r := range runs {
		triggers = append(triggers, r.Trigger)
		if r.Status != StatusFailed {
			t.Fatalf("应全部失败：%s", r.Status)
		}
	}
	if strings.Join(triggers, ",") != "retry,retry,retry,schedule" {
		t.Fatalf("触发方式：%v", triggers)
	}
	if n := len(f.nt.kinds()); n != 4 {
		t.Fatalf("每次失败都应通知，实际 %d 次", n)
	}
	// 恢复目标，下一个整点成功；保留策略只留 2 份成功的
	os.MkdirAll(cfg["path"].(string), 0o700)
	for i := 0; i < 3; i++ {
		p, _ = f.s.GetPlan(ctx, p.ID)
		clock = time.UnixMilli(p.NextRunAt).Add(2 * time.Hour) // 跨过宽限期
		f.s.Tick(ctx)
		f.wait(t)
	}
	ok, _, _ := f.s.ListRuns(ctx, RunQuery{WorkspaceID: f.ws, Status: StatusSuccess})
	if len(ok) != 2 {
		t.Fatalf("保留策略后应剩 2 份成功的备份，实际 %d", len(ok))
	}
	n := 0
	filepath.Walk(cfg["path"].(string), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	if n != 4 { // 2 份备份 + 2 份清单
		t.Fatalf("目标上应剩 4 个文件，实际 %d", n)
	}
}
