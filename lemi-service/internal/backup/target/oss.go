package target

// 阿里云 OSS 用原生 REST 接口 + OSS V4 签名（OSS4-HMAC-SHA256）实现，不走 S3 兼容接口。
// 原因（2026-11 查证阿里云文档「使用 AWS SDK 访问 OSS」与「V4 签名」）：
//   - S3 兼容接口的文档自相矛盾：端点一处写 s3.oss-<region>.aliyuncs.com、一处写 oss-<region>.aliyuncs.com，
//     SigV4 的签名地域没有明确说明（示例里 cn-hangzhou、AWS_GLOBAL 都有）；
//   - OSS 不支持 chunked / aws-chunked 传输，boto3 的 V4 实现与 OSS 不兼容，兼容层的行为随 SDK 版本漂移；
//   - 2025-03-20 起新用户用默认公网域名访问中国内地存储桶的数据接口会被拒（PublicEndpointForbidden），
//     需要绑定自定义域名，自定义域名下 S3 兼容 SDK 的虚拟主机寻址不好处理。
// 原生 V4 签名文档清楚（规范请求用文档示例逐字节核对过），请求只用 PUT/GET/HEAD/DELETE/ListObjectsV2
// 与分片上传，全部由本文件控制。
//
// 写入：大小可以未知。每读满一片（ossPartSize，在内存里缓冲）传一片；第一片就读到 EOF 时退化为单次 PUT。
// 每个请求都带 Content-MD5，失败按通用重试助手重试；数据源出错或上传失败时 AbortMultipartUpload，
// 合并之前对象不可见，不会留下半个对象。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// OSSConfig 是阿里云 OSS 的配置。
type OSSConfig struct {
	Region          string `json:"region"` // 地域 ID，例如 cn-hangzhou（写成 oss-cn-hangzhou 也认）
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
	// Internal 为 true 时走内网端点 oss-<region>-internal.aliyuncs.com（服务部署在同地域阿里云 ECS 上时用）。
	Internal bool `json:"internal"`
	// Endpoint 可选，覆盖默认端点：
	//   - 填 *.aliyuncs.com 的端点（如 oss-accelerate.aliyuncs.com 传输加速）时按虚拟主机风格访问；
	//   - 填其他域名时视为已绑定到该存储桶的自定义域名（CNAME），直接访问该域名。
	// 可以带 https:// 或 http://，缺省 https。2025-03-20 之后开通 OSS 的用户，在中国内地地域用默认公网
	// 域名读写数据会被拒绝，需要在这里填自定义域名。
	Endpoint string `json:"endpoint"`
}

// ossPartSize 是分片大小（每片在内存里缓冲以便校验和重试）。测试里改小（OSS 要求非最后一片至少 100 KB）。
var ossPartSize int64 = 16 << 20

const (
	ossMaxParts      = 10000
	ossUnsignedBody  = "UNSIGNED-PAYLOAD"
	ossTimeFormat    = "20060102T150405Z"
	ossDateFormat    = "20060102"
	ossListPageLimit = 1000
)

var ossBucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

type ossTarget struct {
	region string
	bucket string
	prefix string
	ak, sk string
	scheme string
	host   string // 请求的 Host（虚拟主机风格时含 bucket）
	client *http.Client
	now    func() time.Time
}

func ossEndpointFrom(c OSSConfig) (scheme, host string, err error) {
	region := strings.TrimPrefix(strings.TrimSpace(c.Region), "oss-")
	bucket := strings.TrimSpace(c.Bucket)
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		suffix := ""
		if c.Internal {
			suffix = "-internal"
		}
		return "https", bucket + ".oss-" + region + suffix + ".aliyuncs.com", nil
	}
	scheme = "https"
	if strings.Contains(ep, "://") {
		u, perr := url.Parse(ep)
		if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
			return "", "", fmt.Errorf("阿里云 OSS 自定义端点「%s」不正确", ep)
		}
		scheme, ep = u.Scheme, u.Host
	}
	if strings.ContainsAny(ep, "/ ") {
		return "", "", fmt.Errorf("阿里云 OSS 自定义端点「%s」不正确", ep)
	}
	hostOnly := strings.ToLower(ep)
	if i := strings.LastIndex(hostOnly, ":"); i >= 0 && !strings.Contains(hostOnly[i:], "]") {
		hostOnly = hostOnly[:i]
	}
	if strings.HasSuffix(hostOnly, ".aliyuncs.com") {
		if strings.HasPrefix(hostOnly, bucket+".") {
			return scheme, ep, nil // 已经带了 bucket
		}
		return scheme, bucket + "." + ep, nil
	}
	return scheme, ep, nil // 自定义域名（CNAME）
}

