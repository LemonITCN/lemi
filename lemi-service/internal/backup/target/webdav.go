package target

// WebDAV：PROPFIND（查询、列目录）用 github.com/studio-b12/gowebdav 解析，其余请求（PUT、GET、MKCOL、MOVE、DELETE）
// 自己发，原因：
//   - gowebdav 的请求不带 context，不能取消；这里每次操作新建一个轻量客户端，把 ctx 绑进 Transport；
//   - gowebdav 的自动认证会把不可回放的上传流整个复制进内存（为 401 后重发），大文件会吃光内存，
//     所以统一用预先发送的 Basic 认证（坚果云、群晖、Nextcloud、Alist 都支持），上传流原样直通；
//   - gowebdav 的 MOVE 遇到 409 会无限重建父目录重试；PUT 前每次都 MKCOL，坚果云等服务有请求频率限制，
//     这里缓存已建好的目录。
// 上传先 PUT 到同目录的临时名（~$xxxx.tmp），成功后 MOVE（Overwrite: T）成正式名，服务器不支持覆盖式 MOVE
// 时先删目标再 MOVE。大小未知时默认用分块传输（Transfer-Encoding: chunked）直接 PUT；配置 spool 为真时先把数据
// 暂存到本机（Deps.SpoolDir），再带 Content-Length 上传（坚果云等不接受 chunked 的服务器用）。
// 失败（含数据源出错）时删除临时文件。非流式请求（MKCOL/MOVE/DELETE/PROPFIND/GET、暂存后的 PUT）按通用策略重试；
// 流式 PUT 无法中途重试，由备份引擎整次重试。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/studio-b12/gowebdav"
)

// WebDAVConfig 是 WebDAV 的配置（坚果云、群晖、Nextcloud、Alist 等）。
type WebDAVConfig struct {
	URL      string `json:"url"` // 例如 https://dav.jianguoyun.com/dav/
	Username string `json:"username"`
	Password string `json:"password"`
	Prefix   string `json:"prefix"` // 备份放在 URL 下的这个子目录里
	// Spool 为 true 时，大小未知的上传先暂存到本机再带 Content-Length 上传（服务器不接受 chunked 时用）。
	Spool bool `json:"spool"`
}

type webdavTarget struct {
	scheme   string
	host     string
	basePath string // URL 里的路径（已解码，不带结尾 /），例如 /dav
	prefix   string // 配置的子目录，"" 或 a/b
	user, pw string
	spool    bool
	spoolDir string
	rt       http.RoundTripper
	client   *http.Client // 自己发的请求用（不重试的流式 PUT 也用它）
	dirs     sync.Map     // 已确认存在的目录（相对 URL 根，含 prefix）
}

func openWebDAV(c WebDAVConfig, d Deps) (Target, error) {
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return nil, errors.New("请填写 WebDAV 地址")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("WebDAV 地址「%s」不正确，应以 https:// 或 http:// 开头", raw)
	}
	if u.User != nil {
		return nil, errors.New("WebDAV 地址里不要带用户名和口令，请填在单独的字段里")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("WebDAV 地址不能带 ? 或 # 参数")
	}
	prefix, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return nil, err
	}
	rt := httpTransport(d)
	return &webdavTarget{
		scheme:   u.Scheme,
		host:     u.Host,
		basePath: strings.TrimSuffix(u.Path, "/"),
		prefix:   prefix,
		user:     c.Username,
		pw:       c.Password,
		spool:    c.Spool,
		spoolDir: d.SpoolDir,
		rt:       rt,
		client:   httpClient(d),
	}, nil
}

// davAuth 是预先发送的 Basic 认证，不缓冲请求体。
type davAuth struct{ user, pw string }

func (a *davAuth) Authorize(c *http.Client, rq *http.Request, path string) error {
	if a.user != "" || a.pw != "" {
		rq.SetBasicAuth(a.user, a.pw)
	}
	return nil
}

func (a *davAuth) Verify(c *http.Client, rs *http.Response, path string) (bool, error) {
	if rs.StatusCode == http.StatusUnauthorized {
		return false, gowebdav.NewPathError("Authorize", path, rs.StatusCode)
	}
	return false, nil
}

