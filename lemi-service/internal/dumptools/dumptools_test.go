package dumptools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testManifest() *Manifest {
	all := map[string]File{}
	for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64", "windows-amd64"} {
		all[p] = File{Name: "x-" + p, SHA256: strings.Repeat("a", 64)}
	}
	noIntel := map[string]File{}
	for k, v := range all {
		if k != "darwin-amd64" {
			noIntel[k] = v
		}
	}
	return &Manifest{Versions: []Version{
		{Version: "8.0.46", Series: "8.0", Tag: "t1", Files: all},
		{Version: "8.4.13.1", Series: "8.4", Tag: "t2", Files: noIntel},
		{Version: "8.4.7", Series: "8.4", Tag: "t3", Files: all},
		{Version: "9.7.4.1", Series: "9.7", Tag: "t4", Files: noIntel},
		{Version: "9.5.0", Series: "9.5", Tag: "t5", Files: all},
		{Version: "26.10.0.1", Series: "26.10", Tag: "t6", Files: noIntel},
	}}
}

func TestPick(t *testing.T) {
	m := testManifest()
	cases := []struct{ product, server, platform, want string }{
		{"MySQL", "5.7.44-log", "linux-amd64", "8.0.46"},
		{"MySQL", "8.0.36", "linux-amd64", "8.0.46"},
		{"MySQL", "8.4.3", "linux-amd64", "8.4.13.1"},
		{"MySQL", "8.4.3", "darwin-amd64", "8.4.7"}, // 最新 8.4 没有 Intel 包，退回同系列有包的版本
		{"MySQL", "8.2.0", "linux-amd64", "8.4.13.1"},
		{"MySQL", "9.1.0", "linux-amd64", "9.7.4.1"},
		{"MySQL", "9.1.0", "darwin-amd64", "9.5.0"},
		{"MySQL", "26.10.0", "linux-amd64", "26.10.0.1"},
		{"MariaDB", "5.5.5-10.11.6-MariaDB", "linux-amd64", "8.0.46"},
	}
	for _, c := range cases {
		v, _, err := m.Pick(c.product, c.server, c.platform)
		if err != nil {
			t.Fatalf("%s %s %s：%v", c.product, c.server, c.platform, err)
		}
		if v.Version != c.want {
			t.Errorf("%s %s %s：选了 %s，应为 %s", c.product, c.server, c.platform, v.Version, c.want)
		}
	}
	if _, w, _ := m.Pick("MariaDB", "11.4.2-MariaDB", "linux-amd64"); len(w) == 0 {
		t.Error("MariaDB 应给出警告")
	}
	// 未来的服务器比清单还新：用最新并警告
	v, w, err := m.Pick("MySQL", "30.1.0", "darwin-amd64")
	if err != nil || v.Version != "9.5.0" || len(w) == 0 {
		t.Errorf("更新的服务器：%v %v %v", v, w, err)
	}
	// 清单为空
	if _, _, err := (&Manifest{}).Pick("MySQL", "8.0.1", "linux-amd64"); err == nil {
		t.Error("空清单应报错")
	}
	// 只有 8.4 时，5.7 服务器退而用更新的系列
	only84 := &Manifest{Versions: []Version{{Version: "8.4.7", Series: "8.4", Tag: "t", Files: map[string]File{"linux-amd64": {Name: "a", SHA256: strings.Repeat("b", 64)}}}}}
	if v, w, err := only84.Pick("MySQL", "5.7.1", "linux-amd64"); err != nil || v.Version != "8.4.7" || len(w) == 0 {
		t.Errorf("退回更新系列：%v %v %v", v, w, err)
	}
}

func TestBuiltinManifest(t *testing.T) {
	m := Builtin()
	for _, v := range m.Versions {
		if v.Tag != "tools-mysql-"+v.Version {
			t.Errorf("%s 的 tag 应为 tools-mysql-%s", v.Version, v.Version)
		}
	}
	if _, err := ParseManifest([]byte(`{"versions":[{"version":"8.4.7","tag":"x","files":{"linux-amd64":{"name":"../evil","sha256":"` + strings.Repeat("a", 64) + `"}}}]}`)); err == nil {
		t.Error("文件名带路径应被拒绝")
	}
}