func openOSS(c OSSConfig, d Deps) (Target, error) {
	region := strings.TrimPrefix(strings.TrimSpace(c.Region), "oss-")
	if !regionRe.MatchString(region) {
		return nil, fmt.Errorf("阿里云 OSS 地域「%s」不正确，应类似 cn-hangzhou", c.Region)
	}
	bucket := strings.TrimSpace(c.Bucket)
	if !ossBucketRe.MatchString(bucket) {
		return nil, fmt.Errorf("阿里云 OSS 存储桶名称「%s」不正确", c.Bucket)
	}
	ak := strings.TrimSpace(c.AccessKeyID)
	if ak == "" || c.AccessKeySecret == "" {
		return nil, errors.New("请填写 AccessKey ID 和 AccessKey Secret")
	}
	prefix, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return nil, err
	}
	scheme, host, err := ossEndpointFrom(c)
	if err != nil {
		return nil, err
	}
	return &ossTarget{region: region, bucket: bucket, prefix: prefix, ak: ak, sk: c.AccessKeySecret,
		scheme: scheme, host: host, client: httpClient(d), now: time.Now}, nil
}

// ---------------- V4 签名 ----------------

// ossURIEncode 按 RFC 3986 编码，只保留 A-Z a-z 0-9 - _ . ~；keepSlash 时 / 不编码。
func ossURIEncode(s string, keepSlash bool) string { return uriEncode(s, keepSlash) }

// ossCanonicalQuery 生成规范查询串：名称和值分别编码，按编码后的名称排序；只有名称没有值的不带 =。
func ossCanonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	var items []kv
	for k, vs := range q {
		ek := ossURIEncode(k, false)
		for _, v := range vs {
			items = append(items, kv{ek, ossURIEncode(v, false)})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].k != items[j].k {
			return items[i].k < items[j].k
		}
		return items[i].v < items[j].v
	})
	parts := make([]string, len(items))
	for i, it := range items {
		if it.v == "" {
			parts[i] = it.k
		} else {
			parts[i] = it.k + "=" + it.v
		}
	}
	return strings.Join(parts, "&")
}

// ossCanonicalRequest 按 OSS V4 文档拼规范请求。headers 的键必须是小写。
// 参与签名的头：x-oss-*、content-type、content-md5（存在才参与），以及 additional 里列出的头。
func ossCanonicalRequest(method, canonicalURI string, q url.Values, headers map[string]string, additional []string) string {
	signed := map[string]bool{}
	for k := range headers {
		if strings.HasPrefix(k, "x-oss-") || k == "content-type" || k == "content-md5" {
			signed[k] = true
		}
	}
	for _, k := range additional {
		signed[k] = true
	}
	names := make([]string, 0, len(signed))
	for k := range signed {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k)
		ch.WriteByte(':')
		ch.WriteString(strings.TrimSpace(headers[k]))
		ch.WriteByte('\n')
	}
	add := append([]string(nil), additional...)
	sort.Strings(add)
	return strings.Join([]string{
		method,
		canonicalURI,
		ossCanonicalQuery(q),
		ch.String(),
		strings.Join(add, ";"),
		headers["x-oss-content-sha256"],
	}, "\n")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func ossSigningKey(secret, date, region string) []byte {
	k := hmacSHA256([]byte("aliyun_v4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "oss")
	return hmacSHA256(k, "aliyun_v4_request")
}

// sign 给请求加上 x-oss-date、x-oss-content-sha256 与 Authorization 头。
func (t *ossTarget) sign(req *http.Request, objectKey string, q url.Values) {
	now := t.now().UTC()
	ts := now.Format(ossTimeFormat)
	date := now.Format(ossDateFormat)
	req.Header.Set("x-oss-date", ts)
	req.Header.Set("x-oss-content-sha256", ossUnsignedBody)
	headers := map[string]string{}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-oss-") || lk == "content-type" || lk == "content-md5" {
			headers[lk] = strings.Join(vs, ",")
		}
	}
	canonicalURI := "/" + t.bucket + "/" + ossURIEncode(objectKey, true)
	if objectKey == "" {
		canonicalURI = "/" + t.bucket + "/"
	}
	cr := ossCanonicalRequest(req.Method, canonicalURI, q, headers, nil)
	sum := sha256.Sum256([]byte(cr))
	scope := date + "/" + t.region + "/oss/aliyun_v4_request"
	sts := "OSS4-HMAC-SHA256\n" + ts + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	sig := hex.EncodeToString(hmacSHA256(ossSigningKey(t.sk, date, t.region), sts))
	req.Header.Set("Authorization", "OSS4-HMAC-SHA256 Credential="+t.ak+"/"+scope+", Signature="+sig)
}