func (a *davAuth) Clone() gowebdav.Authenticator { return a }
func (a *davAuth) Close() error                  { return nil }

// ctxTransport 把 ctx 绑到每个请求上，让 gowebdav 的请求也能被取消。
type ctxTransport struct {
	ctx context.Context
	rt  http.RoundTripper
}

func (c ctxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return c.rt.RoundTrip(r.WithContext(c.ctx))
}

// dav 返回只用来发 PROPFIND 的 gowebdav 客户端（带重试）。
func (t *webdavTarget) dav(ctx context.Context) *gowebdav.Client {
	c := gowebdav.NewAuthClient(t.scheme+"://"+t.host+t.basePath, gowebdav.NewPreemptiveAuth(&davAuth{t.user, t.pw}))
	c.SetTransport(ctxTransport{ctx, retryTransport{t.rt}}) // 先绑 ctx，重试的等待也能被取消
	return c
}

// full 返回相对 URL 根的路径（含 prefix）。
func (t *webdavTarget) full(rel string) string { return joinKey(t.prefix, rel) }

func (t *webdavTarget) rawURL(full string) string {
	u := url.URL{Scheme: t.scheme, Host: t.host, Path: t.basePath + "/" + full}
	return u.String()
}

// davStatusError 是自己发的请求得到的非预期状态码。
type davStatusError struct{ status int }

func (e *davStatusError) Error() string { return fmt.Sprintf("WebDAV 返回 HTTP %d", e.status) }

func davStatus(err error) int {
	var de *davStatusError
	if errors.As(err, &de) {
		return de.status
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		var se gowebdav.StatusError
		if errors.As(pe.Err, &se) {
			return se.Status
		}
	}
	return 0
}

func davStatusErr(op, p string, status int) error {
	switch status {
	case http.StatusUnauthorized:
		return errors.New("连接 WebDAV 失败：用户名或口令不正确")
	case http.StatusForbidden:
		return fmt.Errorf("%s「%s」失败：没有权限（WebDAV 返回 403）", op, p)
	case http.StatusNotFound:
		return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return fmt.Errorf("%s「%s」失败：WebDAV 服务器不支持该操作（HTTP %d）", op, p, status)
	case http.StatusLengthRequired:
		return fmt.Errorf("%s「%s」失败：WebDAV 服务器不接受分块传输（HTTP 411），请在目标设置里勾选「先暂存到本机再上传」", op, p)
	case http.StatusLocked:
		return fmt.Errorf("%s「%s」失败：文件被锁定", op, p)
	case http.StatusInsufficientStorage:
		return fmt.Errorf("%s「%s」失败：WebDAV 空间不足", op, p)
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%s「%s」失败：文件超过 WebDAV 服务器允许的大小", op, p)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return fmt.Errorf("%s「%s」失败：WebDAV 服务器繁忙或请求过于频繁（坚果云等服务限制请求频率），请稍后重试", op, p)
	}
	return fmt.Errorf("%s「%s」失败：WebDAV 返回 HTTP %d", op, p, status)
}

