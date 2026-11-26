package target

// 通用 HTTP 重试：用于 OSS 分片、Azure 块、WebDAV 的非流式请求（MKCOL/MOVE/DELETE/PROPFIND、暂存后的 PUT）、
// 百度网盘各接口。请求体必须可以重放（每次重试都重新生成请求）。
// 流式单请求（WebDAV 分块 PUT、SFTP/FTP/SMB 写入）无法中途重试，由备份引擎整次重试。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 重试参数（包级变量，测试里改短）：起始 1 秒、每次翻倍、单次间隔上限 30 秒、带抖动、最多重试 5 次；
// 服务器给了 Retry-After 时至少等那么久（最多 retryAfterCap）。
var (
	retryBaseDelay  = time.Second
	retryMaxDelay   = 30 * time.Second
	retryMaxRetries = 5
	retryAfterCap   = 5 * time.Minute
)

// retryableStatus 判断状态码是否值得重试：408、429 与 5xx（明确表示「不支持」「空间不足」之类的除外）。
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	case http.StatusNotImplemented, http.StatusHTTPVersionNotSupported, http.StatusInsufficientStorage,
		http.StatusLoopDetected, http.StatusNotExtended, http.StatusNetworkAuthenticationRequired:
		return false
	}
	return code >= 500 && code <= 599
}

// retryableNetErr 判断网络错误是否值得重试：连接被重置、超时、临时的 DNS 失败等可以重试；
// 证书错误、域名不存在、出站地址检查拒绝（拨号阶段的非系统调用错误）重试也没用。
func retryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSizeMismatch) {
		return false
	}
	var cve *tls.CertificateVerificationError
	var uae x509.UnknownAuthorityError
	var hne x509.HostnameError
	var cie x509.CertificateInvalidError
	var rhe tls.RecordHeaderError
	if errors.As(err, &cve) || errors.As(err, &uae) || errors.As(err, &hne) || errors.As(err, &cie) || errors.As(err, &rhe) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return opErr.Timeout() || isSyscallErr(opErr.Err)
	}
	return true
}

// parseRetryAfter 解析 Retry-After（秒数或 HTTP 日期）。
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// retryDelay 计算第 attempt 次重试（从 0 开始）前的等待时间。
func retryDelay(attempt int, resp *http.Response) time.Duration {
	d := retryBaseDelay
	for i := 0; i < attempt && d < retryMaxDelay; i++ {
		d *= 2
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	// 抖动：在 [d/2, d] 之间随机，避免多个上传同时重试。
	if half := d / 2; half > 0 {
		d = half + time.Duration(rand.Int64N(int64(half)+1))
	}
	if resp != nil {
		if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
			if ra > retryAfterCap {
				ra = retryAfterCap
			}
			if ra > d {
				d = ra
			}
		}
	}
	return d
}

// doRetry 发送请求，遇到可重试的网络错误或状态码时按指数退避重试。newReq 每次都要生成一个新请求
// （请求体可以重新读）。返回最后一次的响应（状态码由调用方判断，重试用尽时可能是 5xx）或错误。
func doRetry(ctx context.Context, send func(*http.Request) (*http.Response, error), newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := send(req)
		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		if ctx.Err() != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, ctx.Err()
		}
		if attempt >= retryMaxRetries || (err != nil && !retryableNetErr(err)) {
			return resp, err
		}
		d := retryDelay(attempt, resp)
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		if err := sleepCtx(ctx, d); err != nil {
			return nil, err
		}
	}
}

// retryHTTP 用 client 发送可重放的请求，见 doRetry。
func retryHTTP(ctx context.Context, client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	return doRetry(ctx, client.Do, newReq)
}

// retryTransport 给第三方库（gowebdav）发的请求加上重试：只重试可以重放的请求（没有请求体或有 GetBody）。
type retryTransport struct{ rt http.RoundTripper }

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return t.rt.RoundTrip(req)
	}
	first := true
	return doRetry(req.Context(), t.rt.RoundTrip, func() (*http.Request, error) {
		if first {
			first = false
			return req, nil
		}
		r := req.Clone(req.Context())
		if req.GetBody != nil {
			b, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = b
		}
		return r, nil
	})
}