// ---------------- 请求 ----------------

type ossError struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
	EC        string   `xml:"EC"`
	status    int
}

func (e *ossError) Error() string {
	if e.Message != "" {
		return e.Code + "：" + e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return http.StatusText(e.status)
}

func (t *ossTarget) objectURL(objectKey string, q url.Values) string {
	u := t.scheme + "://" + t.host + "/" + ossURIEncode(objectKey, true)
	if len(q) > 0 {
		u += "?" + ossCanonicalQuery(q)
	}
	return u
}

func (t *ossTarget) newReq(ctx context.Context, method, objectKey string, q url.Values, hdr http.Header, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.objectURL(objectKey, q), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		if len(body) == 0 {
			req.Body = http.NoBody
		}
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	t.sign(req, objectKey, q)
	return req, nil
}

// checkResp 把非 2xx 响应转成 *ossError（并关闭响应体）。
func checkOSSResp(resp *http.Response) (*http.Response, error) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	oe := &ossError{status: resp.StatusCode}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if len(bytes.TrimSpace(data)) == 0 {
		// HEAD 请求没有响应体，OSS 把错误 XML 用 base64 放在 x-oss-err 头里。
		if h := resp.Header.Get("x-oss-err"); h != "" {
			data, _ = base64.StdEncoding.DecodeString(h)
		}
	}
	_ = xml.Unmarshal(data, oe)
	if oe.Code == "" && resp.StatusCode == http.StatusNotFound {
		oe.Code = "NoSuchKey"
	}
	return nil, oe
}

// do 发送一个请求（不重试）。返回的响应状态码一定是 2xx。
func (t *ossTarget) do(ctx context.Context, method, objectKey string, q url.Values, hdr http.Header, body []byte) (*http.Response, error) {
	req, err := t.newReq(ctx, method, objectKey, q, hdr, body)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	return checkOSSResp(resp)
}

// doRetry 发送可重放的请求（请求体在内存里），网络错误、408/429/5xx 时按通用策略重试。
func (t *ossTarget) doRetry(ctx context.Context, method, objectKey string, q url.Values, hdr http.Header, body []byte) (*http.Response, error) {
	resp, err := retryHTTP(ctx, t.client, func() (*http.Request, error) {
		return t.newReq(ctx, method, objectKey, q, hdr, body)
	})
	if err != nil {
		return nil, err
	}
	return checkOSSResp(resp)
}

