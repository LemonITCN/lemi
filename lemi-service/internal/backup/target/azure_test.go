package target

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAzure 是测试用的最小 Azure Blob 服务：Put Blob、Put Block、Put Block List、Get/HEAD/DELETE Blob、List Blobs。
// 每个请求都用测试里独立写的一份 Shared Key 规则重算签名并校验（或校验 SAS 令牌）。
type fakeAzure struct {
	t         *testing.T
	account   string
	key       []byte
	base      string // 端点里的路径前缀（模拟 Azurite 的 /<账户>）
	container string
	sas       string // 非空时只接受带这个 sig 的 SAS 请求

	mu          sync.Mutex
	blobs       map[string][]byte
	mtimes      map[string]time.Time
	uncommitted map[string]map[string][]byte
	idLen       map[string]int
	badSigs     int
	md5Seen     int
	blockPuts   int
	blockLists  int
	failBlocks  int // 接下来这么多个 Put Block 轮流返回 503 / 429
	failed      int
	srv         *httptest.Server
}

func newFakeAzure(t *testing.T, base string) *fakeAzure {
	f := &fakeAzure{t: t, account: "lemitest", key: []byte("azure-test-key-not-a-real-secret"), base: base, container: "backups",
		blobs: map[string][]byte{}, mtimes: map[string]time.Time{}, uncommitted: map[string]map[string][]byte{}, idLen: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAzure) config() AzureConfig {
	return AzureConfig{Account: f.account, Key: base64.StdEncoding.EncodeToString(f.key), Container: f.container,
		Endpoint: f.srv.URL + f.base}
}

func (f *fakeAzure) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("x-ms-error-code", code)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\"?><Error><Code>%s</Code><Message>%s\nRequestId:1\nTime:2026-11-26</Message></Error>", code, code)
}

