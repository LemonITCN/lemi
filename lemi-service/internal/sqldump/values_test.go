package sqldump

import (
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// MySQL 把 FLOAT 字面量先解析成 double、检查不超过 FLT_MAX、再转 float。随机位模式全部要还原成同一个 float32。
func TestFloat32LiteralRoundTrip(t *testing.T) {
	check := func(f float32) {
		s := string(appendFloat32Literal(nil, f))
		d, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("%v → %q 解析失败: %v", f, s, err)
		}
		if math.Abs(d) > math.MaxFloat32 {
			t.Fatalf("%v → %q 超过 FLT_MAX，MySQL 会拒绝", f, s)
		}
		if g := float32(d); math.Float32bits(g) != math.Float32bits(f) {
			t.Fatalf("%v(%08x) → %q → %v(%08x)", f, math.Float32bits(f), s, g, math.Float32bits(g))
		}
	}
	for _, f := range []float32{0, float32(math.Copysign(0, -1)), math.MaxFloat32, -math.MaxFloat32, math.SmallestNonzeroFloat32, 1.17549435e-38, 0.1, 1.2345678, 16777217} {
		check(f)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000000; i++ {
		f := math.Float32frombits(r.Uint32())
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			continue
		}
		check(f)
	}
	if s := string(appendFloat32Literal(nil, math.MaxFloat32)); s != "3.4028234663852886e+38" {
		t.Fatalf("FLT_MAX 应写精确值，得到 %s", s)
	}
	if s := string(appendFloat32Literal(nil, 0.1)); s != "0.1" {
		t.Fatalf("0.1 应写最短表示，得到 %s", s)
	}
}