func (t *webdavTarget) wrap(ctx context.Context, op, p string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if s := davStatus(err); s != 0 {
		return davStatusErr(op, p, s)
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("连接 WebDAV 失败：%s", netErrText(err))
}

func (t *webdavTarget) newReq(ctx context.Context, method, full string, body io.Reader, size int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.rawURL(full), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size // -1 时用分块传输
		if size == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if t.user != "" || t.pw != "" {
		req.SetBasicAuth(t.user, t.pw)
	}
	return req, nil
}

// doRetry 发送没有请求体的请求（MKCOL、MOVE、DELETE、GET），可重试。hdr 可为 nil。
func (t *webdavTarget) doRetry(ctx context.Context, method, full string, hdr http.Header) (*http.Response, error) {
	return retryHTTP(ctx, t.client, func() (*http.Request, error) {
		req, err := t.newReq(ctx, method, full, nil, -1)
		if err != nil {
			return nil, err
		}
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		return req, nil
	})
}

func closeBody(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// mkcol 建一级目录。已存在（405）视为成功。
func (t *webdavTarget) mkcol(ctx context.Context, dir string) error {
	resp, err := t.doRetry(ctx, "MKCOL", dir, nil)
	if err != nil {
		return err
	}
	closeBody(resp)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusNoContent, http.StatusMethodNotAllowed:
		return nil
	}
	return &davStatusError{resp.StatusCode}
}

// mkdirAll 确保目录存在（full 是相对 URL 根的目录，"" 表示根）。
func (t *webdavTarget) mkdirAll(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	if _, ok := t.dirs.Load(dir); ok {
		return nil
	}
	// 快速路径：直接建最深一级，已存在时服务器返回 405。
	if err := t.mkcol(ctx, dir); err == nil {
		t.dirs.Store(dir, true)
		return nil
	} else if ctx.Err() != nil {
		return ctx.Err()
	} else if s := davStatus(err); s == 0 || s == http.StatusUnauthorized {
		return err
	}
	segs := strings.Split(dir, "/")
	for i := range segs {
		sub := strings.Join(segs[:i+1], "/")
		if _, ok := t.dirs.Load(sub); ok {
			continue
		}
		if err := t.mkcol(ctx, sub); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 有的服务器对已存在的目录返回 409/403 等，再查一次是不是目录。
			fi, serr := t.dav(ctx).Stat(sub)
			if serr != nil || !fi.IsDir() {
				return err
			}
		}
		t.dirs.Store(sub, true)
	}
	return nil
}

// move 把 from 改名为 to（都是相对 URL 根的路径）。
func (t *webdavTarget) move(ctx context.Context, from, to string, overwrite bool) error {
	hdr := http.Header{}
	hdr.Set("Destination", t.rawURL(to))
	if overwrite {
		hdr.Set("Overwrite", "T")
	} else {
		hdr.Set("Overwrite", "F")
	}
	resp, err := t.doRetry(ctx, "MOVE", from, hdr)
	if err != nil {
		return err
	}
	closeBody(resp)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	}
	return &davStatusError{resp.StatusCode}
}

func (t *webdavTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	if size == 0 {
		if err := emptyCheck(r); err != nil {
			return fmt.Errorf("上传「%s」失败：%w", p, err)
		}
	}
	final := t.full(p)
	dir := dirName(final)
	if err := t.mkdirAll(ctx, dir); err != nil {
		return t.wrap(ctx, "创建目录", dirName(p), err)
	}
	tmp := joinKey(dir, tempName())
	var resp *http.Response
	if size < 0 && t.spool {
		// 先暂存到本机，再带 Content-Length 上传（可以重试）。
		f, n, cleanup, err := spoolFile(ctx, t.spoolDir, "lemi-webdav-*.tmp", r, 0)
		if err != nil {
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
		}
		defer cleanup()
		resp, err = retryHTTP(ctx, t.client, func() (*http.Request, error) {
			var body io.Reader = io.NewSectionReader(f, 0, n)
			if n == 0 {
				body = http.NoBody
			}
			return t.newReq(ctx, http.MethodPut, tmp, body, n)
		})
		if err != nil {
			t.removeQuiet(ctx, tmp)
			return t.wrap(ctx, "上传", p, err)
		}
	} else {
		// 流式 PUT：size < 0 时分块传输，size >= 0 时带 Content-Length（坚果云等不接受 chunked）。
		src := &srcErrReader{r: putReader(r, size)}
		req, err := t.newReq(ctx, http.MethodPut, tmp, src, size)
		if err != nil {
			return err
		}
		resp, err = t.client.Do(req)
		if err != nil {
			t.removeQuiet(ctx, tmp)
			if src.err != nil && ctx.Err() == nil {
				return fmt.Errorf("上传「%s」失败：%w", p, src.err)
			}
			return t.wrap(ctx, "上传", p, err)
		}
		if src.err != nil {
			// 服务器在数据源出错前就回了响应（不应发生）：按失败处理。
			closeBody(resp)
			t.removeQuiet(ctx, tmp)
			return fmt.Errorf("上传「%s」失败：%w", p, src.err)
		}
	}
	closeBody(resp)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.removeQuiet(ctx, tmp)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict {
			t.dirs.Delete(dir) // 目录可能被别人删了，下次重建
			return fmt.Errorf("上传「%s」失败：目标目录不存在（WebDAV 返回 HTTP %d）", p, resp.StatusCode)
		}
		return davStatusErr("上传", p, resp.StatusCode)
	}
	if err := t.move(ctx, tmp, final, true); err != nil {
		if s := davStatus(err); ctx.Err() == nil && (s == http.StatusPreconditionFailed || s == http.StatusConflict || s == http.StatusForbidden) {
			// 不支持覆盖式 MOVE 的服务器（目标已存在时返回 412/409/403）：先删目标再改名。
			if derr := t.deleteRaw(ctx, final); derr == nil {
				err = t.move(ctx, tmp, final, false)
			}
		}
		if err != nil {
			t.removeQuiet(ctx, tmp)
			return t.wrap(ctx, "保存", p, err)
		}
	}
	return nil
}

