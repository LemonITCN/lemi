package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain 让测试程序自己充当「新版本的 lemi」：设置了 LEMI_SELFUPDATE_FAKE_VERSION 且参数是 version 时，
// 打印版本号后退出。这样升级流程里「运行新程序确认版本号」这一步也能用真实的可执行文件测。
func TestMain(m *testing.M) {
	if v := os.Getenv("LEMI_SELFUPDATE_FAKE_VERSION"); v != "" && len(os.Args) > 1 && os.Args[1] == "version" {
		_, _ = os.Stdout.WriteString("lemi " + v + "\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestApplyRunsNewBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if !Supported(goos, goarch) {
		t.Skip("当前平台没有发布包")
	}
	for _, tc := range []struct {
		reported string
		ok       bool
	}{{"0.2.0", true}, {"0.1.9", false}} {
		t.Run(tc.reported, func(t *testing.T) {
			t.Setenv("LEMI_SELFUPDATE_FAKE_VERSION", tc.reported)
			k := newKit(t, goos)
			srv := fakeGitHub(t, k.release(t, "0.2.0", goos, goarch, string(bin)))
			u := k.updater(srv, "0.1.0", goos, goarch)
			u.SkipExecCheck = false
			err := checkAndApply(t, u)
			if tc.ok && err != nil {
				t.Fatalf("新程序版本号正确时应当成功：%v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "版本号")) {
				t.Fatalf("新程序报告的版本号不对时应当拒绝，实际：%v", err)
			}
			if replaced := k.content(t) != "old-binary"; replaced != tc.ok {
				t.Fatalf("替换结果不对：replaced=%v", replaced)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"v0.1.0", "0.1.0", 0},
		{"0.1.1", "0.1.0", 1},
		{"0.2.0", "0.10.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.1.0-dev", "0.1.0", -1},
		{"0.1.0", "0.1.0-dev", 1},
		{"0.1.0-rc.2", "0.1.0-rc.10", -1},
		{"0.1.0-alpha", "0.1.0-beta", -1},
		{"0.1.0-1", "0.1.0-alpha", -1},
		{"0.1.0.5", "0.1.0", 1},
		{"0.1", "0.1.0", 0},
		{"0.1.0+abc", "0.1.0", 0},
		{"garbage", "0.0.1", -1},
		{"0.0.1", "garbage", 1},
		{"1.x.0", "0.1.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	m, err := ParseSums([]byte(h + "  lemi-0.2.0-linux-amd64.tar.gz\n" + h + " *lemi-0.2.0-windows-amd64.zip\r\n\n"))
	if err != nil || m["lemi-0.2.0-linux-amd64.tar.gz"] != h || m["lemi-0.2.0-windows-amd64.zip"] != h {
		t.Fatalf("解析失败：%v %v", m, err)
	}
	for _, bad := range []string{
		h + "  ../evil\n",
		h + "  dir/file\n",
		"xyz  file\n",
		h + "  a\n" + h + "  a\n",
	} {
		if _, err := ParseSums([]byte(bad)); err == nil {
			t.Errorf("应拒绝：%q", bad)
		}
	}
}

func TestBuiltinPublicKey(t *testing.T) {
	if len(ReleasePublicKey()) != ed25519.PublicKeySize {
		t.Fatal("内置公钥长度不对")
	}
}

// fakeRelease 是假 GitHub 上的一个版本。
type fakeRelease struct {
	tag    string
	files  map[string][]byte
	status int // 非 0 时 releases/latest 返回这个状态码
}

// fakeGitHub 起一个 HTTPS 假 GitHub：/latest 返回版本信息，/dl/<文件名> 返回文件。
func fakeGitHub(t *testing.T, rel *fakeRelease) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "缺少 User-Agent", 400)
			return
		}
		switch {
		case r.URL.Path == "/latest":
			if rel.status != 0 {
				w.WriteHeader(rel.status)
				return
			}
			var assets []map[string]any
			for name, b := range rel.files {
				assets = append(assets, map[string]any{"name": name, "size": len(b), "browser_download_url": srv.URL + "/dl/" + name})
			}
			// 版本列表：混入工具版本、预发布版与更早的正式版，检查时只能挑中 rel
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "tools-mysql-9.7.4.1", "name": "MySQL 工具", "published_at": "2026-11-27T00:00:00Z", "assets": []any{}},
				{"tag_name": "v99.0.0-rc.1", "name": "预发布", "prerelease": true, "published_at": "2026-11-27T00:00:00Z", "assets": []any{}},
				{"tag_name": rel.tag, "name": "Lemi " + strings.TrimPrefix(rel.tag, "v"), "body": "更新内容",
					"html_url": srv.URL + "/release", "published_at": "2026-11-26T00:00:00Z", "assets": assets},
				{"tag_name": "v0.0.1", "name": "Lemi 0.0.1", "published_at": "2026-01-01T00:00:00Z", "assets": []any{}},
			})
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			b, ok := rel.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func sumLine(name string, b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]) + "  " + name + "\n"
}