func TestFloat64Literal(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 1000000; i++ {
		f := math.Float64frombits(r.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		s := string(appendFloat64Literal(nil, f))
		d, err := strconv.ParseFloat(s, 64)
		if err != nil || math.Float64bits(d) != math.Float64bits(f) {
			t.Fatalf("%v → %q → %v", f, s, d)
		}
	}
	if s := string(appendFloat64Literal(nil, math.Copysign(0, -1))); s != "-0e0" {
		t.Fatal(s)
	}
}

func TestSQLiteReal(t *testing.T) {
	cases := map[float64]string{1: "1.0", -2: "-2.0", 0.5: "0.5", 1e300: "1e+300", math.Inf(1): "9e999", math.Inf(-1): "-9e999"}
	for f, want := range cases {
		if got := string(appendSQLiteReal(nil, f)); got != want {
			t.Errorf("%v → %s，应为 %s", f, got, want)
		}
	}
}

func TestMySQLStringEscape(t *testing.T) {
	got := string(appendMySQLString(nil, []byte("a\x00'\"\b\n\r\t\x1a\\z中")))
	want := `'a\0\'\"\b\n\r\t\Z\\z中'`
	if got != want {
		t.Fatalf("%s\n%s", got, want)
	}
}

func TestSQLiteStringLiteral(t *testing.T) {
	if got := appendSQLiteString(nil, "it's"); string(got) != "'it''s'" {
		t.Fatal(string(got))
	}
	if got := appendSQLiteString(nil, "a\x00b"); string(got) != "CAST(X'610062' AS TEXT)" {
		t.Fatal(string(got))
	}
	if got := appendSQLiteString(nil, "\xff"); string(got) != "CAST(X'FF' AS TEXT)" {
		t.Fatal(string(got))
	}
	if got := appendSQLiteString(nil, "a\r\n"); !strings.HasPrefix(string(got), "CAST(") {
		t.Fatal(string(got))
	}
}

func TestBits(t *testing.T) {
	cases := map[string]string{"\x00": "b'0'", "\x01": "b'1'", "\x00\x05": "b'101'", "\x80\x00": "b'1000000000000000'", "": "b'0'"}
	for in, want := range cases {
		if got := string(appendBits(nil, []byte(in))); got != want {
			t.Errorf("%x → %s，应为 %s", in, got, want)
		}
	}
}

func TestStripDefiner(t *testing.T) {
	cases := [][2]string{
		{"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS select 1", "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1"},
		{"CREATE DEFINER=`a``b`@`%` PROCEDURE `p`() BEGIN SELECT 'DEFINER=x'; END", "CREATE PROCEDURE `p`() BEGIN SELECT 'DEFINER=x'; END"},
		{"CREATE DEFINER=`root`@`localhost` TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET @a = 1", "CREATE TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET @a = 1"},
		{"CREATE DEFINER='u'@'h' EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1", "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"},
		{"CREATE DEFINER=CURRENT_USER() FUNCTION f() RETURNS INT RETURN 1", "CREATE FUNCTION f() RETURNS INT RETURN 1"},
		{"CREATE PROCEDURE p() SELECT 'DEFINER=`x`@`y`'", "CREATE PROCEDURE p() SELECT 'DEFINER=`x`@`y`'"},
	}
	for _, c := range cases {
		if got := stripDefiner(c[0]); got != c[1] {
			t.Errorf("\n%s\n得到 %s\n应为 %s", c[0], got, c[1])
		}
	}
}

func TestDBNameRewrite(t *testing.T) {
	cases := map[string]string{
		"USE `old`": "USE `new`",
		"USE old":   "USE `new`",
		"CREATE DATABASE IF NOT EXISTS `old` /*!40100 DEFAULT CHARACTER SET utf8mb4 */":           "CREATE DATABASE IF NOT EXISTS `new` /*!40100 DEFAULT CHARACTER SET utf8mb4 */",
		"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `old` /*!40100 DEFAULT CHARACTER SET latin1 */": "CREATE DATABASE /*!32312 IF NOT EXISTS*/ `new` /*!40100 DEFAULT CHARACTER SET latin1 */",
		"/*!40000 DROP DATABASE IF EXISTS `old`*/":                                                "/*!40000 DROP DATABASE IF EXISTS `new`*/",
		"CREATE SCHEMA `old`":                       "CREATE SCHEMA `new`",
		"ALTER DATABASE `old` CHARACTER SET latin1": "ALTER DATABASE `new` CHARACTER SET latin1",
		"ALTER DATABASE CHARACTER SET latin1":       "ALTER DATABASE CHARACTER SET latin1",
		"USE `other`":                               "USE `other`",
		"SELECT * FROM `old`.t":                     "SELECT * FROM `old`.t",
	}
	m := map[string]string{"old": "new"}
	for in, want := range cases {
		ts := leadTokens(in, 8, true)
		got := in
		if tk, ok := dbNameToken(ts); ok {
			if to, mapped := m[identOf(in, tk)]; mapped {
				got = in[:tk.start] + quoteMySQLIdent(to) + in[tk.end:]
			}
		}
		if got != want {
			t.Errorf("%s\n得到 %s\n应为 %s", in, got, want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]string{
		"/*\n Lemi Dump SQL\n\n Source":                                                   "lemi",
		"\ufeff/*\n Navicat Premium Dump SQL\n":                                           "navicat",
		"/*\nNavicat MySQL Data Transfer\n\nSource Server":                                "navicat",
		"-- MySQL dump 10.13  Distrib 8.0.36":                                             "mysqldump",
		"/*M!999999\\- enable the sandbox mode */ \n-- MariaDB dump 10.19-11.4.4-MariaDB": "mysqldump",
		"CREATE TABLE t (a int);":                                                         "unknown",
	}
	for in, want := range cases {
		if got := detectFormat([]byte(in)); got != want {
			t.Errorf("%q → %s，应为 %s", in, got, want)
		}
	}
}

func TestTopoSort(t *testing.T) {
	got := topoSort([]string{"v_a", "v_b", "v_c", "v_d"}, map[string][]string{"v_a": {"v_b"}, "v_b": {"v_c", "t"}, "v_d": {"v_d"}})
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if len(got) != 4 || len(pos) != 4 || !(pos["v_c"] < pos["v_b"] && pos["v_b"] < pos["v_a"]) {
		t.Fatal(got)
	}
	// 环：不死循环，全部输出
	got = topoSort([]string{"x", "y"}, map[string][]string{"x": {"y"}, "y": {"x"}})
	if len(got) != 2 {
		t.Fatal(got)
	}
}

func TestBacktickIdents(t *testing.T) {
	got := backtickIdents("select `a``b`.`c` from `t` where x = '`not`' and y = \"`no`\"")
	if !reflect.DeepEqual(got, []string{"a`b", "c", "t"}) {
		t.Fatal(got)
	}
}

func TestCommentSafe(t *testing.T) {
	if got := commentSafe("a\nb\r*/c"); got != "a b * /c" {
		t.Fatal(got)
	}
}