func (t *webdavTarget) removeQuiet(ctx context.Context, full string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = t.deleteRaw(c, full)
}

// deleteRaw 删除，不存在视为成功。
func (t *webdavTarget) deleteRaw(ctx context.Context, full string) error {
	resp, err := t.doRetry(ctx, http.MethodDelete, full, nil)
	if err != nil {
		return err
	}
	closeBody(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
		return nil
	}
	return &davStatusError{resp.StatusCode}
}

func (t *webdavTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	resp, err := t.doRetry(ctx, http.MethodGet, t.full(p), nil)
	if err != nil {
		return nil, t.wrap(ctx, "读取", p, err)
	}
	if resp.StatusCode != http.StatusOK {
		closeBody(resp)
		return nil, davStatusErr("读取", p, resp.StatusCode)
	}
	return newCheckedReadCloser(resp.Body, resp.ContentLength), nil
}

func (t *webdavTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	fi, err := t.dav(ctx).Stat(t.full(p))
	if err != nil {
		return Entry{}, t.wrap(ctx, "查询", p, err)
	}
	if fi == nil || fi.IsDir() {
		return Entry{}, fmt.Errorf("查询「%s」失败：%w（这是一个目录）", p, ErrNotExist)
	}
	return Entry{Path: p, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

func (t *webdavTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	// WebDAV 的 DELETE 对目录是递归删除，先确认它是文件，防止误删整个目录。
	fi, err := t.dav(ctx).Stat(t.full(p))
	if err != nil {
		if davStatus(err) == http.StatusNotFound {
			return nil
		}
		return t.wrap(ctx, "删除", p, err)
	}
	if fi != nil && fi.IsDir() {
		return fmt.Errorf("删除「%s」失败：这是一个目录", p)
	}
	if err := t.deleteRaw(ctx, t.full(p)); err != nil {
		return t.wrap(ctx, "删除", p, err)
	}
	return nil
}

func (t *webdavTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	c := t.dav(ctx)
	var out []Entry
	// 逐层 Depth: 1 列目录（坚果云等不支持 Depth: infinity）。
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		files, err := c.ReadDir(t.full(rel))
		if err != nil {
			if davStatus(err) == http.StatusNotFound {
				return nil // 目录不存在（或遍历期间被删）
			}
			return err
		}
		for _, fi := range files {
			name := fi.Name()
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				continue
			}
			child := joinKey(rel, name)
			if fi.IsDir() {
				if wantDir(child, prefix) {
					if err := walk(child); err != nil {
						return err
					}
				}
				continue
			}
			if isTempName(name) {
				if isStaleTemp(name, fi.ModTime()) {
					t.deleteRaw(ctx, t.full(child)) // 上次中断留下的临时文件
				}
				continue
			}
			if !strings.HasPrefix(child, prefix) {
				continue
			}
			if _, err := cleanPath(child); err != nil {
				continue
			}
			out = append(out, Entry{Path: child, Size: fi.Size(), ModTime: fi.ModTime()})
		}
		return nil
	}
	if err := walk(prefixDir(prefix)); err != nil {
		return nil, t.wrap(ctx, "列出", prefix, err)
	}
	return out, nil
}

func (t *webdavTarget) Close() error { return nil }