func (t *ossTarget) wrap(ctx context.Context, op, p string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var oe *ossError
	if errors.As(err, &oe) {
		switch oe.Code {
		case "NoSuchKey":
			return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
		case "NoSuchBucket":
			return fmt.Errorf("阿里云 OSS 存储桶「%s」不存在，请检查存储桶名称和地域", t.bucket)
		case "InvalidAccessKeyId":
			return errors.New("阿里云 OSS：AccessKey ID 不存在或已停用")
		case "SignatureDoesNotMatch":
			return errors.New("阿里云 OSS：AccessKey Secret 不正确")
		case "AccessDenied":
			if strings.Contains(oe.Message, "PublicEndpointForbidden") {
				return errors.New(ossPublicEndpointMsg)
			}
			return fmt.Errorf("%s「%s」失败：没有权限，请检查 AccessKey 对该存储桶的读写授权（%s）", op, p, oe.Message)
		case "PublicEndpointForbidden":
			return errors.New(ossPublicEndpointMsg)
		case "RequestTimeTooSkewed":
			return errors.New("阿里云 OSS：本机时间与服务器相差太大，请校准系统时间")
		case "InvalidDigest", "BadDigest":
			return fmt.Errorf("%s「%s」失败：数据在传输中损坏，请重试", op, p)
		}
		return fmt.Errorf("%s「%s」失败：%s", op, p, oe.Error())
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	return fmt.Errorf("连接阿里云 OSS 失败：%s", netErrText(err))
}

const ossPublicEndpointMsg = "阿里云 OSS 拒绝通过默认公网域名读写该存储桶（2025-03-20 之后开通的账号在中国内地地域有此限制）。" +
	"请在 OSS 控制台给存储桶绑定自定义域名，并把它填到「自定义端点」里；服务部署在同地域阿里云 ECS 上时也可以改用内网访问"

func (t *ossTarget) key(p string) string { return joinKey(t.prefix, p) }

func md5Base64(b []byte) string {
	s := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

// readChunk 读满 buf（或读到 EOF）。eof 表示数据源已经结束。数据源返回非 EOF 错误时原样返回。
func readChunk(r io.Reader, buf []byte) (n int, eof bool, err error) {
	n, err = io.ReadFull(r, buf)
	switch err {
	case nil:
		return n, false, nil
	case io.EOF, io.ErrUnexpectedEOF:
		return n, true, nil
	}
	return n, false, err
}

func (t *ossTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	partSize := ossPartSize
	if size > 0 {
		for (size+partSize-1)/partSize > ossMaxParts {
			partSize *= 2
		}
	}
	src := ctxReader{ctx, putReader(r, size)}
	buf := make([]byte, partSize)
	n, eof, err := readChunk(src, buf)
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
	}
	if eof {
		// 数据不超过一片：单次 PUT，带 Content-MD5 让服务端校验，失败可以重试。PUT 是原子的。
		hdr := http.Header{}
		hdr.Set("Content-Type", "application/octet-stream")
		hdr.Set("Content-MD5", md5Base64(buf[:n]))
		resp, err := t.doRetry(ctx, http.MethodPut, t.key(p), nil, hdr, buf[:n])
		if err != nil {
			return t.wrap(ctx, "上传", p, err)
		}
		resp.Body.Close()
		return nil
	}
	return t.putMultipart(ctx, p, src, buf, n)
}

type ossInitResult struct {
	UploadID string `xml:"UploadId"`
}

type ossCompletePart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type ossComplete struct {
	XMLName xml.Name          `xml:"CompleteMultipartUpload"`
	Parts   []ossCompletePart `xml:"Part"`
}

// putMultipart 分片上传：buf[:n] 是已经读满的第一片。每片缓冲在内存里，带 Content-MD5，失败重试；
// 合并完成前对象不可见，出错时放弃分片上传。
func (t *ossTarget) putMultipart(ctx context.Context, p string, src io.Reader, buf []byte, n int) (err error) {
	key := t.key(p)
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/octet-stream")
	resp, err := t.doRetry(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, hdr, []byte{})
	if err != nil {
		return t.wrap(ctx, "上传", p, err)
	}
	var init ossInitResult
	derr := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&init)
	resp.Body.Close()
	if derr != nil || init.UploadID == "" {
		return fmt.Errorf("上传「%s」失败：阿里云 OSS 返回的分片上传响应无法解析", p)
	}
	defer func() {
		if err != nil {
			t.abortMultipart(ctx, key, init.UploadID)
		}
	}()
	var parts []ossCompletePart
	last := false // buf[:n] 是最后一片
	for num := 1; n > 0; num++ {
		if num > ossMaxParts {
			return fmt.Errorf("上传「%s」失败：文件太大，超过阿里云 OSS 分片数上限（%d 片 × %d MiB）", p, ossMaxParts, len(buf)>>20)
		}
		ph := http.Header{}
		ph.Set("Content-MD5", md5Base64(buf[:n]))
		q := url.Values{"partNumber": {strconv.Itoa(num)}, "uploadId": {init.UploadID}}
		resp, err := t.doRetry(ctx, http.MethodPut, key, q, ph, buf[:n])
		if err != nil {
			return t.wrap(ctx, "上传", p, err)
		}
		etag := resp.Header.Get("ETag")
		resp.Body.Close()
		if etag == "" {
			return fmt.Errorf("上传「%s」失败：阿里云 OSS 没有返回分片 ETag", p)
		}
		parts = append(parts, ossCompletePart{PartNumber: num, ETag: etag})
		if last {
			break
		}
		n, last, err = readChunk(src, buf)
		if err != nil {
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
		}
	}
	body, _ := xml.Marshal(ossComplete{Parts: parts})
	ch := http.Header{}
	ch.Set("Content-Type", "application/xml")
	ch.Set("Content-MD5", md5Base64(body))
	resp, err = t.doRetry(ctx, http.MethodPost, key, url.Values{"uploadId": {init.UploadID}}, ch, body)
	if err != nil {
		return t.wrap(ctx, "上传", p, err)
	}
	resp.Body.Close()
	return nil
}

