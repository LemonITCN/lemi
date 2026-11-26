package target

// Azure Blob 用 REST 接口手写实现，不引入 Azure SDK。认证用 Shared Key（按微软文档「Authorize with Shared Key」
// 逐条实现规范化）或 SAS 令牌（填了 SAS 就不用 Shared Key，令牌原样附在每个请求的查询串上）。
// 写入：数据不超过一块（azureBlockSize）时直接 Put Blob；否则 Put Block（每块在内存里缓冲、带 Content-MD5、
// 失败重试）→ 全部成功后 Put Block List 提交。数据源出错或上传失败时不提交，未提交的块服务端 7 天后自动清理，
// 正式文件名下不会出现半个文件（同名旧文件保持原样）。块 ID 等长：<16 位随机串>-<8 位序号> 再 base64。
// x-ms-version 用 2024-11-04（2026-10 查证「Previous Azure Storage versions」仍在支持列表里；
// 中国区等较晚部署新版本的云也早已支持，Azurite 3.33+ 也认）。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// azureAPIVersion 是请求头 x-ms-version 的值。
const azureAPIVersion = "2024-11-04"

// azureBlockSize 是每块的大小（在内存里缓冲以便校验和重试）。测试里改小。
var azureBlockSize int64 = 16 << 20

const (
	azureMaxBlocks = 50000
	azureListMax   = 5000
)

// AzureConfig 是 Azure Blob 存储的配置。
type AzureConfig struct {
	Account   string `json:"account"`   // 存储账户名
	Key       string `json:"key"`       // 访问密钥（base64）；填了 SAS 时可以不填
	Container string `json:"container"` // 容器名，必须已经存在
	Prefix    string `json:"prefix"`    // 备份放在容器里的这个子目录下
	// Endpoint 可选，缺省 https://<account>.blob.core.windows.net。中国区填 https://<account>.blob.core.chinacloudapi.cn；
	// Azurite 等路径风格的端点可以带路径，例如 http://127.0.0.1:10000/devstoreaccount1。
	Endpoint string `json:"endpoint"`
	// SAS 可选，SAS 令牌（sv=...&sig=...，开头的 ? 可有可无）。填了就用它认证，不用访问密钥。
	SAS string `json:"sas"`
}