type kit struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	exe  string
}

func newKit(t *testing.T, goos string) *kit {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), BinaryName(goos))
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &kit{pub: pub, priv: priv, exe: exe}
}

// release 生成一个正常签名的版本：包里是 body 内容的「可执行文件」。
func (k *kit) release(t *testing.T, ver, goos, goarch, body string) *fakeRelease {
	t.Helper()
	pkg := PackageName(ver, goos, goarch)
	files := map[string]string{BinaryName(goos): body, "README.md": "readme", "LICENSE": "license"}
	var data []byte
	if goos == "windows" {
		data = zipOf(t, files)
	} else {
		data = tarGz(t, files)
	}
	other := tarGz(t, map[string]string{"lemi": "other platform"})
	sums := []byte(sumLine(pkg, data) + sumLine(PackageName(ver, "linux", "riscv64"), other))
	return &fakeRelease{tag: "v" + ver, files: map[string][]byte{
		pkg:      data,
		SumsName: sums,
		SigName:  SignSums(k.priv, sums),
	}}
}

func (k *kit) updater(srv *httptest.Server, current, goos, goarch string) *Updater {
	return &Updater{APIURL: srv.URL + "/latest", Client: srv.Client(), PublicKey: k.pub, Current: current,
		GOOS: goos, GOARCH: goarch, ExePath: k.exe, SkipExecCheck: true}
}

func (k *kit) content(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(k.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkAndApply(t *testing.T, u *Updater) error {
	t.Helper()
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	_, err = u.Apply(context.Background(), rel)
	return err
}

func TestApplySignedRelease(t *testing.T) {
	for _, p := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "arm64"}} {
		t.Run(p.goos+"-"+p.goarch, func(t *testing.T) {
			k := newKit(t, p.goos)
			srv := fakeGitHub(t, k.release(t, "0.2.0", p.goos, p.goarch, "new-binary-0.2.0"))
			u := k.updater(srv, "0.1.0", p.goos, p.goarch)
			rel, err := u.Check(context.Background())
			if err != nil || rel == nil || rel.Version != "0.2.0" || !u.Newer(rel) {
				t.Fatalf("检查结果不对：%+v %v", rel, err)
			}
			res, err := u.Apply(context.Background(), rel)
			if err != nil {
				t.Fatalf("签名正确应当升级成功：%v", err)
			}
			if res.From != "0.1.0" || res.To != "0.2.0" {
				t.Fatalf("结果：%+v", res)
			}
			if got := k.content(t); got != "new-binary-0.2.0" {
				t.Fatalf("可执行文件没有被替换：%q", got)
			}
			// 目录里只剩新可执行文件（Windows 多一个 .old），不留临时文件
			entries, _ := os.ReadDir(filepath.Dir(k.exe))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".lemi-update-") {
					t.Fatalf("留下了临时文件 %s", e.Name())
				}
			}
			u.CleanupOld()
		})
	}
}