// abortMultipart 放弃分片上传（清掉已传的分片），用独立的短超时上下文，ctx 已取消时也执行。
func (t *ossTarget) abortMultipart(ctx context.Context, key, uploadID string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if resp, err := t.doRetry(c, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil, nil); err == nil {
		resp.Body.Close()
	}
}

func (t *ossTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	resp, err := t.doRetry(ctx, http.MethodGet, t.key(p), nil, nil, nil)
	if err != nil {
		return nil, t.wrap(ctx, "读取", p, err)
	}
	return newCheckedReadCloser(resp.Body, resp.ContentLength), nil
}

func (t *ossTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	resp, err := t.doRetry(ctx, http.MethodHead, t.key(p), nil, nil, nil)
	if err != nil {
		return Entry{}, t.wrap(ctx, "查询", p, err)
	}
	resp.Body.Close()
	mt, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("查询「%s」失败：阿里云 OSS 没有返回文件大小", p)
	}
	return Entry{Path: p, Size: size, ModTime: mt}, nil
}

func (t *ossTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	resp, err := t.doRetry(ctx, http.MethodDelete, t.key(p), nil, nil, nil)
	if err != nil {
		e := t.wrap(ctx, "删除", p, err)
		if errors.Is(e, ErrNotExist) {
			return nil
		}
		return e
	}
	resp.Body.Close()
	return nil
}

type ossListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
}

func (t *ossTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	full := joinKey(t.prefix, prefix)
	var out []Entry
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(ossListPageLimit)}}
		if full != "" {
			q.Set("prefix", full)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := t.doRetry(ctx, http.MethodGet, "", q, nil, nil)
		if err != nil {
			return nil, t.wrap(ctx, "列出", prefix, err)
		}
		var res ossListResult
		derr := xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&res)
		resp.Body.Close()
		if derr != nil {
			return nil, fmt.Errorf("列出「%s」失败：阿里云 OSS 返回的列表无法解析", prefix)
		}
		for _, c := range res.Contents {
			rel, ok := relKey(t.prefix, c.Key)
			if !ok || strings.HasSuffix(rel, "/") || isTempName(baseName(rel)) {
				continue
			}
			if _, err := cleanPath(rel); err != nil {
				continue
			}
			mt, _ := time.Parse(time.RFC3339Nano, c.LastModified)
			out = append(out, Entry{Path: rel, Size: c.Size, ModTime: mt})
		}
		if !res.IsTruncated {
			return out, nil
		}
		if res.NextContinuationToken == "" || res.NextContinuationToken == token {
			return nil, fmt.Errorf("列出「%s」失败：阿里云 OSS 分页标记异常", prefix)
		}
		token = res.NextContinuationToken
	}
}

func (t *ossTarget) Close() error { return nil }