var (
	azureAccountRe   = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	azureContainerRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9]|-[a-z0-9]){2,62}$`)
)

type azureTarget struct {
	account   string
	secret    []byte
	sas       string // 不带开头的 ?
	container string
	prefix    string
	scheme    string
	host      string
	basePath  string // 端点里的路径（已解码，不带结尾 /）
	baseEsc   string // 同上（编码后）
	client    *http.Client
	now       func() time.Time
}

func openAzure(c AzureConfig, d Deps) (Target, error) {
	account := strings.TrimSpace(c.Account)
	if !azureAccountRe.MatchString(account) {
		return nil, fmt.Errorf("Azure 存储账户名「%s」不正确：应为 3 到 24 位小写字母或数字", c.Account)
	}
	container := strings.TrimSpace(c.Container)
	if len(container) > 63 || !azureContainerRe.MatchString(container) {
		return nil, fmt.Errorf("Azure 容器名「%s」不正确：应为 3 到 63 位小写字母、数字或单个连字符", c.Container)
	}
	sas := strings.TrimPrefix(strings.TrimSpace(c.SAS), "?")
	var key []byte
	if sas != "" {
		q, err := url.ParseQuery(sas)
		if err != nil || q.Get("sig") == "" {
			return nil, errors.New("Azure SAS 令牌格式不正确，应形如 sv=...&sig=...")
		}
	} else {
		k := strings.TrimSpace(c.Key)
		if k == "" {
			return nil, errors.New("请填写 Azure 访问密钥或 SAS 令牌")
		}
		var err error
		key, err = base64.StdEncoding.DecodeString(k)
		if err != nil || len(key) == 0 {
			return nil, errors.New("Azure 访问密钥格式不正确：应为 Azure 门户「访问密钥」里的 base64 字符串")
		}
	}
	prefix, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return nil, err
	}
	t := &azureTarget{account: account, secret: key, sas: sas, container: container, prefix: prefix,
		scheme: "https", host: account + ".blob.core.windows.net", client: httpClient(d), now: time.Now}
	if ep := strings.TrimSpace(c.Endpoint); ep != "" {
		if !strings.Contains(ep, "://") {
			ep = "https://" + ep
		}
		u, err := url.Parse(ep)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("Azure 服务地址「%s」不正确", c.Endpoint)
		}
		t.scheme, t.host = u.Scheme, u.Host
		t.basePath = strings.TrimSuffix(u.Path, "/")
		t.baseEsc = strings.TrimSuffix(u.EscapedPath(), "/")
	}
	return t, nil
}

func (t *azureTarget) key(p string) string { return joinKey(t.prefix, p) }

// resource 返回请求路径（解码与编码两种形式）。blob 为空时是容器本身。
func (t *azureTarget) resource(blob string) (path, esc string) {
	path = t.basePath + "/" + t.container
	esc = t.baseEsc + "/" + t.container
	if blob != "" {
		path += "/" + blob
		esc += "/" + uriEncode(blob, true)
	}
	return path, esc
}

// azureQuery 把查询参数按 RFC 3986 编码（空格编成 %20，不用 +），按名称排序。
func azureQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// azureCanonicalHeaders 拼规范化的 x-ms-* 头：名称小写、按名称排序、值里的连续空白换成一个空格、去掉首尾空白。
func azureCanonicalHeaders(h http.Header) string {
	m := map[string]string{}
	for k, vs := range h {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-ms-") {
			continue
		}
		vals := make([]string, len(vs))
		for i, v := range vs {
			vals[i] = strings.Join(strings.Fields(v), " ")
		}
		m[lk] = strings.Join(vals, ",")
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(m[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// azureCanonicalResource 拼规范化资源：/<账户><编码后的请求路径>，再接每个查询参数
// （名称小写、按名称排序、值解码后按字典序用逗号连接），每个参数前一个换行。
func azureCanonicalResource(account, escPath string, q url.Values) string {
	var b strings.Builder
	b.WriteString("/" + account + escPath)
	lower := map[string][]string{}
	for k, vs := range q {
		lk := strings.ToLower(k)
		lower[lk] = append(lower[lk], vs...)
	}
	names := make([]string, 0, len(lower))
	for k := range lower {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		vs := append([]string(nil), lower[k]...)
		sort.Strings(vs)
		b.WriteString("\n" + k + ":" + strings.Join(vs, ","))
	}
	return b.String()
}

// azureStringToSign 按 Shared Key（2009-09-19 及以后的 Blob 服务）规则拼待签名串。
// Content-Length 为 0 时是空串；不用 Date 头（用 x-ms-date）。
func azureStringToSign(req *http.Request, account, escPath string, q url.Values) string {
	cl := ""
	if req.ContentLength > 0 {
		cl = strconv.FormatInt(req.ContentLength, 10)
	}
	h := req.Header
	return strings.Join([]string{
		req.Method,
		h.Get("Content-Encoding"),
		h.Get("Content-Language"),
		cl,
		h.Get("Content-MD5"),
		h.Get("Content-Type"),
		h.Get("Date"),
		h.Get("If-Modified-Since"),
		h.Get("If-Match"),
		h.Get("If-None-Match"),
		h.Get("If-Unmodified-Since"),
		h.Get("Range"),
	}, "\n") + "\n" + azureCanonicalHeaders(h) + azureCanonicalResource(account, escPath, q)
}

// newReq 生成并签名一个请求。body 为 nil 表示没有请求体。
func (t *azureTarget) newReq(ctx context.Context, method, blob string, q url.Values, hdr http.Header, body []byte) (*http.Request, error) {
	path, esc := t.resource(blob)
	raw := azureQuery(q)
	if t.sas != "" {
		if raw != "" {
			raw += "&"
		}
		raw += t.sas
	}
	u := &url.URL{Scheme: t.scheme, Host: t.host, Path: path, RawPath: esc, RawQuery: raw}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, errors.New("Azure 请求地址不正确")
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		if len(body) == 0 {
			req.Body = http.NoBody
		}
	}
	for k, vs := range hdr {
		req.Header[http.CanonicalHeaderKey(k)] = vs
	}
	req.Header.Set("x-ms-date", t.now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-version", azureAPIVersion)
	if t.sas == "" {
		sts := azureStringToSign(req, t.account, esc, q)
		m := hmac.New(sha256.New, t.secret)
		m.Write([]byte(sts))
		req.Header.Set("Authorization", "SharedKey "+t.account+":"+base64.StdEncoding.EncodeToString(m.Sum(nil)))
	}
	return req, nil
}

type azureError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
	status  int
}

func (e *azureError) Error() string {
	msg := e.Message
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = msg[:i] // 去掉 RequestId 与时间
	}
	switch {
	case e.Code != "" && msg != "":
		return e.Code + "：" + strings.TrimSpace(msg)
	case e.Code != "":
		return e.Code
	}
	return http.StatusText(e.status)
}

// do 发送可重放的请求（请求体在内存里），网络错误、408/429/5xx 时重试。返回的响应一定是 2xx。
func (t *azureTarget) do(ctx context.Context, method, blob string, q url.Values, hdr http.Header, body []byte) (*http.Response, error) {
	resp, err := retryHTTP(ctx, t.client, func() (*http.Request, error) {
		return t.newReq(ctx, method, blob, q, hdr, body)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	ae := &azureError{status: resp.StatusCode}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = xml.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), ae)
	if ae.Code == "" {
		ae.Code = resp.Header.Get("x-ms-error-code") // HEAD 响应没有响应体
	}
	if ae.Code == "" && resp.StatusCode == http.StatusNotFound {
		ae.Code = "BlobNotFound"
	}
	return nil, ae
}

func (t *azureTarget) wrap(ctx context.Context, op, p string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var ae *azureError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "BlobNotFound", "ResourceNotFound":
			return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
		case "ContainerNotFound":
			return fmt.Errorf("Azure Blob 容器「%s」不存在，请先在 Azure 门户里创建", t.container)
		case "AuthenticationFailed":
			if t.sas != "" {
				return errors.New("Azure Blob：身份验证失败，请检查 SAS 令牌是否正确、是否已过期，以及本机时间是否准确")
			}
			return errors.New("Azure Blob：身份验证失败，请检查存储账户名与访问密钥，以及本机时间是否准确")
		case "AuthorizationFailure", "AuthorizationPermissionMismatch", "AuthorizationResourceTypeMismatch",
			"AuthorizationServiceMismatch", "AuthorizationProtocolMismatch", "AuthorizationSourceIPMismatch", "InsufficientAccountPermissions":
			return fmt.Errorf("%s「%s」失败：没有权限，请检查访问密钥或 SAS 令牌的读、写、删除、列出授权（%s）", op, p, ae.Code)
		case "AccountIsDisabled":
			return errors.New("Azure Blob：存储账户已被禁用")
		case "InvalidHeaderValue", "UnsupportedHeader", "InvalidQueryParameterValue":
			if strings.Contains(ae.Message, "x-ms-version") {
				return fmt.Errorf("Azure Blob 服务不支持 API 版本 %s（Azurite 请升级或加 --skipApiVersionCheck）", azureAPIVersion)
			}
		case "Md5Mismatch", "InvalidMd5":
			return fmt.Errorf("%s「%s」失败：数据在传输中损坏，请重试", op, p)
		}
		return fmt.Errorf("%s「%s」失败：Azure Blob 返回 %s", op, p, ae.Error())
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	return fmt.Errorf("连接 Azure Blob 失败：%s", netErrText(err))
}

func (t *azureTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	blockSize := azureBlockSize
	if size > 0 {
		for (size+blockSize-1)/blockSize > azureMaxBlocks {
			blockSize *= 2
		}
	}
	src := ctxReader{ctx, putReader(r, size)}
	buf := make([]byte, blockSize)
	n, last, err := readChunk(src, buf)
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
	}
	key := t.key(p)
	if last {
		// 不超过一块：Put Blob 一次写完（原子）。
		hdr := http.Header{}
		hdr.Set("x-ms-blob-type", "BlockBlob")
		hdr.Set("Content-Type", "application/octet-stream")
		hdr.Set("Content-MD5", md5Base64(buf[:n]))
		resp, err := t.do(ctx, http.MethodPut, key, nil, hdr, buf[:n])
		if err != nil {
			return t.wrap(ctx, "上传", p, err)
		}
		closeBody(resp)
		return nil
	}
	uploadID := randomHex(8)
	var ids []string
	for i := 0; n > 0; i++ {
		if i >= azureMaxBlocks {
			return fmt.Errorf("上传「%s」失败：文件太大，超过 Azure Blob 块数上限（%d 块 × %d MiB）", p, azureMaxBlocks, len(buf)>>20)
		}
		id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s-%08d", uploadID, i)))
		hdr := http.Header{}
		hdr.Set("Content-MD5", md5Base64(buf[:n]))
		resp, err := t.do(ctx, http.MethodPut, key, url.Values{"comp": {"block"}, "blockid": {id}}, hdr, buf[:n])
		if err != nil {
			return t.wrap(ctx, "上传", p, err)
		}
		closeBody(resp)
		ids = append(ids, id)
		if last {
			break
		}
		n, last, err = readChunk(src, buf)
		if err != nil {
			// 不提交：已传的块是未提交状态，服务端 7 天后自动清理。
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
		}
	}
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)
	for _, id := range ids {
		body.WriteString("<Latest>" + id + "</Latest>")
	}
	body.WriteString("</BlockList>")
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/xml")
	hdr.Set("Content-MD5", md5Base64(body.Bytes()))
	hdr.Set("x-ms-blob-content-type", "application/octet-stream")
	resp, err := t.do(ctx, http.MethodPut, key, url.Values{"comp": {"blocklist"}}, hdr, body.Bytes())
	if err != nil {
		return t.wrap(ctx, "上传", p, err)
	}
	closeBody(resp)
	return nil
}

func (t *azureTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, t.key(p), nil, nil, nil)
	if err != nil {
		return nil, t.wrap(ctx, "读取", p, err)
	}
	if strings.EqualFold(resp.Header.Get("x-ms-meta-hdi_isfolder"), "true") {
		closeBody(resp)
		return nil, fmt.Errorf("读取「%s」失败：%w（这是一个目录）", p, ErrNotExist)
	}
	return newCheckedReadCloser(resp.Body, resp.ContentLength), nil
}

func (t *azureTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	resp, err := t.do(ctx, http.MethodHead, t.key(p), nil, nil, nil)
	if err != nil {
		return Entry{}, t.wrap(ctx, "查询", p, err)
	}
	resp.Body.Close()
	if strings.EqualFold(resp.Header.Get("x-ms-meta-hdi_isfolder"), "true") {
		// 开了分层命名空间（Data Lake）的账户里，目录也是一个 blob。
		return Entry{}, fmt.Errorf("查询「%s」失败：%w（这是一个目录）", p, ErrNotExist)
	}
	size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("查询「%s」失败：Azure Blob 没有返回文件大小", p)
	}
	mt, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	return Entry{Path: p, Size: size, ModTime: mt}, nil
}

func (t *azureTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	// 分层命名空间账户里目录也是 blob，先确认它不是目录。
	if _, err := t.Stat(ctx, p); err != nil {
		if errors.Is(err, ErrNotExist) {
			if strings.Contains(err.Error(), "目录") {
				return fmt.Errorf("删除「%s」失败：这是一个目录", p)
			}
			return nil
		}
		return err
	}
	resp, err := t.do(ctx, http.MethodDelete, t.key(p), nil, nil, nil)
	if err != nil {
		e := t.wrap(ctx, "删除", p, err)
		if errors.Is(e, ErrNotExist) {
			return nil
		}
		return e
	}
	closeBody(resp)
	return nil
}

type azureList struct {
	Blobs struct {
		Blob []struct {
			Name       string `xml:"Name"`
			Properties struct {
				LastModified  string `xml:"Last-Modified"`
				ContentLength int64  `xml:"Content-Length"`
				ResourceType  string `xml:"ResourceType"`
			} `xml:"Properties"`
			Metadata struct {
				IsFolder string `xml:"hdi_isfolder"`
			} `xml:"Metadata"`
		} `xml:"Blob"`
	} `xml:"Blobs"`
	NextMarker string `xml:"NextMarker"`
}

func (t *azureTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	full := joinKey(t.prefix, prefix)
	var out []Entry
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "include": {"metadata"},
			"maxresults": {strconv.Itoa(azureListMax)}}
		if full != "" {
			q.Set("prefix", full)
		}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, err := t.do(ctx, http.MethodGet, "", q, nil, nil)
		if err != nil {
			return nil, t.wrap(ctx, "列出", prefix, err)
		}
		var res azureList
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, t.wrap(ctx, "列出", prefix, rerr)
		}
		if err := xml.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &res); err != nil {
			return nil, fmt.Errorf("列出「%s」失败：Azure Blob 返回的列表无法解析", prefix)
		}
		for _, b := range res.Blobs.Blob {
			if strings.EqualFold(b.Metadata.IsFolder, "true") || strings.EqualFold(b.Properties.ResourceType, "directory") {
				continue
			}
			rel, ok := relKey(t.prefix, b.Name)
			if !ok || strings.HasSuffix(rel, "/") || isTempName(baseName(rel)) {
				continue
			}
			if _, err := cleanPath(rel); err != nil {
				continue
			}
			mt, _ := http.ParseTime(b.Properties.LastModified)
			out = append(out, Entry{Path: rel, Size: b.Properties.ContentLength, ModTime: mt})
		}
		if res.NextMarker == "" {
			return out, nil
		}
		if res.NextMarker == marker {
			return nil, fmt.Errorf("列出「%s」失败：Azure Blob 分页标记异常", prefix)
		}
		marker = res.NextMarker
	}
}

func (t *azureTarget) Close() error { return nil }
