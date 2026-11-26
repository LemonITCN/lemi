package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeHealth struct{}

func (fakeHealth) Status(ctx context.Context, id string) (string, int64, int64) {
	return "up", 1764148800000, 12
}

func TestAITools(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.s.Health = fakeHealth{}
	src := f.sqliteDB(t, "a.db", true)
	other := f.sqliteDB(t, "b.db", true)
	tg := f.localTarget(t, "nas")
	p := f.plan(t, src, MethodSQLiteFile, true, tg)
	f.plan(t, other, MethodLemi, false, tg)
	if r := f.runBackup(t, p); r.Status != StatusSuccess {
		t.Fatal(r.Error)
	}
	f.st.Exec(ctx, `INSERT INTO health_checks (connection_id, at, ok, latency_ms, error, status, manual) VALUES (?,?,?,?,?,?,0)`, src.ID, f.s.now().UnixMilli(), 0, 0, "连接超时", "down")
	f.st.Exec(ctx, `INSERT INTO health_checks (connection_id, at, ok, latency_ms, error, status, manual) VALUES (?,?,?,?,?,?,0)`, src.ID, f.s.now().UnixMilli()-1000, 1, 5, "", "up")
	dump := func(v any, err error) string {
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	// 只给 src：看不到 other 的计划与备份
	s := dump(f.s.AIListBackups(ctx, f.owner, f.ws, []string{src.ID}, 10))
	if !strings.Contains(s, `"status":"success"`) || !strings.Contains(s, src.Name) {
		t.Fatalf("list_backups：%s", s)
	}
	s = dump(f.s.AIBackupStatus(ctx, f.owner, f.ws, []string{src.ID}))
	if !strings.Contains(s, `"plan":"计划-sqlite_file"`) || strings.Contains(s, other.Name) || !strings.Contains(s, "last_success") || strings.Contains(s, "备份密码") {
		t.Fatalf("backup_status：%s", s)
	}
	s = dump(f.s.AIHealthStatus(ctx, f.owner, f.ws, []string{src.ID}))
	if !strings.Contains(s, `"status":"up"`) || !strings.Contains(s, `"uptime_24h":50`) || !strings.Contains(s, "连接超时") {
		t.Fatalf("health_status：%s", s)
	}
	// 别的空间的连接 ID 不出现
	ws2, _ := f.acc.CreateWorkspace(ctx, "另一个", "", f.owner.ID)
	if s := dump(f.s.AIBackupStatus(ctx, f.owner, ws2.ID, []string{src.ID})); strings.Contains(s, src.Name) {
		t.Fatalf("跨空间：%s", s)
	}
	if s := dump(f.s.AIListBackups(ctx, f.owner, ws2.ID, []string{src.ID}, 10)); strings.Contains(s, src.Name) {
		t.Fatalf("跨空间：%s", s)
	}
}
