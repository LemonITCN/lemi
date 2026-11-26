package target

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

// TestMain 把重试间隔与分片大小改小，让测试跑得快（各测试串行执行，不改并发中的全局变量）。
func TestMain(m *testing.M) {
	retryBaseDelay = time.Millisecond
	retryMaxDelay = 5 * time.Millisecond
	retryAfterCap = 20 * time.Millisecond
	s3PartSize = 5 << 20 // minio-go 允许的最小分片
	ossPartSize = 256 << 10
	azureBlockSize = 256 << 10
	os.Exit(m.Run())
}

func randData(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

var badPaths = []string{
	"", "/abs", "/", "../x", "a/../b", "a/..", "..", ".", "./a", "a/./b", "a//b", "a/",
	`a\b`, `..\x`, "C:/x", "c:x", "a\x00b", "a\nb", "a\x7fb", "\xff\xfe",
}

func TestCleanPath(t *testing.T) {
	for _, p := range badPaths {
		if _, err := cleanPath(p); err == nil {
			t.Errorf("cleanPath(%q) 应该被拒绝", p)
		}
	}
	good := []string{"a", "a/b/c.txt", "中文 目录/文件 名.txt", "conn-all-20261125-120000.sql.zst.lemienc", "a b/c+d=e@f!g'h(i)", "...a", "a..b", ".hidden"}
	for _, p := range good {
		if got, err := cleanPath(p); err != nil || got != p {
			t.Errorf("cleanPath(%q) = %q, %v", p, got, err)
		}
	}
	if _, err := cleanFilePath("dir/~$abc.tmp"); err == nil {
		t.Error("临时文件名应该被 Put 拒绝")
	}
	for _, p := range []string{"", "a/", "a/b", "a/b-", "dir/"} {
		if _, err := cleanPrefix(p); err != nil {
			t.Errorf("cleanPrefix(%q) 不应报错：%v", p, err)
		}
	}
	for _, p := range []string{"../", "/a", "a//", `a\`, "./"} {
		if _, err := cleanPrefix(p); err == nil {
			t.Errorf("cleanPrefix(%q) 应该被拒绝", p)
		}
	}
	for in, want := range map[string]string{"": "", "/": "", " /a/b/ ": "a/b", "x": "x"} {
		if got, err := cleanRootPrefix(in); err != nil || got != want {
			t.Errorf("cleanRootPrefix(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := cleanRootPrefix("a/../b"); err == nil {
		t.Error("配置 prefix 含 .. 应被拒绝")
	}
	type rd struct {
		dir string
		abs bool
	}
	for in, want := range map[string]rd{"": {"", false}, "/": {"", true}, "/a//b/": {"a/b", true}, "./x/./y": {"x/y", false}, " backups ": {"backups", false}} {
		d, abs, err := cleanRemoteDir("X", in)
		if err != nil || d != want.dir || abs != want.abs {
			t.Errorf("cleanRemoteDir(%q) = %q %v %v", in, d, abs, err)
		}
	}
	for _, in := range []string{"../x", "/a/../b", `a\b`, "a\x01b"} {
		if _, _, err := cleanRemoteDir("X", in); err == nil {
			t.Errorf("cleanRemoteDir(%q) 应该被拒绝", in)
		}
	}
}

func TestSizedReader(t *testing.T) {
	data := randData(t, 100000)
	read := func(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

	got, err := read(newSizedReader(bytes.NewReader(data), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("长度正确时应原样读出：%v", err)
	}
	got, err = read(newSizedReader(iotest.OneByteReader(bytes.NewReader(data)), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("逐字节读也应原样读出：%v", err)
	}
	if _, err := read(newSizedReader(bytes.NewReader(data), int64(len(data))+1)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据短了应报 ErrSizeMismatch，实际 %v", err)
	}
	// 数据长了：读出的数据不能达到声明长度（最后一段不交出去），并报错。
	sr := newSizedReader(bytes.NewReader(data), int64(len(data))-1)
	got, err = read(sr)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据长了应报 ErrSizeMismatch，实际 %v", err)
	}
	if len(got) >= len(data)-1 {
		t.Fatalf("数据长了时不应交出完整的声明长度（交出了 %d 字节）", len(got))
	}
	if _, err := read(newSizedReader(bytes.NewReader(nil), 0)); err != nil {
		t.Fatalf("空数据长度 0 应成功：%v", err)
	}
	if _, err := read(newSizedReader(bytes.NewReader([]byte{1}), 0)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("声明 0 却有数据应报错，实际 %v", err)
	}
	boom := errors.New("boom")
	if _, err := read(newSizedReader(iotest.ErrReader(boom), 10)); !errors.Is(err, boom) {
		t.Fatalf("下层错误应原样返回，实际 %v", err)
	}
	// size < 0：原样读到 EOF。
	if got, err := read(putReader(bytes.NewReader(data), -1)); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("大小未知时应原样读出：%v", err)
	}
}

func TestCheckedReadCloser(t *testing.T) {
	rc := newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abc"))), 5)
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("数据短于声明长度应报错")
	}
	rc = newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abcdef"))), 5)
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("数据长于声明长度应报错")
	}
	rc = newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abcde"))), 5)
	if b, err := io.ReadAll(rc); err != nil || string(b) != "abcde" {
		t.Fatalf("长度正确应成功：%v", err)
	}
}

func TestOpenUnknownKind(t *testing.T) {
	if _, err := Open("nfs", []byte(`{}`), Deps{}); err == nil {
		t.Fatal("未知类型应报错")
	}
	if _, err := Open(KindLocal, nil, Deps{}); err == nil {
		t.Fatal("空配置应报错")
	}
	if _, err := Open(KindLocal, []byte(`{"path":`), Deps{}); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestKinds(t *testing.T) {
	want := []string{KindLocal, KindSFTP, KindWebDAV, KindFTP, KindSMB, KindS3, KindCOS, KindOSS, KindAzure, KindBaidu}
	ks := Kinds()
	if len(ks) != len(want) {
		t.Fatalf("目标类型 %d 种，期望 %d 种", len(ks), len(want))
	}
	for i, k := range ks {
		if k.ID != want[i] {
			t.Fatalf("第 %d 种是 %s，期望 %s", i, k.ID, want[i])
		}
		if k.Name == "" || k.NameEN == "" || len(k.Fields) == 0 {
			t.Fatalf("%s 缺少名称或字段", k.ID)
		}
		if k.Network != (k.ID != KindLocal) {
			t.Fatalf("%s 的 Network 不对", k.ID)
		}
		if k.AdminOnly != (k.ID == KindLocal) || k.OAuth != (k.ID == KindBaidu) {
			t.Fatalf("%s 的 AdminOnly / OAuth 不对", k.ID)
		}
		seen := map[string]bool{}
		for _, f := range k.Fields {
			if f.Key == "" || f.Label == "" || f.LabelEN == "" || seen[f.Key] {
				t.Fatalf("%s 的字段 %+v 不完整或重复", k.ID, f)
			}
			seen[f.Key] = true
			switch f.Type {
			case "text", "password", "number", "bool", "select", "textarea":
			default:
				t.Fatalf("%s.%s 的类型 %q 不对", k.ID, f.Key, f.Type)
			}
			if f.Type == "select" && len(f.Options) == 0 {
				t.Fatalf("%s.%s 没有选项", k.ID, f.Key)
			}
		}
		info, ok := Info(k.ID)
		if !ok || info.ID != k.ID {
			t.Fatalf("Info(%s) 找不到", k.ID)
		}
	}
	if _, ok := Info("nope"); ok {
		t.Fatal("未知类型 Info 应返回 false")
	}
	secrets := map[string][]string{
		KindSFTP:   {"password", "private_key", "passphrase"},
		KindWebDAV: {"password"},
		KindFTP:    {"password"},
		KindSMB:    {"password"},
		KindS3:     {"secret_key"},
		KindCOS:    {"secret_key"},
		KindOSS:    {"access_key_secret"},
		KindAzure:  {"key", "sas"},
		KindBaidu:  {"secret_key", "access_token", "refresh_token"},
	}
	for kind, keys := range secrets {
		info, _ := Info(kind)
		got := info.SecretKeys()
		sort.Strings(got)
		sort.Strings(keys)
		if !eqStrings(got, keys) {
			t.Errorf("%s 的密钥字段 %v，期望 %v", kind, got, keys)
		}
	}
	if info, _ := Info(KindLocal); len(info.SecretKeys()) != 0 {
		t.Error("本地目录不应有密钥字段")
	}
	// 返回的是副本，改了不影响下一次。
	ks[0].Fields[0].Label = "改掉"
	if info, _ := Info(ks[0].ID); info.Fields[0].Label == "改掉" {
		t.Fatal("Kinds 应返回副本")
	}
	// 每种目标的配置键都能被对应的配置结构体接住（防止表单键与 json 键对不上）。
	structs := map[string]any{KindLocal: LocalConfig{}, KindSFTP: SFTPConfig{}, KindWebDAV: WebDAVConfig{}, KindFTP: FTPConfig{},
		KindSMB: SMBConfig{}, KindS3: S3Config{}, KindCOS: COSConfig{}, KindOSS: OSSConfig{}, KindAzure: AzureConfig{}, KindBaidu: BaiduConfig{}}
	for _, k := range Kinds() {
		b, _ := json.Marshal(structs[k.ID])
		var m map[string]any
		json.Unmarshal(b, &m)
		for _, f := range k.Fields {
			if _, ok := m[f.Key]; !ok {
				t.Errorf("%s 的表单字段 %s 在配置结构体里没有对应的 json 键", k.ID, f.Key)
			}
		}
	}
}

// ---------------- 重试 ----------------

func TestRetryHelpers(t *testing.T) {
	for code, want := range map[int]bool{408: true, 429: true, 500: true, 502: true, 503: true, 504: true,
		501: false, 507: false, 400: false, 401: false, 403: false, 404: false, 409: false, 200: false} {
		if retryableStatus(code) != want {
			t.Errorf("retryableStatus(%d) 应为 %v", code, want)
		}
	}
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	if d, ok := parseRetryAfter("7", now); !ok || d != 7*time.Second {
		t.Errorf("Retry-After 秒数解析不对：%v %v", d, ok)
	}
	if d, ok := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); !ok || d != 90*time.Second {
		t.Errorf("Retry-After 日期解析不对：%v %v", d, ok)
	}
	if _, ok := parseRetryAfter("soon", now); ok {
		t.Error("无法解析的 Retry-After 应忽略")
	}

	// 用真实参数检查退避：起始 1 秒、翻倍、上限 30 秒、抖动在 [d/2, d]，Retry-After 优先。
	ob, om, oc := retryBaseDelay, retryMaxDelay, retryAfterCap
	retryBaseDelay, retryMaxDelay, retryAfterCap = time.Second, 30*time.Second, 5*time.Minute
	defer func() { retryBaseDelay, retryMaxDelay, retryAfterCap = ob, om, oc }()
	for attempt, full := range []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		for i := 0; i < 20; i++ {
			d := retryDelay(attempt, nil)
			if d < full/2 || d > full {
				t.Fatalf("第 %d 次重试等待 %v，应在 [%v, %v]", attempt, d, full/2, full)
			}
		}
	}
	resp := &http.Response{Header: http.Header{"Retry-After": {"45"}}}
	if d := retryDelay(0, resp); d != 45*time.Second {
		t.Fatalf("应遵守 Retry-After：%v", d)
	}
	resp.Header.Set("Retry-After", "86400")
	if d := retryDelay(0, resp); d != 5*time.Minute {
		t.Fatalf("Retry-After 太长时应封顶：%v", d)
	}
	if retryableNetErr(context.Canceled) || retryableNetErr(ErrSizeMismatch) {
		t.Fatal("取消与长度不符不应重试")
	}
	if !retryableNetErr(io.ErrUnexpectedEOF) {
		t.Fatal("连接中断应重试")
	}
	// 出站地址检查在拨号阶段拒绝（不是系统调用错误）：不重试，并把原因带给用户。
	blocked := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("目标地址是内网或保留地址，已拦截：10.0.0.1")}
	if retryableNetErr(blocked) {
		t.Fatal("被出站地址检查拒绝的连接不应重试")
	}
	if msg := netErrText(blocked); !strings.Contains(msg, "已拦截") {
		t.Fatalf("拦截原因应带给用户：%s", msg)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	if !retryableNetErr(refused) || !strings.Contains(netErrText(refused), "无法连接服务器") {
		t.Fatal("连接被拒绝应重试，并提示检查地址端口")
	}
}

func TestDoRetry(t *testing.T) {
	var calls atomic.Int64
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	resp, err := retryHTTP(ctx, srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPut, srv.URL, strings.NewReader("payload"))
	})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("先 503、429 再成功时应重试成功：%v", err)
	}
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 3 || bodies[0] != "payload" || bodies[2] != "payload" {
		t.Fatalf("应请求 3 次且每次请求体完整：%d %q", calls.Load(), bodies)
	}

	// 一直失败：重试 retryMaxRetries 次后返回最后的响应。
	calls.Store(0)
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer always.Close()
	resp, err = retryHTTP(ctx, always.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, always.URL, nil)
	})
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("重试用尽应返回最后的响应：%v", err)
	}
	resp.Body.Close()
	if calls.Load() != int64(retryMaxRetries)+1 {
		t.Fatalf("应请求 %d 次，实际 %d", retryMaxRetries+1, calls.Load())
	}

	// 4xx 不重试。
	calls.Store(0)
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer notFound.Close()
	resp, _ = retryHTTP(ctx, notFound.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, notFound.URL, nil)
	})
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("404 不应重试，实际请求 %d 次", calls.Load())
	}

	// retryTransport：不能重放的请求体（管道）不重试。
	calls.Store(0)
	rt := retryTransport{always.Client().Transport}
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte("x")); pw.Close() }()
	req, _ := http.NewRequest(http.MethodPut, always.URL, pr)
	if resp, err := rt.RoundTrip(req); err == nil {
		resp.Body.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("流式请求体不应重试，实际请求 %d 次", calls.Load())
	}
}

// ---------------- 通用一致性测试 ----------------

type suiteOpts struct {
	noEmptyFile bool // 百度网盘不支持空文件
	bigSize     int  // 大文件测试的大小（覆盖分片上传）
}

func mustPut(t *testing.T, tg Target, p string, data []byte) {
	t.Helper()
	if err := tg.Put(context.Background(), p, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Put(%q): %v", p, err)
	}
}

func mustGet(t *testing.T, tg Target, p string) []byte {
	t.Helper()
	rc, err := tg.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("Get(%q): %v", p, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Get(%q) 读取: %v", p, err)
	}
	return b
}

func listPaths(t *testing.T, tg Target, prefix string) []string {
	t.Helper()
	es, err := tg.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List(%q): %v", prefix, err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Path)
	}
	sort.Strings(out)
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runSuite(t *testing.T, tg Target, o suiteOpts) {
	ctx := context.Background()

	// 空目标
	if _, err := tg.Stat(ctx, "nope.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Stat 不存在的文件应返回 ErrNotExist，实际 %v", err)
	}
	if _, err := tg.Get(ctx, "nope/x.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Get 不存在的文件应返回 ErrNotExist，实际 %v", err)
	}
	if err := tg.Delete(ctx, "nope/x.txt"); err != nil {
		t.Fatalf("Delete 不存在的文件应成功，实际 %v", err)
	}
	if got := listPaths(t, tg, ""); len(got) != 0 {
		t.Fatalf("空目标 List 应为空，实际 %v", got)
	}
	if got := listPaths(t, tg, "nodir/"); len(got) != 0 {
		t.Fatalf("不存在的目录 List 应为空，实际 %v", got)
	}

	files := map[string][]byte{
		"a.txt":              []byte("hello"),
		"dir/b.bin":          randData(t, 100*1024+3),
		"dir/sub/c.bin":      randData(t, 7),
		"dir2/d.bin":         randData(t, 10),
		"中文 目录/文件 名+(1).txt": []byte("重要备份"),
	}
	if !o.noEmptyFile {
		files["empty.dat"] = nil
	}
	for p, d := range files {
		mustPut(t, tg, p, d)
	}
	for p, d := range files {
		e, err := tg.Stat(ctx, p)
		if err != nil {
			t.Fatalf("Stat(%q): %v", p, err)
		}
		if e.Path != p || e.Size != int64(len(d)) {
			t.Fatalf("Stat(%q) = %+v，期望大小 %d", p, e, len(d))
		}
		if got := mustGet(t, tg, p); !bytes.Equal(got, d) {
			t.Fatalf("Get(%q) 内容不一致", p)
		}
	}
	var all []string
	for p := range files {
		all = append(all, p)
	}
	sort.Strings(all)
	if got := listPaths(t, tg, ""); !eqStrings(got, all) {
		t.Fatalf("List(\"\") = %v，期望 %v", got, all)
	}
	es, _ := tg.List(ctx, "dir/b.bin")
	if len(es) != 1 || es[0].Size != int64(len(files["dir/b.bin"])) {
		t.Fatalf("List 返回的大小不对：%+v", es)
	}
	cases := map[string][]string{
		"dir/":     {"dir/b.bin", "dir/sub/c.bin"},
		"dir":      {"dir/b.bin", "dir/sub/c.bin", "dir2/d.bin"},
		"dir/sub/": {"dir/sub/c.bin"},
		"dir/s":    {"dir/sub/c.bin"},
		"dir/b":    {"dir/b.bin"},
		"di":       {"dir/b.bin", "dir/sub/c.bin", "dir2/d.bin"},
		"zzz/":     nil,
		"中文 目录/":   {"中文 目录/文件 名+(1).txt"},
	}
	for prefix, want := range cases {
		if got := listPaths(t, tg, prefix); !eqStrings(got, want) {
			t.Fatalf("List(%q) = %v，期望 %v", prefix, got, want)
		}
	}
	if n, b, err := Usage(ctx, tg, "dir/"); err != nil || n != 2 || b != int64(len(files["dir/b.bin"])+len(files["dir/sub/c.bin"])) {
		t.Fatalf("Usage(dir/) = %d, %d, %v", n, b, err)
	}

	// 覆盖写
	mustPut(t, tg, "a.txt", []byte("world, overwritten"))
	if got := mustGet(t, tg, "a.txt"); string(got) != "world, overwritten" {
		t.Fatalf("覆盖写后内容不对：%q", got)
	}

	// 长度不符：不能以正式文件名留下文件，也不能留下可见的临时文件。
	short := randData(t, 5000)
	if err := tg.Put(ctx, "dir/short.bin", bytes.NewReader(short), int64(len(short))+10); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据比声明短应报 ErrSizeMismatch，实际 %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/short.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
	}
	if err := tg.Put(ctx, "dir/long.bin", bytes.NewReader(short), int64(len(short))-10); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据比声明长应报 ErrSizeMismatch，实际 %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/long.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
	}
	if !o.noEmptyFile {
		if err := tg.Put(ctx, "dir/zero.bin", bytes.NewReader([]byte("x")), 0); !errors.Is(err, ErrSizeMismatch) {
			t.Fatalf("声明 0 字节却有数据应报 ErrSizeMismatch，实际 %v", err)
		}
		if _, err := tg.Stat(ctx, "dir/zero.bin"); !errors.Is(err, ErrNotExist) {
			t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
		}
	}
	// 覆盖写失败时旧文件保持原样。
	if err := tg.Put(ctx, "a.txt", bytes.NewReader(short), int64(len(short))+1); err == nil {
		t.Fatal("长度不符应报错")
	}
	if got := mustGet(t, tg, "a.txt"); string(got) != "world, overwritten" {
		t.Fatalf("覆盖写失败后旧文件被破坏：%q", got)
	}
	if got := listPaths(t, tg, "dir/"); !eqStrings(got, []string{"dir/b.bin", "dir/sub/c.bin"}) {
		t.Fatalf("失败的上传后 List(dir/) = %v", got)
	}

	// 大文件
	if o.bigSize > 0 {
		big := randData(t, o.bigSize)
		mustPut(t, tg, "big/big.bin", big)
		if got := mustGet(t, tg, "big/big.bin"); !bytes.Equal(got, big) {
			t.Fatal("大文件往返不一致")
		}
		if e, err := tg.Stat(ctx, "big/big.bin"); err != nil || e.Size != int64(len(big)) {
			t.Fatalf("大文件 Stat: %+v %v", e, err)
		}
		if err := tg.Delete(ctx, "big/big.bin"); err != nil {
			t.Fatal(err)
		}
	}

	// 删除
	if err := tg.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/b.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("删除后 Stat 应返回 ErrNotExist，实际 %v", err)
	}
	if err := tg.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatalf("重复删除应成功，实际 %v", err)
	}
	if got := listPaths(t, tg, "dir/"); !eqStrings(got, []string{"dir/sub/c.bin"}) {
		t.Fatalf("删除后 List(dir/) = %v", got)
	}
	// 目录不能被当成文件删掉。
	if err := tg.Delete(ctx, "dir/sub"); err != nil && !strings.Contains(err.Error(), "目录") {
		t.Fatalf("删除目录路径返回了意外错误：%v", err)
	}
	if got := mustGet(t, tg, "dir/sub/c.bin"); !bytes.Equal(got, files["dir/sub/c.bin"]) {
		t.Fatal("Delete 目录路径不能删掉目录里的文件")
	}
	if _, err := tg.Stat(ctx, "dir/sub"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Stat 目录应视为文件不存在，实际 %v", err)
	}

	// 越界路径一律拒绝
	for _, p := range badPaths {
		if err := tg.Put(ctx, p, bytes.NewReader([]byte("x")), 1); err == nil {
			t.Fatalf("Put(%q) 应被拒绝", p)
		}
		if _, err := tg.Get(ctx, p); err == nil || errors.Is(err, ErrNotExist) {
			t.Fatalf("Get(%q) 应被拒绝，实际 %v", p, err)
		}
		if _, err := tg.Stat(ctx, p); err == nil || errors.Is(err, ErrNotExist) {
			t.Fatalf("Stat(%q) 应被拒绝，实际 %v", p, err)
		}
		if err := tg.Delete(ctx, p); err == nil {
			t.Fatalf("Delete(%q) 应被拒绝", p)
		}
	}
	for _, p := range []string{"../", "/", "a//", `a\b`} {
		if _, err := tg.List(ctx, p); err == nil {
			t.Fatalf("List(%q) 应被拒绝", p)
		}
	}
	if err := tg.Put(ctx, "~$evil.tmp", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("Put 临时文件名应被拒绝")
	}

	// 测试连接
	if err := Probe(ctx, tg); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := listPaths(t, tg, probePrefix); len(got) != 0 {
		t.Fatalf("Probe 后应删除测试文件，实际 %v", got)
	}
}

// ---------------- 流式写入测试 ----------------

type streamOpts struct {
	bigSize    int  // 大小未知的大文件（应跨多个分片）
	abortAt    int  // NewUpload 写多少字节后放弃（应已经开始上传分片）
	noEmpty    bool // 不支持空文件（百度网盘）
	noLeftover func(t *testing.T)
}

// writeChunks 把 data 分成不等长的小段写进 w。
func writeChunks(w io.Writer, data []byte) error {
	sizes := []int{1, 7, 4096, 65536, 1 << 20, 333}
	for i := 0; len(data) > 0; i++ {
		n := sizes[i%len(sizes)]
		if n > len(data) {
			n = len(data)
		}
		if _, err := w.Write(data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// eventually 等待条件成立（服务器端处理中断的上传可能稍晚于客户端返回）。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runStreamSuite(t *testing.T, tg Target, o streamOpts) {
	t.Helper()
	ctx := context.Background()

	// 1. size = -1 的流式写入：大文件（跨分片）与小文件（第一片就结束）。
	big := randData(t, o.bigSize)
	if err := tg.Put(ctx, "stream/big.bin", bytes.NewReader(big), -1); err != nil {
		t.Fatalf("流式写入大文件失败：%v", err)
	}
	if got := mustGet(t, tg, "stream/big.bin"); !bytes.Equal(got, big) {
		t.Fatal("流式写入的大文件读回不一致")
	}
	if e, err := tg.Stat(ctx, "stream/big.bin"); err != nil || e.Size != int64(len(big)) {
		t.Fatalf("流式写入的大文件 Stat: %+v %v", e, err)
	}
	small := randData(t, 1000)
	if err := tg.Put(ctx, "stream/small.bin", iotest.HalfReader(bytes.NewReader(small)), -1); err != nil {
		t.Fatalf("流式写入小文件失败：%v", err)
	}
	if got := mustGet(t, tg, "stream/small.bin"); !bytes.Equal(got, small) {
		t.Fatal("流式写入的小文件读回不一致")
	}
	if !o.noEmpty {
		if err := tg.Put(ctx, "stream/empty.bin", bytes.NewReader(nil), -1); err != nil {
			t.Fatalf("流式写入空文件失败：%v", err)
		}
		if e, err := tg.Stat(ctx, "stream/empty.bin"); err != nil || e.Size != 0 {
			t.Fatalf("流式写入的空文件 Stat: %+v %v", e, err)
		}
	} else if err := tg.Put(ctx, "stream/empty.bin", bytes.NewReader(nil), -1); err == nil {
		t.Fatal("不支持空文件的目标流式写入空数据应报错")
	}

	// 2. NewUpload：分段写入后 Commit，内容一致。
	data := randData(t, o.bigSize/2+12345)
	u := NewUpload(ctx, tg, "stream/upload.bin")
	if err := writeChunks(u, data); err != nil {
		t.Fatalf("NewUpload 写入失败：%v", err)
	}
	if u.Written() != int64(len(data)) {
		t.Fatalf("Written() = %d，期望 %d", u.Written(), len(data))
	}
	if err := u.Commit(); err != nil {
		t.Fatalf("NewUpload Commit 失败：%v", err)
	}
	if got := mustGet(t, tg, "stream/upload.bin"); !bytes.Equal(got, data) {
		t.Fatal("NewUpload Commit 后内容不一致")
	}

	// 3. NewUpload 写一半后 Abort：不留下正式文件。
	u = NewUpload(ctx, tg, "stream/aborted.bin")
	if err := writeChunks(u, randData(t, o.abortAt)); err != nil {
		t.Fatalf("放弃前的写入失败：%v", err)
	}
	cause := errors.New("引擎放弃了这次备份")
	u.Abort(cause)
	if u.Err() == nil {
		t.Fatal("Abort 后 Err() 应返回错误")
	}
	if _, err := tg.Stat(ctx, "stream/aborted.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Abort 后不能留下正式文件，Stat 返回 %v", err)
	}

	// 4. 覆盖已有文件时 Abort：旧文件保持原样。
	u = NewUpload(ctx, tg, "stream/upload.bin")
	if err := writeChunks(u, randData(t, o.abortAt)); err != nil {
		t.Fatalf("放弃前的写入失败：%v", err)
	}
	u.Abort(nil)
	if got := mustGet(t, tg, "stream/upload.bin"); !bytes.Equal(got, data) {
		t.Fatal("覆盖写被放弃后旧文件被破坏")
	}

	// 5. 数据源中途出错（Put 直接调用）：返回该错误，不留下文件。
	boom := errors.New("数据库导出中断")
	r := io.MultiReader(bytes.NewReader(randData(t, o.abortAt)), iotest.ErrReader(boom))
	if err := tg.Put(ctx, "stream/broken.bin", r, -1); !errors.Is(err, boom) {
		t.Fatalf("数据源出错时应返回该错误，实际 %v", err)
	}
	if _, err := tg.Stat(ctx, "stream/broken.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("数据源出错后不能留下文件，Stat 返回 %v", err)
	}

	// 6. ctx 取消：返回取消错误，不留下文件。
	cctx, cancel := context.WithCancel(ctx)
	u = NewUpload(cctx, tg, "stream/canceled.bin")
	if err := writeChunks(u, randData(t, o.abortAt)); err != nil {
		t.Fatalf("取消前的写入失败：%v", err)
	}
	cancel()
	u.Abort(context.Canceled)
	if u.Err() == nil {
		t.Fatal("取消后 Err() 应返回错误")
	}
	if _, err := tg.Stat(ctx, "stream/canceled.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("取消后不能留下正式文件，Stat 返回 %v", err)
	}

	want := []string{"stream/big.bin", "stream/small.bin", "stream/upload.bin"}
	if !o.noEmpty {
		want = []string{"stream/big.bin", "stream/empty.bin", "stream/small.bin", "stream/upload.bin"}
	}
	if got := listPaths(t, tg, "stream/"); !eqStrings(got, want) {
		t.Fatalf("流式测试后 List(stream/) = %v，期望 %v", got, want)
	}
	if o.noLeftover != nil {
		o.noLeftover(t)
	}
	for _, p := range want {
		if err := tg.Delete(ctx, p); err != nil {
			t.Fatalf("Delete(%q): %v", p, err)
		}
	}
}
