package sqldump

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildMyFixture 在库 db 里建全套类型夹具。按服务器版本跳过不支持的特性。
func buildMyFixture(t testing.TB, s *myServer, c *sql.Conn, db string) {
	v := s.ver
	maria := v.mariadb
	my8 := !maria && v.atLeast(8, 0, 0)
	myExec(t, c, "USE "+quoteMySQLIdent(db), "SET SESSION sql_mode = ''", "SET SESSION time_zone = '+08:00'", "SET NAMES utf8mb4")

	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	hexAll := fmt.Sprintf("0x%X", all)
	rnd := rand.New(rand.NewSource(42))
	big := make([]byte, 1<<20)
	rnd.Read(big)

	stmts := []string{
		`CREATE TABLE ints (
			id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			ti TINYINT, tiu TINYINT UNSIGNED, si SMALLINT, siu SMALLINT UNSIGNED, mi MEDIUMINT, miu MEDIUMINT UNSIGNED,
			i INT, iu INT UNSIGNED, bi BIGINT, biu BIGINT UNSIGNED, zf INT(8) UNSIGNED ZEROFILL, b1 TINYINT(1)
		) ENGINE=InnoDB`,
		`INSERT INTO ints VALUES (1, -128, 0, -32768, 0, -8388608, 0, -2147483648, 0, -9223372036854775808, 0, 0, 0),
			(2, 127, 255, 32767, 65535, 8388607, 16777215, 2147483647, 4294967295, 9223372036854775807, 18446744073709551615, 42, 1),
			(3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,

		`CREATE TABLE nums (id INT PRIMARY KEY, d65 DECIMAL(65,30), d10 DECIMAL(10,0), du DECIMAL(5,2) UNSIGNED,
			f FLOAT, d DOUBLE, fu FLOAT UNSIGNED, fmd FLOAT(7,2), dmd DOUBLE(12,4))`,
		`INSERT INTO nums VALUES
			(1, 99999999999999999999999999999999999.999999999999999999999999999999, 9999999999, 999.99, 1.2345678e0, 0.1e0+0.2e0, 0.1e0, 12345.67, 12345678.1234),
			(2, -99999999999999999999999999999999999.999999999999999999999999999999, -9999999999, 0, -0e0, -0e0, 0, -12345.67, -0.0001),
			(3, 0.000000000000000000000000000001, 0, 0.01, 3.4028234663852886e38, 1.7976931348623157e308, 1.401298464324817e-45, 0, 0),
			(4, 1.5, 1, 1.5, -1.17549435e-38, 5e-324, 3.0e-39, 1.5, 1.5),
			(5, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,

		`CREATE TABLE bits (id INT PRIMARY KEY, b1 BIT(1), b7 BIT(7), b8 BIT(8), b17 BIT(17), b64 BIT(64))`,
		`INSERT INTO bits VALUES (1, b'1', b'1111111', b'10000000', b'10000000000000001', b'1111111111111111111111111111111111111111111111111111111111111111'),
			(2, b'0', b'0', b'0', b'0', b'0'), (3, NULL, NULL, NULL, NULL, NULL), (4, 1, 5, 255, 65537, 9223372036854775808)`,

		`CREATE TABLE strs (id INT PRIMARY KEY, c CHAR(20), vc VARCHAR(255), tt TINYTEXT, t TEXT, mt MEDIUMTEXT, lt LONGTEXT,
			l1 VARCHAR(100) CHARACTER SET latin1, g VARCHAR(100) CHARACTER SET gbk, u VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
			e ENUM('a','b','c d','it''s'), s SET('x','y','z')) DEFAULT CHARSET=utf8mb4`,
		`INSERT INTO strs VALUES
			(1, 'abc', 'nul\0inside', 'back\\slash', 'quote '' and "', 'line1\nline2\r\nline3\ttab', 'ctrl-z \Z end \b',
				'café ÿ ñ', '中文汉字', 'emoji 😀 🚀 日本語 한국어 العربية', 'c d', 'x,z'),
			(2, '', '', '', '', '', '', '', '', '', 'it''s', ''),
			(3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
			(4, 'semi;colon', '-- not a comment', '# nor this', '/* nor */ this', ';;\ndelimiter ;\n', 'DELIMITER //', 'x', 'y', 'z', 'a', 'x,y,z')`,
		"INSERT INTO strs (id, lt) VALUES (5, REPEAT('长文本 long text 😀 ', 20000))",

		`CREATE TABLE bins (id INT PRIMARY KEY, b BINARY(16), vb VARBINARY(300), tb TINYBLOB, bl BLOB, mb MEDIUMBLOB, lb LONGBLOB)`,
		"INSERT INTO bins VALUES (1, 0x00FF, " + hexAll + ", 0x00, " + hexAll + ", " + hexAll + ", " + hexAll + "), (2, '', '', '', '', '', ''), (3, NULL, NULL, NULL, NULL, NULL, NULL)",
		fmt.Sprintf("INSERT INTO bins (id, lb) VALUES (4, 0x%X)", big),

		`CREATE TABLE times (id INT PRIMARY KEY, d DATE, dt DATETIME(6), ts TIMESTAMP(6) NULL DEFAULT NULL, tm TIME(6), y YEAR,
			dt0 DATETIME, ts0 TIMESTAMP NULL DEFAULT NULL, tm0 TIME)`,
		`INSERT INTO times VALUES
			(1, '1000-01-01', '1000-01-01 00:00:00.000000', '1970-01-01 08:00:01.000000', '-838:59:59.000000', 1901, '1000-01-01 00:00:00', '1970-01-01 08:00:01', '-838:59:59'),
			(2, '9999-12-31', '9999-12-31 23:59:59.999999', '2038-01-19 11:14:07.999999', '838:59:59.000000', 2155, '9999-12-31 23:59:59', '2038-01-19 11:14:07', '838:59:59'),
			(3, '0000-00-00', '0000-00-00 00:00:00', '0000-00-00 00:00:00', '00:00:00', 0, '0000-00-00 00:00:00', '0000-00-00 00:00:00', '00:00:00'),
			(4, '2026-02-03', '2026-02-03 04:05:06.123456', '2026-02-03 04:05:06.654321', '-01:02:03.500000', 2026, '2026-02-03 04:05:06', '2026-03-29 02:30:00', '100:00:00'),
			(5, '2026-00-15', '2026-11-00 00:00:00', NULL, NULL, NULL, NULL, NULL, NULL),
			(6, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,

		"CREATE TABLE `select` (`from` INT PRIMARY KEY, `a``b` VARCHAR(10) COMMENT 'it''s a \"comment\" -- # */ \\n end', `order` INT, `名字` VARCHAR(10)) COMMENT='表注释 it''s */ -- x'",
		"INSERT INTO `select` VALUES (1, 'x', 2, '值'), (2, NULL, NULL, NULL)",

		`CREATE TABLE fk_a (id INT PRIMARY KEY, b_id INT, KEY (b_id)) ENGINE=InnoDB`,
		`CREATE TABLE fk_b (id INT PRIMARY KEY, a_id INT, KEY (a_id)) ENGINE=InnoDB`,
		`ALTER TABLE fk_a ADD CONSTRAINT fk_a_b FOREIGN KEY (b_id) REFERENCES fk_b(id) ON DELETE CASCADE`,
		`ALTER TABLE fk_b ADD CONSTRAINT fk_b_a FOREIGN KEY (a_id) REFERENCES fk_a(id) ON UPDATE SET NULL`,
		`SET FOREIGN_KEY_CHECKS = 0`,
		`INSERT INTO fk_a VALUES (1, 1), (2, 2)`,
		`INSERT INTO fk_b VALUES (1, 1), (2, 1)`,
		`SET FOREIGN_KEY_CHECKS = 1`,

		`CREATE TABLE parted (id INT NOT NULL, d DATE NOT NULL, PRIMARY KEY (id, d)) PARTITION BY RANGE (YEAR(d)) (PARTITION p0 VALUES LESS THAN (2000), PARTITION p1 VALUES LESS THAN MAXVALUE)`,
		`INSERT INTO parted VALUES (1, '1999-01-01'), (2, '2026-01-01')`,

		`CREATE TABLE gen (id INT PRIMARY KEY, a INT, b INT AS (a * 2) VIRTUAL, c VARCHAR(20) AS (CONCAT('x', a)) STORED, KEY (c))`,
		`INSERT INTO gen (id, a) VALUES (1, 1), (2, NULL), (3, -5)`,

		`CREATE TABLE myisam_t (id INT PRIMARY KEY, v VARCHAR(10)) ENGINE=MyISAM`,
		`INSERT INTO myisam_t VALUES (1, 'a')`,

		`CREATE TABLE autozero (id INT AUTO_INCREMENT PRIMARY KEY, v VARCHAR(10)) AUTO_INCREMENT=100`,
		`SET SESSION sql_mode = 'NO_AUTO_VALUE_ON_ZERO'`,
		`INSERT INTO autozero VALUES (0, 'zero'), (1, 'one'), (50, 'fifty')`,
		`SET SESSION sql_mode = ''`,

		`CREATE TABLE empty_t (id INT PRIMARY KEY)`,
		`CREATE TABLE log (id INT AUTO_INCREMENT PRIMARY KEY, msg VARCHAR(200))`,
		`CREATE TABLE nopk (a INT, b VARCHAR(10))`,
		`INSERT INTO nopk VALUES (1, 'dup'), (1, 'dup'), (NULL, NULL)`,

		"CREATE TABLE geo (id INT PRIMARY KEY, g GEOMETRY, p POINT, pg POLYGON" + map[bool]string{true: ", p4326 POINT SRID 4326", false: ", p4326 POINT"}[my8] + ")",
		`INSERT INTO geo VALUES (1, ST_GeomFromText('POINT(1.5 -2.25)'), ST_GeomFromText('POINT(0 0)'),
			ST_GeomFromText('POLYGON((0 0,10 0,10 10,0 10,0 0),(1 1,2 1,2 2,1 1))'), ST_GeomFromText('POINT(10 20)', 4326)),
			(2, ST_GeomFromText('LINESTRING(0 0,1 1,2 3)', 3857), NULL, NULL, NULL),
			(3, ST_GeomFromText('MULTIPOLYGON(((0 0,1 0,1 1,0 0)))', 0), ST_GeomFromText('POINT(-180 90)'), NULL, ST_GeomFromText('POINT(-45.5 170.25)', 4326))`,
	}
	if !maria && !v.atLeast(5, 7, 8) {
		t.Fatalf("测试需要 MySQL 5.7.8+ 或 MariaDB")
	}
	stmts = append(stmts,
		`CREATE TABLE jsons (id INT PRIMARY KEY, j JSON)`,
		`INSERT INTO jsons VALUES (1, '{"a": [1, 2, {"b": "中文😀"}], "c": null, "d": 1.5e300, "e": "\\u0000 \\\\ \\"", "f": -0.0}'),
			(2, '[]'), (3, 'null'), (4, '"str"'), (5, NULL), (6, '{"k": 12345678901234567890}')`,
	)
	if my8 && v.atLeast(8, 0, 23) || maria && v.atLeast(10, 3, 3) {
		stmts = append(stmts, `CREATE TABLE invis (id INT PRIMARY KEY, a INT, h INT INVISIBLE DEFAULT 7)`,
			`INSERT INTO invis (id, a, h) VALUES (1, 1, 100), (2, 2, NULL)`, `INSERT INTO invis (id, a) VALUES (3, 3)`)
	}
	if my8 && v.atLeast(8, 0, 16) || maria {
		stmts = append(stmts, `CREATE TABLE checked (id INT PRIMARY KEY, a INT, CONSTRAINT chk_a CHECK (a > 0))`, `INSERT INTO checked VALUES (1, 5)`)
	}
	if my8 && v.atLeast(8, 0, 13) {
		stmts = append(stmts,
			`CREATE TABLE defaults (id INT PRIMARY KEY AUTO_INCREMENT, created DATETIME DEFAULT CURRENT_TIMESTAMP, u VARCHAR(36) DEFAULT (UUID()), j JSON DEFAULT (JSON_ARRAY()), n INT DEFAULT (1 + 1))`,
			`INSERT INTO defaults (id) VALUES (1), (2)`,
			`CREATE TABLE funcidx (id INT PRIMARY KEY, s VARCHAR(50), KEY idx_lower ((LOWER(s))))`,
			`INSERT INTO funcidx VALUES (1, 'ABC')`)
	}
	if maria {
		stmts = append(stmts,
			`CREATE TABLE defaults (id INT PRIMARY KEY AUTO_INCREMENT, created DATETIME DEFAULT CURRENT_TIMESTAMP, u VARCHAR(36) DEFAULT (UUID()), n INT DEFAULT (1 + 1))`,
			`INSERT INTO defaults (id) VALUES (1), (2)`,
			`CREATE SEQUENCE seq1 START WITH 100 INCREMENT BY 5`,
			`SELECT NEXTVAL(seq1)`)
		if v.atLeast(10, 7, 0) {
			stmts = append(stmts, `CREATE TABLE mtypes (id INT PRIMARY KEY, ip INET6, u UUID)`,
				`INSERT INTO mtypes VALUES (1, '::1', '123e4567-e89b-12d3-a456-426655440000'), (2, '2001:db8::ff00:42:8329', NULL)`)
		}
	}
	// 例程、视图、触发器、事件
	stmts = append(stmts,
		`CREATE FUNCTION f_double(x INT) RETURNS INT DETERMINISTIC RETURN x * 2`,
		`SET SESSION sql_mode = 'PIPES_AS_CONCAT,STRICT_ALL_TABLES'`,
		`CREATE PROCEDURE p_tricky(IN a INT, OUT b VARCHAR(100))
BEGIN
  -- don't break; delimiter ;;
  DECLARE s VARCHAR(100) DEFAULT 'semi;colon';
  /* block; comment */
  SET b = s || ';;' || a;
  INSERT INTO log(msg) VALUES (CONCAT('called ', a));
END`,
		`SET SESSION sql_mode = ''`,
		// v_a 依赖 v_b（名字排在前面），v_b 调用存储函数：验证拓扑排序与「函数先于视图」
		`CREATE VIEW v_b AS SELECT f_double(id) AS dd, id FROM ints`,
		`CREATE VIEW v_a AS SELECT dd FROM v_b WHERE id > 0`,
		`CREATE SQL SECURITY INVOKER VIEW v_strs AS SELECT id, CONCAT(vc, ';', 'x') AS cc FROM strs WHERE vc <> 'it''s'`,
		`CREATE TRIGGER trg_ins BEFORE INSERT ON strs FOR EACH ROW BEGIN
  IF NEW.vc = 'x' THEN
    SET NEW.vc = 'x;;y'; -- comment; with semicolons
  END IF;
END`,
		`CREATE TRIGGER trg_log AFTER INSERT ON ints FOR EACH ROW INSERT INTO log(msg) VALUES ('first')`,
		`CREATE TRIGGER trg_log2 AFTER INSERT ON ints FOR EACH ROW FOLLOWS trg_log INSERT INTO log(msg) VALUES ('second')`,
		`SET SESSION time_zone = '+08:00'`,
		`CREATE EVENT ev_daily ON SCHEDULE EVERY 1 DAY STARTS '2030-01-01 08:00:00' ON COMPLETION PRESERVE DISABLE DO DELETE FROM log WHERE id < 0`,
		`SET SESSION time_zone = '+00:00'`,
		`CREATE EVENT ev_once ON SCHEDULE AT '2035-01-01 00:00:00' ON COMPLETION PRESERVE DISABLE DO BEGIN INSERT INTO log(msg) VALUES ('a;b'); INSERT INTO log(msg) VALUES ('c'); END`,
	)
	myExec(t, c, stmts...)
}

// 往返：导出 → 导入新库 → 全面比对；再用官方客户端导入我们的文件、用我们的导入器导入 mysqldump 的文件。
func TestMySQLMatrix(t *testing.T) {
	for _, s := range myServers(t) {
		s := s
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			admin := s.conn(t, "", false)
			var vt string
			if err := admin.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&vt); err != nil {
				t.Fatalf("连不上 %s: %v", s.name, err)
			}
			s.ver = parseMyVersion(vt)
			t.Logf("%s: %s", s.name, vt)
			src := s.newDB(t, admin, "src")
			buildMyFixture(t, s, admin, src)

			t.Run("roundtrip", func(t *testing.T) { testMyRoundTrip(t, s, src) })
			t.Run("options", func(t *testing.T) { testMyOptions(t, s, src) })
			t.Run("multidb", func(t *testing.T) { testMyMultiDB(t, s, src) })
			t.Run("hostile_session", func(t *testing.T) { testMyHostileSession(t, s, src) })
			t.Run("random", func(t *testing.T) { testMyRandom(t, s) })
			t.Run("truncated", func(t *testing.T) { testMyTruncated(t, s, src) })
			t.Run("cancel_and_kill", func(t *testing.T) { testMyCancelKill(t, s) })
			if s.container != "" {
				t.Run("official_client_imports_ours", func(t *testing.T) { testMyOfficialClient(t, s, src) })
				t.Run("we_import_mysqldump", func(t *testing.T) { testMyImportMysqldump(t, s, src) })
			}
		})
	}
}

// 大表单独跑（不与其他服务器的测试并行，内存峰值才量得准）。
func TestMySQLLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过大表测试")
	}
	for _, s := range myServers(t) {
		t.Run(s.name, func(t *testing.T) {
			admin := s.conn(t, "", false)
			var vt string
			if err := admin.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&vt); err != nil {
				t.Fatalf("连不上 %s: %v", s.name, err)
			}
			s.ver = parseMyVersion(vt)
			testMyLarge(t, s)
		})
	}
}

func testMyRoundTrip(t *testing.T, s *myServer, src string) {
	for _, ext := range []bool{false, true} {
		t.Run(fmt.Sprintf("extended=%v", ext), func(t *testing.T) {
			c := s.conn(t, src, false)
			data, man := exportMy(t, c, ExportOptions{Databases: []string{src}, ExtendedInsert: ext, MaxStatementBytes: 64 << 10, ConnectionName: "conn", HostLabel: "127.0.0.1:3306"})
			if dir := os.Getenv("LEMI_TEST_DUMP_DIR"); dir != "" { // 调试：留下导出文件
				_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-extended-%v.sql", s.name, ext)), data, 0o644)
			}
			if !bytes.HasPrefix(data, []byte("/*\n Lemi Dump SQL\n")) || !bytes.Contains(data, []byte("\nSET SQL_MODE = 'NO_AUTO_VALUE_ON_ZERO';\n")) {
				t.Fatalf("文件头不对: %.600s", data)
			}
			if !bytes.Contains(data, []byte("delimiter ;;\n")) || bytes.Contains(data, []byte("DEFINER=")) {
				t.Fatal("例程应该用 delimiter ;; 且默认去掉 DEFINER")
			}
			if len(man.Warnings) == 0 || !strings.Contains(strings.Join(man.Warnings, "\n"), "myisam_t") {
				t.Fatalf("MyISAM 表应有警告: %v", man.Warnings)
			}
			fi := bytes.Index(data, []byte("Function structure for f_double"))
			vi := bytes.Index(data, []byte("View structure for v_b"))
			va := bytes.Index(data, []byte("View structure for v_a"))
			ti := bytes.Index(data, []byte("Triggers structure for table"))
			if fi < 0 || vi < fi || va < vi || ti < va {
				t.Fatalf("对象顺序不对: 函数 %d 视图 v_b %d v_a %d 触发器 %d", fi, vi, va, ti)
			}
			// 导入会话故意设成别的时区，TIMESTAMP 不能偏移
			dstConn := s.conn(t, "", false)
			dst := s.newDB(t, dstConn, "rt")
			myExec(t, dstConn, "SET SESSION time_zone = '-05:00'")
			res := importMy(t, dstConn, data, ImportOptions{TargetDatabase: dst})
			if !res.LemiDump || !res.Complete || res.Format != "lemi" {
				t.Fatalf("%+v", res)
			}
			compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), dst, nil)
			checkMyManifest(t, s.conn(t, "", false), dst, man.Databases[0])
			checkMyManifest(t, s.conn(t, "", false), src, man.Databases[0])
			// 还原后的库再导出，内容与原文件逐字节相同
			again, _ := exportMy(t, s.conn(t, dst, false), ExportOptions{Databases: []string{dst}, ExtendedInsert: ext, MaxStatementBytes: 64 << 10, ConnectionName: "conn", HostLabel: "127.0.0.1:3306"})
			a := bytes.ReplaceAll(data, []byte(src), []byte("X"))
			b := bytes.ReplaceAll(again, []byte(dst), []byte("X"))
			if !bytes.Equal(a, b) {
				la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
				for i := 0; i < len(la) && i < len(lb); i++ {
					if !bytes.Equal(la[i], lb[i]) {
						t.Fatalf("还原后再导出的文件第 %d 行不同:\n%.500s\n%.500s", i+1, la[i], lb[i])
					}
				}
				t.Fatalf("还原后再导出的文件长度不同 %d vs %d", len(a), len(b))
			}
			checkBehavior(t, s, dst)
		})
	}
}

// checkBehavior 验证例程按原 sql_mode 重建（PIPES_AS_CONCAT 下 || 是拼接）、触发器顺序、视图可查询。
func checkBehavior(t *testing.T, s *myServer, db string) {
	c := s.conn(t, db, false)
	myExec(t, c, "DELETE FROM log", "CALL p_tricky(7, @b)")
	var b string
	if err := c.QueryRowContext(context.Background(), "SELECT @b").Scan(&b); err != nil || b != "semi;colon;;7" {
		t.Fatalf("存储过程行为变了: %q %v", b, err)
	}
	myExec(t, c, "DELETE FROM log", "INSERT INTO ints (id, ti) VALUES (1000, 1)")
	rows := queryStrings(t, c, "SELECT msg FROM log ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "first" || rows[1][0] != "second" {
		t.Fatalf("触发器顺序不对: %v", rows)
	}
	myExec(t, c, "INSERT INTO strs (id, vc) VALUES (1000, 'x')")
	var vc string
	c.QueryRowContext(context.Background(), "SELECT vc FROM strs WHERE id = 1000").Scan(&vc)
	if vc != "x;;y" {
		t.Fatalf("BEFORE INSERT 触发器没生效: %q", vc)
	}
	var n int
	if err := c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM v_a").Scan(&n); err != nil || n == 0 {
		t.Fatalf("视图查询失败: %d %v", n, err)
	}
	myExec(t, c, "DELETE FROM ints WHERE id = 1000", "DELETE FROM strs WHERE id = 1000", "DELETE FROM log")
}

func testMyOptions(t *testing.T, s *myServer, src string) {
	c := s.conn(t, src, false)
	// 只导结构
	data, man := exportMy(t, c, ExportOptions{Databases: []string{src}, NoData: true})
	if bytes.Contains(data, []byte("Records of")) || man.Databases[0].Tables[0].Hash != "" {
		t.Fatal("只导结构却有数据")
	}
	adm := s.conn(t, "", false)
	dst := s.newDB(t, adm, "opt")
	importMy(t, adm, data, ImportOptions{TargetDatabase: dst})
	// 只导数据：先删掉触发器（否则导数据时会触发）
	myExec(t, adm, "USE "+quoteMySQLIdent(dst), "DROP TRIGGER trg_ins", "DROP TRIGGER trg_log", "DROP TRIGGER trg_log2")
	data2, man2 := exportMy(t, c, ExportOptions{Databases: []string{src}, NoStructure: true, ExtendedInsert: true})
	if bytes.Contains(data2, []byte("structure for")) || bytes.Contains(data2, []byte("\nDROP ")) || bytes.Contains(data2, []byte("\ndelimiter ;;\n")) {
		t.Fatal("只导数据却有结构语句")
	}
	importMy(t, adm, data2, ImportOptions{TargetDatabase: dst})
	checkMyManifest(t, s.conn(t, "", false), dst, man2.Databases[0])
	// 选表 + 保留 DEFINER + 不写 DROP
	data3, man3 := exportMy(t, c, ExportOptions{Databases: []string{src}, Tables: []string{"ints", "v_b"}, KeepDefiner: true, NoDrop: true})
	dm := man3.Databases[0]
	if len(dm.Tables) != 1 || dm.Views != 1 || dm.Triggers != 2 || dm.Routines != 0 || dm.Events != 0 {
		t.Fatalf("选表导出不对: %+v", dm)
	}
	if !bytes.Contains(data3, []byte("DEFINER=")) || bytes.Contains(data3, []byte("DROP ")) {
		t.Fatal("KeepDefiner / NoDrop 没生效")
	}
	// 选了不存在的表要报错
	if _, err := Export(context.Background(), c, io.Discard, ExportOptions{Dialect: MySQL, Databases: []string{src}, Tables: []string{"nosuch"}}); err == nil {
		t.Fatal("不存在的表应该报错")
	}
	// 连接开了 parseTime 也要导出同样的内容
	cp := s.conn(t, src, true)
	d1, _ := exportMy(t, c, ExportOptions{Databases: []string{src}})
	d2, _ := exportMy(t, cp, ExportOptions{Databases: []string{src}})
	if !bytes.Equal(d1, d2) {
		t.Fatal("parseTime=true 的连接导出结果不同")
	}
	// 进度回调
	var calls, last atomic.Int64
	_, err := Export(context.Background(), c, io.Discard, ExportOptions{Dialect: MySQL, Databases: []string{src}, Progress: func(p Progress) {
		calls.Add(1)
		last.Store(int64(p.TablesDone*1000 + p.TablesTotal))
	}})
	if err != nil || calls.Load() == 0 || last.Load()/1000 != last.Load()%1000 {
		t.Fatalf("进度回调不对: %v %d %d", err, calls.Load(), last.Load())
	}
}

func testMyMultiDB(t *testing.T, s *myServer, src string) {
	adm := s.conn(t, "", false)
	second := s.newDB(t, adm, "second")
	myExec(t, adm, "USE "+quoteMySQLIdent(second), "CREATE TABLE t2 (id INT PRIMARY KEY, v VARCHAR(10))", "INSERT INTO t2 VALUES (1, 'two')",
		"CREATE VIEW v2 AS SELECT v FROM t2")
	data, man := exportMy(t, s.conn(t, "", false), ExportOptions{Databases: []string{src, second}})
	if len(man.Databases) != 2 || !bytes.Contains(data, []byte("CREATE DATABASE IF NOT EXISTS `"+src+"`")) {
		t.Fatal("多库导出应每库一节 CREATE DATABASE IF NOT EXISTS + USE")
	}
	r1, r2 := src+"_m1", second+"_m2"
	t.Cleanup(func() {
		c := s.conn(t, "", false)
		c.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quoteMySQLIdent(r1))
		c.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quoteMySQLIdent(r2))
	})
	importMy(t, adm, data, ImportOptions{DatabaseMap: map[string]string{src: r1, second: r2}})
	compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), r1, nil)
	compareDBs(t, s.conn(t, "", false), second, s.conn(t, "", false), r2, nil)
	checkMyManifest(t, s.conn(t, "", false), r2, man.Databases[1])
}

// 导入会话带着 ANSI_QUOTES、NO_BACKSLASH_ESCAPES 等「敌意」sql_mode，文件自己的 SET SQL_MODE 要让切分器跟上。
func testMyHostileSession(t *testing.T, s *myServer, src string) {
	data, _ := exportMy(t, s.conn(t, src, false), ExportOptions{Databases: []string{src}, ExtendedInsert: true})
	adm := s.conn(t, "", false)
	dst := s.newDB(t, adm, "hostile")
	myExec(t, adm, "SET SESSION sql_mode = 'ANSI_QUOTES,NO_BACKSLASH_ESCAPES,STRICT_ALL_TABLES'", "SET NAMES latin1", "SET SESSION time_zone = '+13:00'")
	importMy(t, adm, data, ImportOptions{TargetDatabase: dst})
	var mode string
	adm.QueryRowContext(context.Background(), "SELECT @@session.sql_mode").Scan(&mode)
	if mode != "ANSI_QUOTES,NO_BACKSLASH_ESCAPES,STRICT_ALL_TABLES" {
		t.Fatalf("导入后会话 sql_mode 没恢复: %s", mode)
	}
	compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), dst, nil)
}

func testMyOfficialClient(t *testing.T, s *myServer, src string) {
	for _, ext := range []bool{false, true} {
		data, _ := exportMy(t, s.conn(t, src, false), ExportOptions{Databases: []string{src}, ExtendedInsert: ext})
		adm := s.conn(t, "", false)
		dst := s.newDB(t, adm, "cli")
		s.dockerClient(t, dst, data)
		compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), dst, nil)
	}
}

func testMyImportMysqldump(t *testing.T, s *myServer, src string) {
	dump := s.dockerDump(t, src)
	if err := os.WriteFile(filepath.Join(t.TempDir(), "dump.sql"), dump, 0o600); err != nil {
		t.Fatal(err)
	}
	adm := s.conn(t, "", false)
	ref := s.newDB(t, adm, "mdref")
	s.dockerClient(t, ref, dump)
	ours := s.newDB(t, adm, "mdours")
	res := importMy(t, adm, dump, ImportOptions{TargetDatabase: ours})
	if res.Format != "mysqldump" || !res.Complete {
		t.Fatalf("%+v", res)
	}
	// 与官方客户端导入同一个文件的结果逐项比对（mysqldump 自己会丢 FLOAT 精度，所以不和源库比 FLOAT 列）
	compareDBs(t, s.conn(t, "", false), ref, s.conn(t, "", false), ours, nil)
	compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), ours, map[string]bool{"nums": true})
	// 截断的 mysqldump 文件必须报错
	end := bytes.LastIndex(dump, []byte("-- Dump completed"))
	cut := s.newDB(t, adm, "mdcut")
	if _, err := Import(context.Background(), adm, bytes.NewReader(dump[:end]), ImportOptions{Dialect: MySQL, TargetDatabase: cut}); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("截断的 mysqldump 文件应报 ErrIncomplete，得到 %v", err)
	}
}

func testMyTruncated(t *testing.T, s *myServer, src string) {
	data, _ := exportMy(t, s.conn(t, src, false), ExportOptions{Databases: []string{src}})
	var cuts []int
	for i := 1; i < len(data); i++ {
		if data[i] == '\n' && data[i-1] == ';' {
			cuts = append(cuts, i+1)
		}
	}
	rnd := rand.New(rand.NewSource(3))
	var pick []int
	for i := 0; i < 12; i++ {
		pick = append(pick, cuts[rnd.Intn(len(cuts))], rnd.Intn(len(data)))
	}
	end := bytes.LastIndex(data, []byte(endMarkerPrefix))
	pick = append(pick, end, end+10)
	adm := s.conn(t, "", false)
	for _, c := range pick {
		dst := s.newDB(t, adm, "cut")
		res, err := Import(context.Background(), adm, bytes.NewReader(data[:c]), ImportOptions{Dialect: MySQL, TargetDatabase: dst})
		if err == nil || res.Complete {
			t.Fatalf("在 %d/%d 处截断的文件导入竟然成功: %+v", c, len(data), res)
		}
	}
}

func makeBigTable(t testing.TB, s *myServer, c *sql.Conn, db string, rows int) {
	myExec(t, c, "USE "+quoteMySQLIdent(db),
		"CREATE TABLE big (id INT PRIMARY KEY, a VARCHAR(64), b DOUBLE, c DATETIME(6), d BLOB)",
		"CREATE TABLE seq10 (n INT PRIMARY KEY)",
		"INSERT INTO seq10 VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)")
	digits := []string{"seq10 a", "seq10 b", "seq10 c", "seq10 d", "seq10 e", "seq10 f"}
	expr := "a.n + b.n*10 + c.n*100 + d.n*1000 + e.n*10000 + f.n*100000"
	myExec(t, c, fmt.Sprintf(`INSERT INTO big SELECT %[1]s, CONCAT('row ', %[1]s, ' 值 ''q'' \\ ;'), (%[1]s) / 7e0,
		TIMESTAMPADD(SECOND, %[1]s, '2020-01-01 00:00:00.123456'), UNHEX(SHA1(%[1]s)) FROM %[2]s WHERE %[1]s < %[3]d`,
		expr, strings.Join(digits, " CROSS JOIN "), rows))
	myExec(t, c, "DROP TABLE seq10")
}

func testMyCancelKill(t *testing.T, s *myServer) {
	adm := s.conn(t, "", false)
	src := s.newDB(t, adm, "ck")
	makeBigTable(t, s, adm, src, 30000)
	data, _ := exportMy(t, s.conn(t, src, false), ExportOptions{Databases: []string{src}})

	// 导入中途取消
	dst := s.newDB(t, adm, "ckc")
	ctx, cancel := context.WithCancel(context.Background())
	imp := s.conn(t, "", false)
	res, err := Import(ctx, imp, &cancelReader{r: bytes.NewReader(data), at: int64(len(data) / 2), cancel: cancel}, ImportOptions{Dialect: MySQL, TargetDatabase: dst})
	cancel()
	if !errors.Is(err, context.Canceled) || res.Complete {
		t.Fatalf("取消后应返回 context.Canceled: %v %+v", err, res)
	}

	// 导入中途连接被 KILL：ContinueOnError 也必须报错
	dst2 := s.newDB(t, adm, "ckk")
	victim := s.conn(t, "", false)
	var id int64
	victim.QueryRowContext(context.Background(), "SELECT CONNECTION_ID()").Scan(&id)
	killer := s.conn(t, "", false)
	kr := &cancelReader{r: bytes.NewReader(data), at: int64(len(data) / 2), cancel: func() {
		killer.ExecContext(context.Background(), fmt.Sprintf("KILL %d", id))
	}}
	res, err = Import(context.Background(), victim, kr, ImportOptions{Dialect: MySQL, TargetDatabase: dst2, ContinueOnError: true})
	if err == nil || res.Complete {
		t.Fatalf("连接被 KILL 后导入竟然成功: %+v", res)
	}

	// 导出中途取消
	ctx2, cancel2 := context.WithCancel(context.Background())
	_, err = Export(ctx2, s.conn(t, src, false), &cancelWriter{at: int64(len(data) / 3), cancel: cancel2}, ExportOptions{Dialect: MySQL, Databases: []string{src}})
	cancel2()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("导出取消后应返回 context.Canceled，得到 %v", err)
	}
}

type cancelWriter struct {
	n, at  int64
	cancel func()
}

func (c *cancelWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.n >= c.at {
		c.cancel()
	}
	return len(p), nil
}

// 大表：50 万行流式导出导入，内存峰值不随行数线性增长。
func testMyLarge(t *testing.T, s *myServer) {
	adm := s.conn(t, "", false)
	src := s.newDB(t, adm, "big")
	const n = 500000
	makeBigTable(t, s, adm, src, n)
	f, err := os.CreateTemp(t.TempDir(), "big-*.sql")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	peak := measurePeak(t, func() {
		man, err := Export(context.Background(), s.conn(t, src, false), f, ExportOptions{Dialect: MySQL, Databases: []string{src}, ExtendedInsert: true})
		if err != nil {
			t.Fatal(err)
		}
		if man.Databases[0].Tables[0].Rows != n {
			t.Fatalf("导出 %d 行", man.Databases[0].Tables[0].Rows)
		}
	})
	st, _ := f.Stat()
	t.Logf("导出 %d 行，文件 %.1f MB，导出时堆峰值增量 %.1f MB", n, float64(st.Size())/1e6, float64(peak)/1e6)
	if peak > 48<<20 {
		t.Fatalf("导出内存峰值 %d 字节，随数据量增长了", peak)
	}
	dst := s.newDB(t, adm, "bigr")
	f.Seek(0, 0)
	peak = measurePeak(t, func() {
		res, err := Import(context.Background(), s.conn(t, "", false), f, ImportOptions{Dialect: MySQL, TargetDatabase: dst, TotalBytes: st.Size()})
		if err != nil || !res.Complete || res.Bytes != st.Size() {
			t.Fatalf("%v %+v", err, res)
		}
	})
	t.Logf("导入时堆峰值增量 %.1f MB", float64(peak)/1e6)
	if peak > 48<<20 {
		t.Fatalf("导入内存峰值 %d 字节，随数据量增长了", peak)
	}
	n1, h1, _ := TableChecksum(context.Background(), adm, MySQL, src, "big")
	n2, h2, _ := TableChecksum(context.Background(), adm, MySQL, dst, "big")
	if n1 != n || n1 != n2 || h1 != h2 {
		t.Fatalf("大表校验不一致 %d %s / %d %s", n1, h1, n2, h2)
	}
}

func measurePeak(t testing.TB, fn func()) uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse
	var peak atomic.Uint64
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > base && m.HeapInuse-base > peak.Load() {
					peak.Store(m.HeapInuse - base)
				}
			}
		}
	}()
	fn()
	close(done)
	return peak.Load()
}

// ---------- 随机 ----------

type rcol struct {
	def string
	gen func(*rand.Rand) string
}

func randMyColumns(s *myServer) []rcol {
	q := func(v string) string { return string(appendMySQLString(nil, []byte(v))) }
	randStr := func(r *rand.Rand, n int) string {
		alphabet := []string{"a", "Z", "0", " ", "'", "\"", "\\", "\n", "\r", "\t", "\x00", "\x1a", ";", "--", "#", "/*", "*/", "中", "😀", "é", "`"}
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	cols := []rcol{
		{"TINYINT", func(r *rand.Rand) string { return fmt.Sprint(r.Intn(256) - 128) }},
		{"SMALLINT UNSIGNED", func(r *rand.Rand) string { return fmt.Sprint(r.Intn(65536)) }},
		{"INT", func(r *rand.Rand) string { return fmt.Sprint(int32(r.Uint32())) }},
		{"BIGINT", func(r *rand.Rand) string { return fmt.Sprint(int64(r.Uint64())) }},
		{"BIGINT UNSIGNED", func(r *rand.Rand) string { return fmt.Sprint(r.Uint64()) }},
		{"DECIMAL(30,10)", func(r *rand.Rand) string {
			return fmt.Sprintf("%s%d.%010d", []string{"", "-"}[r.Intn(2)], r.Int63n(1e18), r.Int63n(1e10))
		}},
		{"FLOAT", func(r *rand.Rand) string {
			for {
				f := math.Float32frombits(r.Uint32())
				if !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) {
					return string(appendFloat64Literal(nil, float64(f)))
				}
			}
		}},
		{"DOUBLE", func(r *rand.Rand) string {
			for {
				f := math.Float64frombits(r.Uint64())
				if !math.IsNaN(f) && !math.IsInf(f, 0) {
					return string(appendFloat64Literal(nil, f))
				}
			}
		}},
		{"VARCHAR(100)", func(r *rand.Rand) string { return q(randStr(r, r.Intn(30))) }},
		{"TEXT", func(r *rand.Rand) string { return q(randStr(r, r.Intn(300))) }},
		{"VARCHAR(50) CHARACTER SET latin1", func(r *rand.Rand) string {
			b := make([]byte, r.Intn(30))
			for i := range b {
				b[i] = byte(32 + r.Intn(95))
			}
			return q(string(b) + "é")
		}},
		{"VARBINARY(200)", func(r *rand.Rand) string {
			b := make([]byte, r.Intn(200))
			r.Read(b)
			if len(b) == 0 {
				return "''"
			}
			return fmt.Sprintf("0x%X", b)
		}},
		{"BLOB", func(r *rand.Rand) string {
			b := make([]byte, r.Intn(2000))
			r.Read(b)
			return fmt.Sprintf("X'%X'", b)
		}},
		{"BIT(13)", func(r *rand.Rand) string { return fmt.Sprint(r.Intn(1 << 13)) }},
		{"DATE", func(r *rand.Rand) string {
			return fmt.Sprintf("'%04d-%02d-%02d'", 1000+r.Intn(9000), 1+r.Intn(12), 1+r.Intn(28))
		}},
		{"DATETIME(3)", func(r *rand.Rand) string {
			return fmt.Sprintf("'%04d-%02d-%02d %02d:%02d:%02d.%03d'", 1000+r.Intn(9000), 1+r.Intn(12), 1+r.Intn(28), r.Intn(24), r.Intn(60), r.Intn(60), r.Intn(1000))
		}},
		{"TIMESTAMP(6) NULL", func(r *rand.Rand) string {
			return fmt.Sprintf("FROM_UNIXTIME(%d.%06d)", 1+r.Int63n(2147483646), r.Intn(1000000))
		}},
		{"TIME(2)", func(r *rand.Rand) string {
			return fmt.Sprintf("'%s%d:%02d:%02d.%02d'", []string{"", "-"}[r.Intn(2)], r.Intn(839), r.Intn(60), r.Intn(60), r.Intn(100))
		}},
		{"YEAR", func(r *rand.Rand) string { return fmt.Sprint(1901 + r.Intn(255)) }},
		{"ENUM('a','b''c','d e')", func(r *rand.Rand) string { return []string{"'a'", "'b''c'", "'d e'"}[r.Intn(3)] }},
		{"SET('x','y','z')", func(r *rand.Rand) string { return []string{"''", "'x'", "'x,z'", "'x,y,z'"}[r.Intn(4)] }},
		{"JSON", func(r *rand.Rand) string {
			js, _ := json.Marshal(randStr(r, 5))
			return q(fmt.Sprintf(`{"k%d": [%d, %s, null, true, {"n": %s}]}`, r.Intn(9), r.Intn(1000), js, string(appendFloat64Literal(nil, r.NormFloat64()))))
		}},
	}
	return cols
}

func testMyRandom(t *testing.T, s *myServer) {
	adm := s.conn(t, "", false)
	cols := randMyColumns(s)
	for seed := int64(1); seed <= 4; seed++ {
		r := rand.New(rand.NewSource(seed))
		src := s.newDB(t, adm, "rnd")
		myExec(t, adm, "USE "+quoteMySQLIdent(src), "SET SESSION sql_mode = ''", "SET SESSION time_zone = '+00:00'")
		ntab := 1 + r.Intn(3)
		for ti := 0; ti < ntab; ti++ {
			nc := 2 + r.Intn(8)
			picked := make([]rcol, nc)
			defs := []string{"`id` INT PRIMARY KEY"}
			for i := range picked {
				picked[i] = cols[r.Intn(len(cols))]
				defs = append(defs, quoteMySQLIdent(fmt.Sprintf("c%d %s", i, randIdentMy(r)))+" "+picked[i].def)
			}
			tname := fmt.Sprintf("t%d %s", ti, randIdentMy(r))
			myExec(t, adm, "CREATE TABLE "+quoteMySQLIdent(tname)+" ("+strings.Join(defs, ", ")+")")
			rows := r.Intn(200)
			for i := 0; i < rows; i++ {
				vals := []string{fmt.Sprint(i)}
				for _, p := range picked {
					if r.Intn(10) == 0 {
						vals = append(vals, "NULL")
					} else {
						vals = append(vals, p.gen(r))
					}
				}
				myExec(t, adm, "INSERT INTO "+quoteMySQLIdent(tname)+" VALUES ("+strings.Join(vals, ", ")+")")
			}
		}
		data, man := exportMy(t, s.conn(t, src, false), ExportOptions{Databases: []string{src}, ExtendedInsert: r.Intn(2) == 0, MaxStatementBytes: 1 + r.Intn(50000)})
		dst := s.newDB(t, adm, "rndr")
		importMy(t, adm, data, ImportOptions{TargetDatabase: dst, CommitEvery: 1 + r.Intn(50)})
		compareDBs(t, s.conn(t, "", false), src, s.conn(t, "", false), dst, nil)
		checkMyManifest(t, s.conn(t, "", false), dst, man.Databases[0])
	}
}

func randIdentMy(r *rand.Rand) string {
	parts := []string{"a", "名", "x y", "q'q", "back`tick", "semi;", "--", "😀", "select", "end"}
	return parts[r.Intn(len(parts))] + parts[r.Intn(len(parts))]
}