func TestRejectBadSignature(t *testing.T) {
	k := newKit(t, "linux")
	rel := k.release(t, "0.2.0", "linux", "amd64", "evil")
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	rel.files[SigName] = SignSums(other, rel.files[SumsName]) // 别人的私钥签的
	srv := fakeGitHub(t, rel)
	if err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("签名错误应当拒绝，实际：%v", err)
	}
	if k.content(t) != "old-binary" {
		t.Fatal("签名错误时不能改动可执行文件")
	}
}

func TestRejectTamperedSums(t *testing.T) {
	k := newKit(t, "linux")
	rel := k.release(t, "0.2.0", "linux", "amd64", "good")
	// 换掉包并改写清单里的哈希，但签名还是原清单的
	evil := tarGz(t, map[string]string{"lemi": "evil"})
	pkg := PackageName("0.2.0", "linux", "amd64")
	rel.files[pkg] = evil
	rel.files[SumsName] = []byte(sumLine(pkg, evil))
	srv := fakeGitHub(t, rel)
	if err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("清单被改应当拒绝，实际：%v", err)
	}
	if k.content(t) != "old-binary" {
		t.Fatal("清单被改时不能改动可执行文件")
	}
}

func TestRejectMissingSignature(t *testing.T) {
	k := newKit(t, "linux")
	rel := k.release(t, "0.2.0", "linux", "amd64", "x")
	delete(rel.files, SigName)
	srv := fakeGitHub(t, rel)
	if err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64")); err == nil {
		t.Fatal("没有签名文件应当拒绝")
	}
	if k.content(t) != "old-binary" {
		t.Fatal("没有签名时不能改动可执行文件")
	}
}

func TestRejectHashMismatch(t *testing.T) {
	k := newKit(t, "linux")
	rel := k.release(t, "0.2.0", "linux", "amd64", "good")
	// 清单与签名都是真的，但下载到的包被换了
	rel.files[PackageName("0.2.0", "linux", "amd64")] = tarGz(t, map[string]string{"lemi": "evil"})
	srv := fakeGitHub(t, rel)
	if err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64")); !errors.Is(err, ErrChecksum) {
		t.Fatalf("哈希不符应当拒绝，实际：%v", err)
	}
	if k.content(t) != "old-binary" {
		t.Fatal("哈希不符时不能改动可执行文件")
	}
}

func TestRejectDowngradeAndSame(t *testing.T) {
	for _, cur := range []string{"0.2.0", "0.3.0"} {
		k := newKit(t, "linux")
		srv := fakeGitHub(t, k.release(t, "0.2.0", "linux", "amd64", "x"))
		u := k.updater(srv, cur, "linux", "amd64")
		rel, err := u.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if u.Newer(rel) {
			t.Fatalf("当前 %s 时不应提示 0.2.0 为新版本", cur)
		}
		if _, err := u.Apply(context.Background(), rel); !errors.Is(err, ErrNotNewer) {
			t.Fatalf("当前 %s 时升级到 0.2.0 应当拒绝，实际：%v", cur, err)
		}
		if k.content(t) != "old-binary" {
			t.Fatal("拒绝降级时不能改动可执行文件")
		}
	}
}

// 旧版本真实签名的清单 + 冒充的新版本号：清单里没有新版本的包名，必须拒绝（防重放旧版本）。
func TestRejectReplayedOldRelease(t *testing.T) {
	k := newKit(t, "linux")
	old := k.release(t, "0.1.5", "linux", "amd64", "old-but-signed")
	pkgOld := PackageName("0.1.5", "linux", "amd64")
	pkgNew := PackageName("0.2.0", "linux", "amd64")
	rel := &fakeRelease{tag: "v0.2.0", files: map[string][]byte{
		pkgNew:   old.files[pkgOld],
		SumsName: old.files[SumsName],
		SigName:  old.files[SigName],
	}}
	srv := fakeGitHub(t, rel)
	err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64"))
	if err == nil || !strings.Contains(err.Error(), "签名清单里没有") {
		t.Fatalf("旧清单冒充新版本应当拒绝，实际：%v", err)
	}
	if k.content(t) != "old-binary" {
		t.Fatal("重放旧版本时不能改动可执行文件")
	}
}