func TestPlatform(t *testing.T) {
	cases := []struct {
		goos, arch string
		build      uint32
		want       string
		err        bool
	}{
		{"linux", "amd64", 0, "linux-amd64", false},
		{"linux", "arm64", 0, "linux-arm64", false},
		{"darwin", "arm64", 0, "darwin-arm64", false},
		{"windows", "amd64", 19045, "windows-amd64", false},
		{"windows", "arm64", 22631, "windows-amd64", false}, // Windows 11 ARM：x64 仿真
		{"windows", "arm64", 19041, "", true},               // Windows 10 ARM：没有 x64 仿真
		{"linux", "386", 0, "", true},
		{"freebsd", "amd64", 0, "", true},
	}
	for _, c := range cases {
		got, err := platformFor(c.goos, c.arch, c.build)
		if c.err {
			if !errors.Is(err, ErrNoPlatform) {
				t.Errorf("%s/%s：应报 ErrNoPlatform，实际 %v", c.goos, c.arch, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s/%s：%s %v", c.goos, c.arch, got, err)
		}
	}
}

func TestParseVersionOutput(t *testing.T) {
	cases := []struct {
		out, ver string
		maria    bool
	}{
		{"mysqldump  Ver 8.4.13 for macos15 on arm64 (MySQL Community Server - GPL)", "8.4.13", false},
		{"mysql  Ver 8.0.46 for Linux on x86_64 (MySQL Community Server - GPL)", "8.0.46", false},
		{"mysqldump  Ver 10.19 Distrib 10.11.6-MariaDB, for debian-linux-gnu (x86_64)", "10.11.6", true},
		{"mysqldump from 11.4.2-MariaDB, client 10.19 for Linux (x86_64)", "11.4.2", true},
	}
	for _, c := range cases {
		v, m, err := parseVersionOutput(c.out)
		if err != nil || v != c.ver || m != c.maria {
			t.Errorf("%q：%s %v %v", c.out, v, m, err)
		}
	}
	if _, _, err := parseVersionOutput("hello"); err == nil {
		t.Error("不认识的输出应报错")
	}
}

func TestOptQuote(t *testing.T) {
	got := optQuote(`a"b'c\d#e` + "\n")
	want := `"a\"b\'c\\d#e\n"`
	if got != want {
		t.Fatalf("%s != %s", got, want)
	}
}

type entry struct {
	name string
	typ  byte
	body string
	mode int64
}

func makeTarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body))}
		if e.typ == tar.TypeSymlink {
			h.Linkname, h.Size = "/etc/passwd", 0
		}
		if e.typ == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractRejects(t *testing.T) {
	bad := [][]entry{
		{{name: "../evil", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{{name: "bin/../../evil", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{{name: "/abs", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{{name: "bin/link", typ: tar.TypeSymlink, mode: 0o777}},
		{{name: `bin\x`, typ: tar.TypeReg, body: "x", mode: 0o644}},
	}
	for i, es := range bad {
		dir := t.TempDir()
		src := filepath.Join(dir, "a.tgz")
		os.WriteFile(src, makeTarGz(t, es), 0o600)
		out := filepath.Join(dir, "out")
		os.MkdirAll(out, 0o700)
		if err := extractTarGz(src, out); err == nil {
			t.Errorf("第 %d 组应被拒绝", i)
		}
		if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
			t.Fatalf("第 %d 组写出了解压目录之外的文件", i)
		}
	}
	// 解压炸弹：总量上限
	old := MaxUnpacked
	MaxUnpacked = 1000
	defer func() { MaxUnpacked = old }()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tgz")
	os.WriteFile(src, makeTarGz(t, []entry{{name: "a", typ: tar.TypeReg, body: strings.Repeat("0", 800), mode: 0o644}, {name: "b", typ: tar.TypeReg, body: strings.Repeat("0", 800), mode: 0o644}}), 0o600)
	if err := extractTarGz(src, t.TempDir()); err == nil {
		t.Error("超过总量上限应失败")
	}
}

func TestExtractZipRejects(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.dll")
	w.Write([]byte("x"))
	zw.Close()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.zip")
	os.WriteFile(src, buf.Bytes(), 0o600)
	if err := extractZip(src, filepath.Join(dir, "out")); err == nil {
		t.Error("zip 路径穿越应被拒绝")
	}
}

// 用一个假的工具包测试下载、校验、解压与失败清理。
func TestInstall(t *testing.T) {
	platform, err := Platform()
	if err != nil {
		t.Skip(err)
	}
	pkg := makeTarGz(t, []entry{
		{name: "bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "bin/" + exeName("mysqldump", platform), typ: tar.TypeReg, body: "#!/bin/sh\necho 'mysqldump  Ver 8.4.13 for test'\n", mode: 0o755},
		{name: "bin/" + exeName("mysql", platform), typ: tar.TypeReg, body: "#!/bin/sh\necho 'mysql  Ver 8.4.13 for test'\n", mode: 0o755},
		{name: "lib/libfoo.so", typ: tar.TypeReg, body: "lib", mode: 0o644},
		{name: "LICENSE", typ: tar.TypeReg, body: "GPL", mode: 0o644},
	})
	sum := sha256.Sum256(pkg)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/tools-mysql-8.4.13.1/pkg.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(pkg)
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), srv.URL)
	m.Manifest = &Manifest{Versions: []Version{{Version: "8.4.13.1", Series: "8.4", Tag: "tools-mysql-8.4.13.1",
		Files: map[string]File{platform: {Name: "pkg.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}}}
	tool, warns, err := m.Resolve(context.Background(), "MySQL", "8.4.3", true, nil)
	if err != nil {
		t.Fatal(err, warns)
	}
	if tool.Version != "8.4.13.1" || !strings.HasPrefix(tool.Mysqldump, m.Dir) {
		t.Fatalf("%+v", tool)
	}
	if fi, _ := os.Stat(tool.Mysqldump); fi.Mode()&0o100 == 0 {
		t.Fatal("程序没有执行权限")
	}
	// 再次解析不重复下载
	if _, _, err := m.Resolve(context.Background(), "MySQL", "8.4.3", true, nil); err != nil || hits != 1 {
		t.Fatalf("重复下载：hits=%d err=%v", hits, err)
	}
	if _, list := statusOf(m); len(list) != 1 || !list[0].Installed {
		t.Fatalf("状态：%+v", list)
	}
	// 校验失败：删除并报错，不留下目录
	m2 := NewManager(t.TempDir(), srv.URL)
	m2.Manifest = &Manifest{Versions: []Version{{Version: "8.4.13.1", Series: "8.4", Tag: "tools-mysql-8.4.13.1",
		Files: map[string]File{platform: {Name: "pkg.tar.gz", SHA256: strings.Repeat("0", 64)}}}}}
	if _, _, err := m2.Resolve(context.Background(), "MySQL", "8.4.3", true, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("应报校验失败：%v", err)
	}
	ents, _ := os.ReadDir(m2.Dir)
	if len(ents) != 0 {
		t.Fatalf("校验失败后留下了文件：%v", ents)
	}
	// 没装且不自动安装
	if _, _, err := m2.Resolve(context.Background(), "MySQL", "8.4.3", false, nil); err == nil {
		t.Fatal("没装时应报错")
	}
	// 预装目录（Docker 镜像）：没有完成标记也认
	pre := t.TempDir()
	os.MkdirAll(filepath.Join(pre, "mysql-8.4.13.1"), 0o700)
	if err := os.Rename(m.InstallDir("8.4.13.1", platform), filepath.Join(pre, "mysql-8.4.13.1", platform)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(pre, "mysql-8.4.13.1", platform, ".lemi-ok"))
	if m.Installed(&m.Manifest.Versions[0], platform) != nil {
		t.Fatal("挪走后不应显示已安装")
	}
	m.Preinstalled = pre
	if tl := m.Installed(&m.Manifest.Versions[0], platform); tl == nil || tl.Source != "preinstalled" {
		t.Fatalf("预装目录：%+v", tl)
	}
	m.Preinstalled = ""
	if err := m.Remove("8.4.13.1"); err != nil {
		t.Fatal(err)
	}
	if m.Installed(&m.Manifest.Versions[0], platform) != nil {
		t.Fatal("删除后仍显示已安装")
	}
}

func statusOf(m *Manager) (string, []VersionStatus) {
	p, _, l := m.Status()
	return p, l
}

// 用 mysql-tools 工作流产出的真实工具包测试（LEMI_TEST_TOOLS_PKG=<tar.gz 路径>）。
func TestInstallRealPackage(t *testing.T) {
	pkgPath := os.Getenv("LEMI_TEST_TOOLS_PKG")
	if pkgPath == "" {
		t.Skip("未设置 LEMI_TEST_TOOLS_PKG")
	}
	platform, err := Platform()
	if err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	name := filepath.Base(pkgPath)
	ver := strings.TrimSuffix(strings.TrimPrefix(name, "mysql-client-"), "-"+platform+".tar.gz")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	defer srv.Close()
	m := NewManager(t.TempDir(), srv.URL)
	m.Manifest = &Manifest{Versions: []Version{{Version: ver, Series: seriesOf(ver), Tag: "tools-mysql-" + ver, Files: map[string]File{platform: {Name: name, SHA256: hex.EncodeToString(sum[:])}}}}}
	tool, _, err := m.Resolve(context.Background(), "MySQL", "8.4.0", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, maria, err := DetectVersion(context.Background(), tool.Mysqldump)
	if err != nil || maria || !strings.HasPrefix(ver, v) {
		t.Fatalf("解压后的 mysqldump --version：%s %v %v", v, maria, err)
	}
}

func TestPrepareOptionFile(t *testing.T) {
	tool := &Tool{Mysqldump: "x", Mysql: "y"}
	s, err := tool.prepare(context.Background(), Conn{Host: "db.example.com", Port: 3307, User: "u", Password: `p"w'\#`, TLSMode: "verify-full", TLSCA: "CA"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.defaults)
	fi, _ := os.Stat(s.defaults)
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("选项文件权限 %v", fi.Mode().Perm())
	}
	txt := string(b)
	for _, want := range []string{`password="p\"w\'\\#"`, `host="db.example.com"`, "port=3307", "ssl-mode=VERIFY_IDENTITY", "ssl-ca="} {
		if !strings.Contains(txt, want) {
			t.Errorf("选项文件缺少 %s：\n%s", want, txt)
		}
	}
	dir := s.dir
	s.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("临时目录没有删除")
	}
	// 经转发：主机改为 127.0.0.1，verify-full 降为 VERIFY_CA 并警告
	s, err = tool.prepare(context.Background(), Conn{Host: "db.example.com", Port: 3306, User: "u", TLSMode: "verify-full",
		Dial: func(ctx context.Context, n, a string) (net.Conn, error) { return nil, errors.New("x") }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	b, _ = os.ReadFile(s.defaults)
	if !strings.Contains(string(b), `host="127.0.0.1"`) || !strings.Contains(string(b), "ssl-mode=VERIFY_CA") || len(s.warnings) == 0 {
		t.Fatalf("转发：\n%s %v", b, s.warnings)
	}
	// MariaDB 客户端
	mt := &Tool{MariaDB: true}
	s2, err := mt.prepare(context.Background(), Conn{Host: "h", User: "u", TLSMode: "disable"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.close()
	b, _ = os.ReadFile(s2.defaults)
	if !strings.Contains(string(b), "\nskip-ssl\n") || strings.Contains(string(b), "ssl-mode") {
		t.Fatalf("MariaDB 选项：\n%s", b)
	}
	args := mt.DumpArgs("f", DumpOptions{Databases: []string{"a"}, NoColumnStatistics: true})
	if strings.Contains(strings.Join(args, " "), "gtid") || strings.Contains(strings.Join(args, " "), "column-statistics") {
		t.Fatalf("MariaDB 客户端不应带 MySQL 专有参数：%v", args)
	}
	args = tool.DumpArgs("f", DumpOptions{Databases: []string{"a", "b"}, NoColumnStatistics: true})
	if args[0] != "--defaults-file=f" || !strings.Contains(strings.Join(args, " "), "--column-statistics=0 --databases a b") {
		t.Fatalf("参数：%v", args)
	}
}

func TestForwarder(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	dials := 0
	f, err := Forward(func(ctx context.Context, n, a string) (net.Conn, error) {
		dials++
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}, "remote.invalid:3306")
	if err != nil {
		t.Fatal(err)
	}
	// 目标是假地址：拨号失败时客户端连接被关闭
	c, err := net.Dial("tcp", f.Addr())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("拨号失败时应断开")
	}
	c.Close()
	f.Close()

	f, err = Forward((&net.Dialer{}).DialContext, echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.Addr(), "127.0.0.1:") {
		t.Fatalf("应监听回环地址：%s", f.Addr())
	}
	c, err = net.Dial("tcp", f.Addr())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hello"))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("转发：%q %v", got, err)
	}
	f.Close() // 关闭时断开进行中的连接
	if _, err := c.Read(got); err == nil {
		t.Fatal("关闭转发后连接应断开")
	}
	if _, err := net.Dial("tcp", f.Addr()); err == nil {
		t.Fatal("关闭后不应再接受连接")
	}
}