// testSharedKey 独立按微软文档拼待签名串（与被测代码分开写，防止两边犯同样的错）。
func (f *fakeAzure) testSharedKey(r *http.Request) string {
	cl := ""
	if r.ContentLength > 0 {
		cl = strconv.FormatInt(r.ContentLength, 10)
	}
	var lines []string
	lines = append(lines, r.Method)
	for _, h := range []string{"Content-Encoding", "Content-Language"} {
		lines = append(lines, r.Header.Get(h))
	}
	lines = append(lines, cl)
	for _, h := range []string{"Content-MD5", "Content-Type", "Date", "If-Modified-Since", "If-Match", "If-None-Match", "If-Unmodified-Since", "Range"} {
		lines = append(lines, r.Header.Get(h))
	}
	var xms []string
	for k := range r.Header {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-ms-") {
			xms = append(xms, lk+":"+strings.TrimSpace(r.Header.Get(k)))
		}
	}
	sort.Strings(xms)
	sts := strings.Join(lines, "\n") + "\n"
	for _, h := range xms {
		sts += h + "\n"
	}
	sts += "/" + f.account + r.URL.EscapedPath()
	q := r.URL.Query()
	var names []string
	for k := range q {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	for _, k := range names {
		vs := q[k]
		sort.Strings(vs)
		sts += "\n" + k + ":" + strings.Join(vs, ",")
	}
	m := hmac.New(sha256.New, f.key)
	m.Write([]byte(sts))
	return "SharedKey " + f.account + ":" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (f *fakeAzure) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("x-ms-version") != azureAPIVersion || r.Header.Get("x-ms-date") == "" {
		f.fail(w, 400, "MissingRequiredHeader")
		return
	}
	if f.sas != "" {
		if r.URL.Query().Get("sig") != f.sas || r.Header.Get("Authorization") != "" {
			f.badSigs++
			f.fail(w, 403, "AuthenticationFailed")
			return
		}
	} else if r.Header.Get("Authorization") != f.testSharedKey(r) {
		f.badSigs++
		f.fail(w, 403, "AuthenticationFailed")
		return
	}
	p := strings.TrimPrefix(r.URL.Path, f.base+"/")
	container, blob, _ := strings.Cut(p, "/")
	if container != f.container {
		f.fail(w, 404, "ContainerNotFound")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		f.fail(w, 400, "InvalidInput")
		return
	}
	if cm := r.Header.Get("Content-MD5"); cm != "" {
		f.md5Seen++
		s := md5.Sum(body)
		if base64.StdEncoding.EncodeToString(s[:]) != cm {
			f.fail(w, 400, "Md5Mismatch")
			return
		}
	}
	q := r.URL.Query()
	switch {
	case blob == "" && r.Method == http.MethodGet && q.Get("restype") == "container" && q.Get("comp") == "list":
		f.list(w, q)
	case r.Method == http.MethodPut && q.Get("comp") == "block":
		if f.failBlocks > 0 {
			f.failBlocks--
			f.failed++
			if f.failed%2 == 1 {
				f.fail(w, 503, "ServerBusy")
			} else {
				w.Header().Set("Retry-After", "0")
				f.fail(w, 429, "ServerBusy")
			}
			return
		}
		raw, err := base64.StdEncoding.DecodeString(q.Get("blockid"))
		if err != nil || len(raw) > 64 {
			f.fail(w, 400, "InvalidQueryParameterValue")
			return
		}
		id := q.Get("blockid")
		if l, ok := f.idLen[blob]; ok && l != len(id) && len(f.uncommitted[blob]) > 0 {
			f.fail(w, 400, "InvalidBlobOrBlock")
			return
		}
		f.idLen[blob] = len(id)
		if f.uncommitted[blob] == nil {
			f.uncommitted[blob] = map[string][]byte{}
		}
		f.uncommitted[blob][id] = body
		f.blockPuts++
		w.WriteHeader(201)
	case r.Method == http.MethodPut && q.Get("comp") == "blocklist":
		var bl struct {
			Latest []string `xml:"Latest"`
		}
		if err := xml.Unmarshal(body, &bl); err != nil || len(bl.Latest) == 0 {
			f.fail(w, 400, "InvalidXmlDocument")
			return
		}
		var all []byte
		for _, id := range bl.Latest {
			d, ok := f.uncommitted[blob][id]
			if !ok {
				f.fail(w, 400, "InvalidBlockList")
				return
			}
			all = append(all, d...)
		}
		delete(f.uncommitted, blob)
		delete(f.idLen, blob)
		f.blobs[blob] = all
		f.mtimes[blob] = time.Now()
		f.blockLists++
		w.WriteHeader(201)
	case r.Method == http.MethodPut && blob != "":
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
			f.fail(w, 400, "MissingRequiredHeader")
			return
		}
		f.blobs[blob] = body
		f.mtimes[blob] = time.Now()
		delete(f.uncommitted, blob)
		w.WriteHeader(201)
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && blob != "":
		d, ok := f.blobs[blob]
		if !ok {
			if r.Method == http.MethodHead {
				w.Header().Set("x-ms-error-code", "BlobNotFound")
				w.WriteHeader(404)
				return
			}
			f.fail(w, 404, "BlobNotFound")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(d)))
		w.Header().Set("Last-Modified", f.mtimes[blob].UTC().Format(http.TimeFormat))
		w.Header().Set("x-ms-blob-type", "BlockBlob")
		if r.Method == http.MethodGet {
			w.Write(d)
		}
	case r.Method == http.MethodDelete && blob != "":
		if _, ok := f.blobs[blob]; !ok {
			f.fail(w, 404, "BlobNotFound")
			return
		}
		delete(f.blobs, blob)
		w.WriteHeader(202)
	default:
		f.fail(w, 400, "UnsupportedHttpVerb")
	}
}