func TestRejectArchiveWithoutBinary(t *testing.T) {
	k := newKit(t, "linux")
	pkg := PackageName("0.2.0", "linux", "amd64")
	data := tarGz(t, map[string]string{"../lemi": "escape", "a/b/lemi": "too deep", "README.md": "x"})
	sums := []byte(sumLine(pkg, data))
	srv := fakeGitHub(t, &fakeRelease{tag: "v0.2.0", files: map[string][]byte{pkg: data, SumsName: sums, SigName: SignSums(k.priv, sums)}})
	if err := checkAndApply(t, k.updater(srv, "0.1.0", "linux", "amd64")); !errors.Is(err, errBadArchive) {
		t.Fatalf("包里没有合法的可执行文件应当拒绝，实际：%v", err)
	}
	if k.content(t) != "old-binary" {
		t.Fatal("包不对时不能改动可执行文件")
	}
}

func TestNoReleaseYet(t *testing.T) {
	k := newKit(t, "linux")
	srv := fakeGitHub(t, &fakeRelease{status: http.StatusNotFound})
	u := k.updater(srv, "0.1.0", "linux", "amd64")
	rel, err := u.Check(context.Background())
	if err != nil || rel != nil {
		t.Fatalf("没有正式版时应返回空：%v %v", rel, err)
	}
	if _, err := u.Apply(context.Background(), nil); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("实际：%v", err)
	}
}

func TestRateLimited(t *testing.T) {
	k := newKit(t, "linux")
	srv := fakeGitHub(t, &fakeRelease{status: http.StatusForbidden})
	_, err := k.updater(srv, "0.1.0", "linux", "amd64").Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "限流") {
		t.Fatalf("实际：%v", err)
	}
}

func TestRejectNonHTTPS(t *testing.T) {
	k := newKit(t, "linux")
	u := &Updater{Client: http.DefaultClient, PublicKey: k.pub, Current: "0.1.0", GOOS: "linux", GOARCH: "amd64", ExePath: k.exe, SkipExecCheck: true}
	rel := &Release{Version: "0.2.0", Assets: []Asset{
		{Name: PackageName("0.2.0", "linux", "amd64"), URL: "http://example.invalid/p"},
		{Name: SumsName, URL: "http://example.invalid/s"},
		{Name: SigName, URL: "http://example.invalid/g"},
	}}
	if _, err := u.Apply(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("非 HTTPS 下载地址应当拒绝，实际：%v", err)
	}
}

func TestPackageName(t *testing.T) {
	if got := PackageName("v1.2.3", "windows", "arm64"); got != "lemi-1.2.3-windows-arm64.zip" {
		t.Fatal(got)
	}
	if got := PackageName("1.2.3", "darwin", "amd64"); got != "lemi-1.2.3-darwin-amd64.tar.gz" {
		t.Fatal(got)
	}
	if Supported("linux", "riscv64") || !Supported("windows", "arm64") {
		t.Fatal("平台支持表不对")
	}
}

// 仓库里只有工具版本（tools-mysql-*）与预发布版时，视为还没有正式版，不能报错
func TestCheckOnlyToolReleases(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "tools-mysql-9.7.4.1", "published_at": "2026-11-27T00:00:00Z", "assets": []any{}},
			{"tag_name": "tools-mysql-8.0.46", "published_at": "2026-11-26T00:00:00Z", "assets": []any{}},
			{"tag_name": "v1.0.0-beta.1", "prerelease": true, "published_at": "2026-11-26T00:00:00Z", "assets": []any{}},
			{"tag_name": "v2.0.0", "draft": true, "published_at": "2026-11-26T00:00:00Z", "assets": []any{}},
		})
	}))
	t.Cleanup(srv.Close)
	u := &Updater{APIURL: srv.URL, Client: srv.Client(), Current: "0.1.0", GOOS: "linux", GOARCH: "amd64"}
	rel, err := u.Check(context.Background())
	if err != nil || rel != nil {
		t.Fatalf("只有工具版本时应当是 (nil, nil)，得到 (%v, %v)", rel, err)
	}
}
