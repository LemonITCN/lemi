package target

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/net/webdav"
)

type davServer struct {
	srv         *httptest.Server
	dir         string
	noOverwrite bool // 模拟不支持覆盖式 MOVE 的服务器
	puts        atomic.Int64
	chunked     atomic.Int64
	noChunked   atomic.Bool  // 模拟坚果云：拒绝 chunked 上传（411）
	failEach    atomic.Int64 // 接下来每种非 PUT 方法的前几次请求返回 503（测试重试）
	failMu      sync.Mutex
	failed      map[string]int
}

// countingBody 记录请求体读到的字节数与错误。
type countingBody struct {
	io.ReadCloser
	n   int64
	err error
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

func startDAV(t *testing.T, noOverwrite bool) *davServer {
	t.Helper()
	ds := &davServer{dir: t.TempDir(), noOverwrite: noOverwrite, failed: map[string]int{}}
	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(ds.dir), LockSystem: webdav.NewMemLS()}
	ds.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "口令 pw" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n := ds.failEach.Load(); n > 0 && r.Method != http.MethodPut {
			ds.failMu.Lock()
			fail := ds.failed[r.Method] < int(n)
			if fail {
				ds.failed[r.Method]++
			}
			ds.failMu.Unlock()
			if fail {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		if r.Method == http.MethodPut {
			ds.puts.Add(1)
			if r.ContentLength < 0 || len(r.TransferEncoding) > 0 {
				ds.chunked.Add(1) // 坚果云不接受 chunked 上传
				if ds.noChunked.Load() {
					io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusLengthRequired)
					return
				}
			}
		}
		if ds.noOverwrite && r.Method == "MOVE" {
			if dst, err := url.Parse(r.Header.Get("Destination")); err == nil {
				rel := strings.TrimPrefix(dst.Path, "/dav")
				if _, err := os.Stat(filepath.Join(ds.dir, filepath.FromSlash(rel))); err == nil {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			}
		}
		if r.Method == http.MethodPut {
			// 像真实的 WebDAV 服务器一样丢弃没传完的上传。x/net/webdav 会留下半个文件；客户端中断上传后
			// 立刻发出的 DELETE 可能先于这里的处理到达，测试检查残留临时文件时就会偶发失败。
			body := &countingBody{ReadCloser: r.Body}
			r.Body = body
			h.ServeHTTP(w, r)
			if body.err != nil || (r.ContentLength >= 0 && body.n != r.ContentLength) {
				os.Remove(filepath.Join(ds.dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/dav"))))
			}
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ds.srv.Close)
	return ds
}

func openDAV(t *testing.T, ds *davServer, user, pw, prefix string) Target {
	return openDAVSpool(t, ds, user, pw, prefix, false, "")
}

func openDAVSpool(t *testing.T, ds *davServer, user, pw, prefix string, spool bool, spoolDir string) Target {
	t.Helper()
	cfg, _ := json.Marshal(WebDAVConfig{URL: ds.srv.URL + "/dav/", Username: user, Password: pw, Prefix: prefix, Spool: spool})
	tg, err := Open(KindWebDAV, cfg, Deps{SpoolDir: spoolDir})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestWebDAVSuite(t *testing.T) {
	ds := startDAV(t, false)
	runSuite(t, openDAV(t, ds, "user", "口令 pw", "备份/数据 1"), suiteOpts{bigSize: 3<<20 + 7})
	if ds.chunked.Load() != 0 {
		t.Fatalf("大小已知时有 %d 次 PUT 用了 chunked 传输", ds.chunked.Load())
	}
	// 文件确实落在 prefix 子目录下，且没有残留临时文件。
	if _, err := os.Stat(filepath.Join(ds.dir, "备份", "数据 1", "dir", "sub", "c.bin")); err != nil {
		t.Fatalf("文件不在 prefix 子目录下：%v", err)
	}
	filepath.WalkDir(ds.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("残留临时文件：%s", p)
		}
		return nil
	})
}

// 大小未知：默认分块传输直接 PUT 到临时名。
func TestWebDAVStreamChunked(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "口令 pw", "流式")
	runStreamSuite(t, tg, streamOpts{bigSize: 3<<20 + 9, abortAt: 1 << 20, noLeftover: func(t *testing.T) { noTempFiles(t, ds.dir) }})
	if ds.chunked.Load() == 0 {
		t.Fatal("大小未知时应使用分块传输")
	}
	noTempFiles(t, ds.dir)
}

// 勾选「先暂存」：大小未知时先落到 SpoolDir，再带 Content-Length 上传（可重试），用完删除暂存文件。
func TestWebDAVStreamSpool(t *testing.T) {
	ds := startDAV(t, false)
	ds.noChunked.Store(true)
	spool := t.TempDir()
	tg := openDAVSpool(t, ds, "user", "口令 pw", "", true, spool)
	runStreamSuite(t, tg, streamOpts{bigSize: 2<<20 + 3, abortAt: 1 << 20, noLeftover: func(t *testing.T) {
		noTempFiles(t, ds.dir)
		if ents, _ := os.ReadDir(spool); len(ents) != 0 {
			t.Fatalf("暂存目录里有残留文件：%v", ents)
		}
	}})
	if ds.chunked.Load() != 0 {
		t.Fatalf("先暂存模式不应使用分块传输，实际 %d 次", ds.chunked.Load())
	}
	// 不勾选时，拒绝 chunked 的服务器给出明确提示。
	tg2 := openDAV(t, ds, "user", "口令 pw", "")
	err := tg2.Put(context.Background(), "x.bin", strings.NewReader("abc"), -1)
	if err == nil || !strings.Contains(err.Error(), "先暂存") {
		t.Fatalf("服务器拒绝 chunked 时应提示勾选先暂存，实际 %v", err)
	}
	noTempFiles(t, ds.dir)
}

// MKCOL、MOVE、PROPFIND、DELETE、GET 先 503 两次再成功：自动重试。
func TestWebDAVRetry(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "口令 pw", "r")
	ds.failEach.Store(2)
	mustPut(t, tg, "a/b/c.txt", []byte("retry"))
	if got := mustGet(t, tg, "a/b/c.txt"); string(got) != "retry" {
		t.Fatalf("重试后内容不对：%q", got)
	}
	if got := listPaths(t, tg, ""); !eqStrings(got, []string{"a/b/c.txt"}) {
		t.Fatalf("重试后 List = %v", got)
	}
	if err := tg.Delete(context.Background(), "a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	ds.failMu.Lock()
	defer ds.failMu.Unlock()
	for _, m := range []string{"MKCOL", "MOVE", "PROPFIND", "GET", "DELETE"} {
		if ds.failed[m] != 2 {
			t.Errorf("%s 应先失败 2 次，实际 %d", m, ds.failed[m])
		}
	}
}

func TestWebDAVNoOverwriteMove(t *testing.T) {
	ds := startDAV(t, true)
	tg := openDAV(t, ds, "user", "口令 pw", "")
	mustPut(t, tg, "m/a.txt", []byte("one"))
	mustPut(t, tg, "m/a.txt", []byte("two"))
	if got := mustGet(t, tg, "m/a.txt"); string(got) != "two" {
		t.Fatalf("不支持覆盖式 MOVE 时覆盖写失败：%q", got)
	}
}

func TestWebDAVWrongPassword(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "wrong", "")
	err := Probe(context.Background(), tg)
	if err == nil || !strings.Contains(err.Error(), "用户名或口令不正确") {
		t.Fatalf("口令错误应提示「用户名或口令不正确」，实际 %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("错误信息泄露了口令")
	}
	if _, err := tg.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "用户名或口令不正确") {
		t.Fatalf("List 口令错误提示不对：%v", err)
	}
}

func TestWebDAVDirCacheAvoidsMkcol(t *testing.T) {
	ds := startDAV(t, false)
	var mkcol atomic.Int64
	inner := ds.srv.Config.Handler
	ds.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MKCOL" {
			mkcol.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	tg := openDAV(t, ds, "user", "口令 pw", "p")
	for i := 0; i < 5; i++ {
		mustPut(t, tg, "same/dir/f"+string(rune('a'+i)), []byte("x"))
	}
	if n := mkcol.Load(); n > 4 { // 第一次：建最深一级失败 1 次 + 逐级 3 次
		t.Fatalf("同一目录多次上传发了 %d 次 MKCOL，应只建一次", n)
	}
}

func TestWebDAVConfigErrors(t *testing.T) {
	bad := []WebDAVConfig{
		{URL: ""},
		{URL: "ftp://x/"},
		{URL: "https://user:pw@x/dav"},
		{URL: "https://x/dav?a=1"},
		{URL: "https://x/dav", Prefix: "../up"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindWebDAV, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
}

func TestWebDAVCanceled(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "口令 pw", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tg.List(ctx, ""); err == nil {
		t.Fatal("取消后 List 应报错")
	}
	if err := tg.Put(ctx, "c.txt", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("取消后 Put 应报错")
	}
}