func (f *fakeAzure) list(w http.ResponseWriter, q url.Values) {
	prefix := q.Get("prefix")
	max, _ := strconv.Atoi(q.Get("maxresults"))
	if max <= 0 || max > 3 {
		max = 3 // 故意很小，覆盖分页
	}
	var names []string
	for k := range f.blobs {
		if strings.HasPrefix(k, prefix) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if m := q.Get("marker"); m != "" {
		i := sort.SearchStrings(names, m)
		names = names[i:]
	}
	next := ""
	if len(names) > max {
		next = names[max]
		names = names[:max]
	}
	var b bytes.Buffer
	b.WriteString("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\"?><EnumerationResults ServiceEndpoint=\"x\" ContainerName=\"" + f.container + "\"><Prefix>")
	xml.EscapeText(&b, []byte(prefix))
	b.WriteString("</Prefix><Blobs>")
	for _, n := range names {
		b.WriteString("<Blob><Name>")
		xml.EscapeText(&b, []byte(n))
		fmt.Fprintf(&b, "</Name><Properties><Last-Modified>%s</Last-Modified><Content-Length>%d</Content-Length><BlobType>BlockBlob</BlobType></Properties><Metadata /></Blob>",
			f.mtimes[n].UTC().Format(http.TimeFormat), len(f.blobs[n]))
	}
	b.WriteString("</Blobs><NextMarker>")
	xml.EscapeText(&b, []byte(next))
	b.WriteString("</NextMarker></EnumerationResults>")
	w.Header().Set("Content-Type", "application/xml")
	w.Write(b.Bytes())
}

func (f *fakeAzure) snapshot() (blobs []string, uncommitted, badSigs, blockLists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.blobs {
		blobs = append(blobs, k)
	}
	sort.Strings(blobs)
	for _, m := range f.uncommitted {
		uncommitted += len(m)
	}
	return blobs, uncommitted, f.badSigs, f.blockLists
}

func openAzureT(t *testing.T, c AzureConfig) Target {
	t.Helper()
	b, _ := json.Marshal(c)
	tg, err := Open(KindAzure, b, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestAzureSuite(t *testing.T) {
	f := newFakeAzure(t, "/lemitest") // 路径风格端点（像 Azurite），规范化资源里账户名出现两次
	c := f.config()
	c.Prefix = "Lemi/备份 1"
	tg := openAzureT(t, c)
	runSuite(t, tg, suiteOpts{bigSize: int(azureBlockSize)*3 + 77})
	blobs, _, badSigs, blockLists := f.snapshot()
	if badSigs != 0 {
		t.Fatalf("有 %d 个请求签名校验失败", badSigs)
	}
	if blockLists == 0 {
		t.Fatal("大文件应分块上传并提交块列表")
	}
	for _, k := range blobs {
		if !strings.HasPrefix(k, "Lemi/备份 1/") {
			t.Errorf("blob %q 不在 prefix 下", k)
		}
	}
	if f.md5Seen == 0 {
		t.Error("上传没有带 Content-MD5")
	}
}

func TestAzureStream(t *testing.T) {
	f := newFakeAzure(t, "")
	tg := openAzureT(t, f.config())
	runStreamSuite(t, tg, streamOpts{
		bigSize: int(azureBlockSize)*3 + 5,
		abortAt: int(azureBlockSize)*2 + 9,
		noLeftover: func(t *testing.T) {
			blobs, uncommitted, badSigs, _ := f.snapshot()
			for _, b := range blobs {
				if strings.Contains(b, "aborted") || strings.Contains(b, "broken") || strings.Contains(b, "canceled") {
					t.Fatalf("中途出错的上传被提交了：%s", b)
				}
			}
			if uncommitted == 0 {
				t.Fatal("中途放弃时已上传的块应保持未提交状态")
			}
			if badSigs != 0 {
				t.Fatalf("有 %d 个请求签名校验失败", badSigs)
			}
		},
	})
	// 块 ID 等长：同一个 blob 上放弃过一次后再上传不会因为 ID 长度不同被拒。
	data := randData(t, int(azureBlockSize)*2+1)
	u := NewUpload(context.Background(), tg, "again.bin")
	writeChunks(u, data[:azureBlockSize+10])
	u.Abort(nil)
	if err := tg.Put(context.Background(), "again.bin", bytes.NewReader(data), -1); err != nil {
		t.Fatalf("放弃后重新上传失败：%v", err)
	}
	if got := mustGet(t, tg, "again.bin"); !bytes.Equal(got, data) {
		t.Fatal("重新上传后内容不一致")
	}
}

func TestAzureRetry(t *testing.T) {
	f := newFakeAzure(t, "")
	tg := openAzureT(t, f.config())
	f.mu.Lock()
	f.failBlocks = 2 // 先 503 再 429
	f.mu.Unlock()
	data := randData(t, int(azureBlockSize)*2+3)
	if err := tg.Put(context.Background(), "r.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("块上传失败后应重试成功：%v", err)
	}
	if got := mustGet(t, tg, "r.bin"); !bytes.Equal(got, data) {
		t.Fatal("重试后内容不一致")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed != 2 {
		t.Fatalf("应先失败 2 次，实际 %d", f.failed)
	}
}

func TestAzureSAS(t *testing.T) {
	f := newFakeAzure(t, "")
	f.sas = "c2lnbmF0dXJl"
	c := f.config()
	c.Key = ""
	c.SAS = "?sv=2024-11-04&ss=b&srt=co&sp=rwdlac&se=2030-01-01T00:00:00Z&sig=" + url.QueryEscape(f.sas)
	tg := openAzureT(t, c)
	if err := Probe(context.Background(), tg); err != nil {
		t.Fatalf("SAS 认证 Probe 失败：%v", err)
	}
	mustPut(t, tg, "a b/c.bin", randData(t, int(azureBlockSize)+1))
	if got := listPaths(t, tg, ""); !eqStrings(got, []string{"a b/c.bin"}) {
		t.Fatalf("SAS 认证 List = %v", got)
	}
	if _, _, bad, _ := f.snapshot(); bad != 0 {
		t.Fatalf("有 %d 个 SAS 请求被拒", bad)
	}
}

func TestAzureErrors(t *testing.T) {
	f := newFakeAzure(t, "")
	c := f.config()
	c.Key = base64.StdEncoding.EncodeToString([]byte("wrong-key"))
	err := Probe(context.Background(), openAzureT(t, c))
	if err == nil || !strings.Contains(err.Error(), "身份验证失败") {
		t.Fatalf("密钥错误提示不对：%v", err)
	}
	c = f.config()
	c.Container = "missing"
	_, err = openAzureT(t, c).List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "容器") {
		t.Fatalf("容器不存在提示不对：%v", err)
	}
	bad := []AzureConfig{
		{Account: "UPPER", Key: "a2V5", Container: "c1c"},
		{Account: "acct", Key: "a2V5", Container: "x"},
		{Account: "acct", Key: "a2V5", Container: "bad--name"},
		{Account: "acct", Container: "cont"},
		{Account: "acct", Key: "不是base64", Container: "cont"},
		{Account: "acct", SAS: "sv=1", Container: "cont"},
		{Account: "acct", Key: "a2V5", Container: "cont", Endpoint: "ftp://x"},
		{Account: "acct", Key: "a2V5", Container: "cont", Prefix: "../x"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindAzure, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
	// 默认端点与中国区端点。
	tg := openAzureT(t, AzureConfig{Account: "acct", Key: "a2V5", Container: "cont"}).(*azureTarget)
	if tg.scheme != "https" || tg.host != "acct.blob.core.windows.net" || tg.basePath != "" {
		t.Fatalf("默认端点不对：%s://%s%s", tg.scheme, tg.host, tg.basePath)
	}
	tg = openAzureT(t, AzureConfig{Account: "acct", Key: "a2V5", Container: "cont", Endpoint: "acct.blob.core.chinacloudapi.cn"}).(*azureTarget)
	if tg.scheme != "https" || tg.host != "acct.blob.core.chinacloudapi.cn" {
		t.Fatalf("中国区端点不对：%s://%s", tg.scheme, tg.host)
	}
}

// 用微软文档「Authorize with Shared Key」里的格式手工核对一个待签名串。
func TestAzureStringToSign(t *testing.T) {
	tg := openAzureT(t, AzureConfig{Account: "myaccount", Key: "a2V5", Container: "mycontainer"}).(*azureTarget)
	tg.now = func() time.Time { return time.Date(2026, 11, 26, 8, 49, 37, 0, time.UTC) }
	req, err := tg.newReq(context.Background(), http.MethodPut, "a b/文件.bin", url.Values{"comp": {"block"}, "blockid": {"QUJD+/=="}},
		http.Header{"Content-Md5": {"bWQ1"}, "X-Ms-Meta-Note": {"  two   spaces "}}, []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := "PUT\n\n\n3\nbWQ1\n\n\n\n\n\n\n\n" +
		"x-ms-date:Thu, 26 Nov 2026 08:49:37 GMT\nx-ms-meta-note:two spaces\nx-ms-version:" + azureAPIVersion + "\n" +
		"/myaccount/mycontainer/a%20b/%E6%96%87%E4%BB%B6.bin\nblockid:QUJD+/==\ncomp:block"
	_, esc := tg.resource("a b/文件.bin")
	if got := azureStringToSign(req, "myaccount", esc, url.Values{"comp": {"block"}, "blockid": {"QUJD+/=="}}); got != want {
		t.Fatalf("待签名串不对：\n%q\n期望\n%q", got, want)
	}
	if req.URL.EscapedPath() != "/mycontainer/a%20b/%E6%96%87%E4%BB%B6.bin" || !strings.Contains(req.URL.RawQuery, "blockid=QUJD%2B%2F%3D%3D") {
		t.Fatalf("请求地址编码不对：%s", req.URL)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "SharedKey myaccount:") {
		t.Fatalf("Authorization 不对：%s", req.Header.Get("Authorization"))
	}
	// 空请求体：Content-Length 一行是空串。
	req, _ = tg.newReq(context.Background(), http.MethodPut, "x", nil, nil, []byte{})
	if sts := azureStringToSign(req, "myaccount", "/mycontainer/x", nil); !strings.HasPrefix(sts, "PUT\n\n\n\n") {
		t.Fatalf("Content-Length 为 0 时应为空串：%q", sts)
	}
}

// 真实 Azurite（或 Azure）测试：设置 LEMI_TEST_AZURE_ENDPOINT / ACCOUNT / KEY / CONTAINER 时运行。
func TestAzureReal(t *testing.T) {
	ep := os.Getenv("LEMI_TEST_AZURE_ENDPOINT")
	if ep == "" {
		t.Skip("未设置 LEMI_TEST_AZURE_ENDPOINT，跳过真实 Azure Blob 测试")
	}
	c := AzureConfig{Endpoint: ep, Account: os.Getenv("LEMI_TEST_AZURE_ACCOUNT"), Key: os.Getenv("LEMI_TEST_AZURE_KEY"),
		Container: os.Getenv("LEMI_TEST_AZURE_CONTAINER"), Prefix: "lemi-test-" + randomHex(4)}
	tg := openAzureT(t, c)
	at := tg.(*azureTarget)
	ctx := context.Background()
	// 建容器（已存在返回 409，忽略）。
	resp, err := retryHTTP(ctx, at.client, func() (*http.Request, error) {
		return at.newReq(ctx, http.MethodPut, "", url.Values{"restype": {"container"}}, nil, []byte{})
	})
	if err != nil {
		t.Fatalf("创建容器失败：%v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 409 {
		t.Fatalf("创建容器返回 HTTP %d", resp.StatusCode)
	}
	runSuite(t, tg, suiteOpts{bigSize: int(azureBlockSize)*3 + 77})
	runStreamSuite(t, tg, streamOpts{bigSize: int(azureBlockSize)*3 + 5, abortAt: int(azureBlockSize)*2 + 9})
	// 清理套件留下的文件。
	for _, p := range listPaths(t, tg, "") {
		if err := tg.Delete(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if got := listPaths(t, tg, ""); len(got) != 0 {
		t.Fatalf("清理后仍有文件：%v", got)
	}
}
