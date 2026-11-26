package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
	"github.com/LemonITCN/lemi/lemi-service/internal/sched"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
)

// MySQL 端到端：
//
//	LEMI_TEST_MYSQL_ADDR=127.0.0.1:3306 LEMI_TEST_MYSQL_PASSWORD=... \
//	LEMI_TEST_TOOLS_PKG=<mysql-tools 工作流产出的 mysql-client-*.tar.gz>   或
//	LEMI_TEST_MYSQLDUMP=<mysqldump 路径> LEMI_TEST_MYSQL_CLIENT=<mysql 路径> \
//	go test ./internal/backup -run MySQL
func mysqlSetup(t *testing.T) (*fixture, *conns.Connection, *sql.DB) {
	addr := os.Getenv("LEMI_TEST_MYSQL_ADDR")
	if addr == "" {
		t.Skip("未设置 LEMI_TEST_MYSQL_ADDR")
	}
	f := setup(t)
	ctx := context.Background()
	host, port, _ := net.SplitHostPort(addr)
	pn, _ := strconv.Atoi(port)
	root := os.Getenv("LEMI_TEST_MYSQL_PASSWORD")
	admin, err := sql.Open("mysql", "root:"+root+"@tcp("+addr+")/?multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	// 专用账号，口令里带选项文件需要转义的字符
	pw := `pw"with'quotes\#and;semi`
	for _, q := range []string{
		"DROP USER IF EXISTS 'lemi_bk'@'%'",
		"CREATE USER 'lemi_bk'@'%' IDENTIFIED BY " + mysqlQuote(pw),
		"GRANT ALL PRIVILEGES ON *.* TO 'lemi_bk'@'%'",
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	c, err := f.cs.Create(ctx, f.ws, f.owner.ID, &conns.Input{Name: "mysql测试", Driver: "mysql",
		Params: map[string]any{"host": host, "port": pn, "user": "lemi_bk", "password": pw, "tls_mode": "preferred"}})
	if err != nil {
		t.Fatal(err)
	}
	// 客户端工具
	if pkg := os.Getenv("LEMI_TEST_TOOLS_PKG"); pkg != "" {
		b, err := os.ReadFile(pkg)
		if err != nil {
			t.Fatal(err)
		}
		platform, _ := dumptools.Platform()
		name := filepath.Base(pkg)
		ver := strings.TrimSuffix(strings.TrimPrefix(name, "mysql-client-"), "-"+platform+".tar.gz")
		sum := sha256.Sum256(b)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
		t.Cleanup(srv.Close)
		m := dumptools.NewManager(filepath.Join(f.dir, "tools"), srv.URL)
		series := strings.Join(strings.Split(ver, ".")[:2], ".")
		m.Manifest = &dumptools.Manifest{Versions: []dumptools.Version{{Version: ver, Series: series, Tag: "tools-mysql-" + ver,
			Files: map[string]dumptools.File{platform: {Name: name, SHA256: hex.EncodeToString(sum[:])}}}}}
		f.s.Tools = m
	} else if dump := os.Getenv("LEMI_TEST_MYSQLDUMP"); dump != "" {
		m := dumptools.NewManager(filepath.Join(f.dir, "tools"), "http://127.0.0.1:1")
		custom := dumptools.Custom{Mysqldump: dump, Mysql: os.Getenv("LEMI_TEST_MYSQL_CLIENT")}
		m.Custom = func() dumptools.Custom { return custom }
		f.s.Tools = m
	}
	return f, c, admin
}

func mysqlQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func seedMySQL(t *testing.T, admin *sql.DB, db string) {
	t.Helper()
	stmts := []string{
		"DROP DATABASE IF EXISTS `" + db + "`",
		"CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4",
		"USE `" + db + "`",
		"CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, name VARCHAR(100) NOT NULL, note TEXT, avatar BLOB, amount DECIMAL(20,6), created DATETIME(3), meta JSON)",
		"CREATE TABLE `odd``name` (x INT)",
		"CREATE TABLE empty_t (x INT)",
		"CREATE VIEW v_users AS SELECT id, name FROM users",
		"CREATE TABLE audit_t (id INT AUTO_INCREMENT PRIMARY KEY, uid BIGINT)",
		"CREATE TRIGGER trg AFTER INSERT ON users FOR EACH ROW INSERT INTO audit_t (uid) VALUES (NEW.id)",
		"CREATE PROCEDURE p_count() BEGIN SELECT COUNT(*) FROM users; END",
	}
	for _, q := range stmts {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	tx, _ := admin.Begin()
	tx.Exec("USE `" + db + "`")
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	for i := 0; i < 300; i++ {
		_, err := tx.Exec("INSERT INTO users (name, note, avatar, amount, created, meta) VALUES (?, ?, ?, ?, ?, ?)",
			"用户 (x) 'q' \"d\" "+strconv.Itoa(i), "多行\n文本\t制表 \\ 反斜杠 \x1a 😀", all[:i%257], "12345678901234.123456", "2026-11-26 12:00:00.123", `{"k": [1, "二"]}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	tx.Exec("INSERT INTO `odd``name` VALUES (1),(2),(3)")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func checksums(t *testing.T, admin *sql.DB, db string) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	out := map[string]string{}
	for _, tb := range []string{"users", "odd`name", "empty_t", "audit_t"} {
		n, h, err := sqldump.TableChecksum(ctx, conn, sqldump.MySQL, db, tb)
		if err != nil {
			t.Fatalf("%s.%s：%v", db, tb, err)
		}
		out[tb] = strconv.FormatInt(n, 10) + "/" + h
	}
	var v, p, tr int
	admin.QueryRow("SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?", db).Scan(&v)
	admin.QueryRow("SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?", db).Scan(&p)
	admin.QueryRow("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?", db).Scan(&tr)
	out["objects"] = strconv.Itoa(v) + "/" + strconv.Itoa(p) + "/" + strconv.Itoa(tr)
	return out
}

func eqMap(a, b map[string]string) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestMySQLBackupRestoreVerify(t *testing.T) {
	f, c, admin := mysqlSetup(t)
	ctx := context.Background()
	seedMySQL(t, admin, "lemi_bk_src")
	t.Cleanup(func() {
		for _, d := range []string{"lemi_bk_src", "lemi_bk_dst", "lemi_bk_dst2"} {
			admin.Exec("DROP DATABASE IF EXISTS `" + d + "`")
		}
	})
	want := checksums(t, admin, "lemi_bk_src")
	tg := f.localTarget(t, "nas")
	if err := f.s.SetWSSettings(ctx, f.ws, WSSettings{VerifyConnectionID: c.ID}); err != nil {
		t.Fatal(err)
	}
	methods := []string{MethodLemi}
	if f.s.Tools != nil {
		methods = append([]string{MethodMysqldump}, methods...)
	} else {
		t.Log("没有提供 mysqldump（LEMI_TEST_TOOLS_PKG 或 LEMI_TEST_MYSQLDUMP），只测自研导出器")
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			p, err := f.s.CreatePlan(ctx, f.ws, f.owner.ID, &PlanInput{ConnectionID: c.ID, Name: "mysql-" + method, Method: method,
				Databases: []string{"lemi_bk_src"}, Encrypt: true, Passphrase: "一个足够长的备份密码-123", Targets: []string{tg.ID},
				Schedule: sched.Spec{Kind: "manual"}, Verify: VerifyConfig{AfterBackup: true}})
			if err != nil {
				t.Fatal(err)
			}
			r := f.runBackup(t, p)
			if r.Status != StatusSuccess {
				t.Fatalf("备份失败：%s", r.Error)
			}
			f.wait(t) // 备份后校验
			r, _ = f.s.GetRun(ctx, r.ID)
			var man BackupManifest
			json.Unmarshal([]byte(r.Manifest), &man)
			rows := map[string]int64{}
			for _, d := range man.Databases {
				for _, tb := range d.Tables {
					rows[d.Name+"."+tb.Name] = tb.Rows
				}
			}
			if rows["lemi_bk_src.users"] != 300 || rows["lemi_bk_src.odd`name"] != 3 || rows["lemi_bk_src.audit_t"] != 300 {
				t.Fatalf("清单行数：%v（工具 %s）", rows, r.Tool)
			}
			if r.VerifyStatus != "ok" {
				t.Fatalf("还原校验失败：%s", r.VerifyDetail)
			}
			var vr VerifyResult
			json.Unmarshal([]byte(r.VerifyDetail), &vr)
			if !vr.TempDBDropped || vr.TablesChecked < 4 || (method == MethodLemi && !vr.HashChecked) {
				t.Fatalf("校验详情：%+v", vr)
			}
			var leftover int
			admin.QueryRow("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'lemi\\_verify\\_%'").Scan(&leftover)
			if leftover != 0 {
				t.Fatal("临时校验库没有删除")
			}
			// 还原到新库名
			dst := map[string]string{MethodMysqldump: "lemi_bk_dst", MethodLemi: "lemi_bk_dst2"}[method]
			in := &RestoreInput{ConnectionID: c.ID, DatabaseMap: map[string]string{"lemi_bk_src": dst}}
			rec, err := f.s.StartRestore(ctx, r, c, in, f.owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.wait(t)
			rec, _ = f.s.GetRestore(ctx, rec.ID)
			if rec.Status != StatusSuccess {
				t.Fatalf("还原失败：%s", rec.Error)
			}
			wantMethod := RestoreLemi
			if method == MethodMysqldump {
				wantMethod = RestoreMySQLClient
			}
			if rec.Method != wantMethod {
				t.Fatalf("还原方式 %s，应为 %s", rec.Method, wantMethod)
			}
			if got := checksums(t, admin, dst); !eqMap(got, want) {
				t.Fatalf("还原后不一致：\n%v\n%v", got, want)
			}
			// 目标库已非空：不确认被拒，确认后覆盖
			if _, err := f.s.StartRestore(ctx, r, c, in, f.owner.ID); err == nil {
				t.Fatal("非空目标库没有确认应被拒绝")
			}
			in.Confirm = map[string]string{dst: dst}
			rec, err = f.s.StartRestore(ctx, r, c, in, f.owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.wait(t)
			rec, _ = f.s.GetRestore(ctx, rec.ID)
			if rec.Status != StatusSuccess {
				t.Fatalf("覆盖还原失败：%s", rec.Error)
			}
			if got := checksums(t, admin, dst); !eqMap(got, want) {
				t.Fatalf("覆盖还原后不一致：\n%v\n%v", got, want)
			}
			// 下载为 .sql
			job, err := f.s.DownloadJob(ctx, r, f.owner.ID, "sql", "")
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			if err := job.Write(ctx, &sb); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sb.String(), "lemi_bk_src") || !strings.HasSuffix(job.Filename, ".sql") {
				t.Fatalf("下载为 SQL：%s", job.Filename)
			}
		})
	}
}
