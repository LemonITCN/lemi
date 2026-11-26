package target

// FTP / FTPS 基于 github.com/jlaffaye/ftp，被动模式（EPSV，不支持时退回 PASV，PASV 回复里的地址不信任，
// 一律连控制连接的地址）。控制连接与数据连接都经 Deps.Dial 拨号：
//   - tls=implicit：所有连接拨通后立即包 TLS（库在设置了拨号函数时不会自己包）；
//   - tls=explicit：控制连接明文拨号，由库发 AUTH TLS 后升级；数据连接（PROT P）由这里包 TLS；
//   - 数据连接与控制连接共用 TLS 会话缓存，支持要求「数据连接复用 TLS 会话」且用会话票据的服务器。
// FTP 控制连接不能并发使用，这里维护一个小连接池，每个操作独占一条连接；ctx 取消时直接关掉连接中断阻塞的读写。
// 写入：STOR 到同目录临时名（~$xxxx.tmp）→ 核对大小（服务器支持 SIZE 时）→ RNFR/RNTO 改名（目标已存在且服务器
// 不覆盖时先删再改名）。数据源出错时库仍会正常结束数据连接，随后删除临时文件。流式写入无法中途重试，由备份引擎
// 整次重试；幂等操作在连接断开时换一条新连接重试一次。列目录优先 MLSD，服务器没有声明 MLST 时用 LIST。

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// FTPConfig 是 FTP / FTPS 的配置。
type FTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"` // 缺省 21，隐式 TLS 缺省 990
	Username string `json:"username"`
	Password string `json:"password"`
	// TLS：none（明文）、explicit（AUTH TLS，常见的 FTPES）、implicit（隐式 TLS，常见端口 990）。缺省 none。
	TLS                string `json:"tls"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"` // 不校验服务器证书（自签名证书时用）
	Path               string `json:"path"`                 // 备份根目录；相对路径相对登录后的默认目录
}

const (
	ftpTLSNone     = "none"
	ftpTLSExplicit = "explicit"
	ftpTLSImplicit = "implicit"
	ftpPoolSize    = 4
	ftpIdleCheck   = 30 * time.Second // 空闲超过这个时长的连接，复用前先 NOOP 确认还活着
)

type ftpTarget struct {
	addr     string
	host     string
	user, pw string
	tlsMode  string
	tlsCfg   *tls.Config
	dir      string // 配置的根目录（去掉多余 /）
	abs      bool
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)

	mu     sync.Mutex
	base   string // 解析后的绝对根目录（第一次连接后确定）
	idle   []*ftpConn
	closed bool
	dirs   sync.Map
	// noMLST 为 true 表示服务器不支持 MLST，查询改用 SIZE/MDTM。
	noMLST bool
}

// ftpConn 是一条已登录的连接，以及它拨出的所有网络连接（ctx 取消时一起关掉）。
type ftpConn struct {
	sc       *ftp.ServerConn
	mu       sync.Mutex
	conns    []net.Conn
	dead     bool
	lastUsed time.Time
}

func (c *ftpConn) track(nc net.Conn) {
	c.mu.Lock()
	c.conns = append(c.conns, nc)
	// 只保留还可能在用的：控制连接 + 最近一条数据连接。
	if len(c.conns) > 2 {
		c.conns = append(c.conns[:1], c.conns[len(c.conns)-1])
	}
	c.mu.Unlock()
}

// kill 强行关掉所有网络连接，中断阻塞的读写；之后这条连接不再复用。
func (c *ftpConn) kill() {
	c.mu.Lock()
	c.dead = true
	conns := c.conns
	c.mu.Unlock()
	for _, nc := range conns {
		nc.Close()
	}
}

func (c *ftpConn) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

func (c *ftpConn) quit() {
	done := make(chan struct{})
	go func() {
		c.sc.Quit()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.kill()
	}
}

func openFTP(c FTPConfig, d Deps) (Target, error) {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return nil, errors.New("请填写 FTP 服务器地址")
	}
	if strings.ContainsAny(host, "/ ") {
		return nil, fmt.Errorf("FTP 服务器地址「%s」不正确，只填主机名或 IP", host)
	}
	mode := strings.ToLower(strings.TrimSpace(c.TLS))
	if mode == "" {
		mode = ftpTLSNone
	}
	if mode != ftpTLSNone && mode != ftpTLSExplicit && mode != ftpTLSImplicit {
		return nil, fmt.Errorf("FTP 加密方式「%s」不正确，只能是 none、explicit 或 implicit", c.TLS)
	}
	port := c.Port
	if port == 0 {
		port = 21
		if mode == ftpTLSImplicit {
			port = 990
		}
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("FTP 端口 %d 不正确", c.Port)
	}
	if strings.TrimSpace(c.Username) == "" {
		return nil, errors.New("请填写 FTP 用户名（匿名登录填 anonymous）")
	}
	if strings.ContainsAny(c.Username+c.Password, "\r\n") {
		return nil, errors.New("FTP 用户名和口令不能包含换行")
	}
	dir, abs, err := cleanRemoteDir("FTP", c.Path)
	if err != nil {
		return nil, err
	}
	t := &ftpTarget{
		addr:    net.JoinHostPort(host, strconv.Itoa(port)),
		host:    host,
		user:    strings.TrimSpace(c.Username),
		pw:      c.Password,
		tlsMode: mode,
		dir:     dir,
		abs:     abs,
		dial:    dialFunc(d),
	}
	if mode != ftpTLSNone {
		t.tlsCfg = &tls.Config{
			ServerName:         strings.Trim(host, "[]"),
			InsecureSkipVerify: c.InsecureSkipVerify,
			ClientSessionCache: tls.NewLRUClientSessionCache(16),
			MinVersion:         tls.VersionTLS12,
		}
	}
	return t, nil
}

// dialConn 新建一条连接并登录。
func (t *ftpTarget) dialConn(ctx context.Context) (*ftpConn, error) {
	fc := &ftpConn{}
	first := true
	var fmu sync.Mutex
	dialFn := func(network, addr string) (net.Conn, error) {
		fmu.Lock()
		isControl := first
		first = false
		fmu.Unlock()
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if !isControl {
			// 数据连接的 ctx 是建立连接时的 ctx；后续操作的取消由 kill 负责。
			dctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel2()
			dctx = dctx2
		}
		nc, err := t.dial(dctx, network, addr)
		if err != nil {
			return nil, err
		}
		if t.tlsCfg != nil && (t.tlsMode == ftpTLSImplicit || !isControl) {
			nc = tls.Client(nc, t.tlsCfg)
		}
		fc.track(nc)
		return nc, nil
	}
	opts := []ftp.DialOption{
		ftp.DialWithDialFunc(dialFn),
		ftp.DialWithShutTimeout(30 * time.Second),
	}
	if t.tlsMode == ftpTLSExplicit {
		opts = append(opts, ftp.DialWithExplicitTLS(t.tlsCfg))
	} else if t.tlsMode == ftpTLSImplicit {
		opts = append(opts, ftp.DialWithTLS(t.tlsCfg))
	}
	type result struct {
		sc  *ftp.ServerConn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc, err := ftp.Dial(t.addr, opts...)
		if err == nil {
			err = sc.Login(t.user, t.pw)
			if err != nil {
				sc.Quit()
				sc = nil
			}
		}
		ch <- result{sc, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		fc.kill()
		<-ch
		return nil, ctx.Err()
	}
	if res.err != nil {
		fc.kill()
		return nil, t.connErr(ctx, res.err)
	}
	fc.sc = res.sc
	fc.lastUsed = time.Now()
	if err := t.resolveBase(fc); err != nil {
		fc.quit()
		return nil, err
	}
	return fc, nil
}

func (t *ftpTarget) connErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		switch te.Code {
		case ftp.StatusNotLoggedIn:
			return errors.New("连接 FTP 失败：用户名或口令不正确")
		case ftp.StatusNotAvailable:
			return errors.New("连接 FTP 失败：服务器暂时不可用（421），可能是连接数已满")
		}
		if t.tlsMode == ftpTLSExplicit && (te.Code == ftp.StatusCommandNotImplemented || te.Code == ftp.StatusBadCommand || te.Code == ftp.StatusNotImplemented) {
			return errors.New("连接 FTP 失败：服务器不支持 AUTH TLS（显式加密），请换成隐式加密或不加密")
		}
		return fmt.Errorf("连接 FTP 失败：服务器返回 %d %s", te.Code, strings.TrimSpace(te.Msg))
	}
	return fmt.Errorf("连接 FTP 服务器 %s 失败：%s", t.addr, netErrText(err))
}

// resolveBase 第一次连接后确定绝对根目录（相对路径相对登录后的默认目录）。
func (t *ftpTarget) resolveBase(fc *ftpConn) error {
	t.mu.Lock()
	done := t.base != ""
	t.mu.Unlock()
	if done {
		return nil
	}
	base := "/" + t.dir
	if !t.abs {
		pwd, err := fc.sc.CurrentDir()
		if err != nil || !strings.HasPrefix(pwd, "/") {
			pwd = "/"
		}
		base = path.Join(pwd, t.dir)
	}
	t.mu.Lock()
	if t.base == "" {
		t.base = path.Clean(base)
	}
	t.mu.Unlock()
	return nil
}

func (t *ftpTarget) full(rel string) string {
	t.mu.Lock()
	base := t.base
	t.mu.Unlock()
	if rel == "" {
		return base
	}
	return path.Join(base, rel)
}

// get 取一条可用的连接（池里的或新拨的）。
func (t *ftpTarget) get(ctx context.Context) (*ftpConn, error) {
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, errors.New("FTP 目标已关闭")
		}
		var fc *ftpConn
		if n := len(t.idle); n > 0 {
			fc = t.idle[n-1]
			t.idle = t.idle[:n-1]
		}
		t.mu.Unlock()
		if fc == nil {
			return t.dialConn(ctx)
		}
		if time.Since(fc.lastUsed) < ftpIdleCheck {
			return fc, nil
		}
		// 空闲太久，服务器可能已经断开：NOOP 确认一下。
		stop := context.AfterFunc(ctx, fc.kill)
		err := fc.sc.NoOp()
		stop()
		if err == nil {
			return fc, nil
		}
		fc.kill()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

// put 把连接放回池里（坏掉的、池满的直接断开）。
func (t *ftpTarget) put(fc *ftpConn) {
	if fc.isDead() {
		fc.kill()
		return
	}
	fc.lastUsed = time.Now()
	t.mu.Lock()
	if !t.closed && len(t.idle) < ftpPoolSize {
		t.idle = append(t.idle, fc)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	go fc.quit()
}

var errFTPConnLost = errors.New("与 FTP 服务器的连接已断开")

// isFTPConnErr 判断错误是否意味着连接已经不能用了（网络错误、421 服务关闭）。
func isFTPConnErr(err error) bool {
	if err == nil {
		return false
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code == ftp.StatusNotAvailable
	}
	if errors.Is(err, errFTPConnLost) {
		return true
	}
	var pe textproto.ProtocolError
	if errors.As(err, &pe) {
		return true
	}
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.As(err, &ne)
}

// with 用一条连接执行 fn；ctx 取消时关掉连接中断阻塞调用。连接断开时作废这条连接，retry 为 true（幂等操作）时
// 换一条新连接再试一次。
func (t *ftpTarget) with(ctx context.Context, retry bool, fn func(fc *ftpConn) error) error {
	for attempt := 0; ; attempt++ {
		fc, err := t.get(ctx)
		if err != nil {
			return err
		}
		stop := context.AfterFunc(ctx, fc.kill)
		err = fn(fc)
		if !stop() || ctx.Err() != nil {
			fc.kill()
			if err != nil {
				return ctx.Err()
			}
			return err
		}
		if isFTPConnErr(err) {
			fc.kill()
			if retry && attempt == 0 {
				continue
			}
			return err
		}
		t.put(fc)
		return err
	}
}

func ftpCode(err error) int {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}

// ftpNotFound 判断是不是「文件不存在 / 不可用」一类的永久错误（550、450 等）。
func ftpNotFound(err error) bool {
	c := ftpCode(err)
	return c == ftp.StatusFileUnavailable || c == ftp.StatusFileActionIgnored
}

func ftpErr(op, p string, err error) error {
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		switch te.Code {
		case ftp.StatusNotLoggedIn:
			return errors.New("FTP：登录已失效，请检查用户名和口令")
		case ftp.StatusExceededStorage:
			return fmt.Errorf("%s「%s」失败：FTP 空间不足（%w）", op, p, te)
		}
		// 保留原始错误（421 等要据此判断连接已不可用）。
		return fmt.Errorf("%s「%s」失败：服务器返回 %w", op, p, te)
	}
	if isFTPConnErr(err) {
		return fmt.Errorf("%s「%s」失败：%w（%s）", op, p, errFTPConnLost, netErrText(err))
	}
	return fmt.Errorf("%s「%s」失败：%s", op, p, netErrText(err))
}

// ftpStat 查询单个路径：返回大小、修改时间、是否目录。不存在返回 ErrNotExist。
func (t *ftpTarget) ftpStat(fc *ftpConn, full string) (size int64, mtime time.Time, isDir bool, err error) {
	t.mu.Lock()
	noMLST := t.noMLST
	t.mu.Unlock()
	if !noMLST {
		e, err := fc.sc.GetEntry(full)
		if err == nil {
			return int64(e.Size), e.Time, e.Type == ftp.EntryTypeFolder, nil
		}
		switch c := ftpCode(err); {
		case c == ftp.StatusFileUnavailable || c == ftp.StatusFileActionIgnored:
			return 0, time.Time{}, false, ErrNotExist
		case c == ftp.StatusNotImplemented || c == ftp.StatusCommandNotImplemented || c == ftp.StatusBadCommand ||
			c == ftp.StatusBadArguments || c == ftp.StatusNotImplementedParameter:
			// 服务器不支持 MLST：以后都用 SIZE/MDTM。
			t.mu.Lock()
			t.noMLST = true
			t.mu.Unlock()
		case c == 0 && !isFTPConnErr(err):
			// MLST 的回复格式不标准，库解析不了：这次退回 SIZE/MDTM。
		default:
			return 0, time.Time{}, false, err
		}
	}
	n, err := fc.sc.FileSize(full)
	if err != nil {
		if ftpNotFound(err) {
			// 可能是目录（SIZE 对目录返回 550），也可能不存在：都当成文件不存在。
			return 0, time.Time{}, t.isDir(fc, full), ErrNotExist
		}
		return 0, time.Time{}, false, err
	}
	if fc.sc.IsGetTimeSupported() {
		mtime, _ = fc.sc.GetTime(full)
	}
	return n, mtime, false, nil
}

// isDir 用 CWD 判断路径是不是目录（所有命令都用绝对路径，改变当前目录没有影响）。
func (t *ftpTarget) isDir(fc *ftpConn, full string) bool {
	return fc.sc.ChangeDir(full) == nil
}

// mkdirAll 逐级建目录（已存在的跳过）。
func (t *ftpTarget) mkdirAll(fc *ftpConn, dir string) error {
	if _, ok := t.dirs.Load(dir); ok {
		return nil
	}
	if t.isDir(fc, dir) {
		t.dirs.Store(dir, true)
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		cur += "/" + seg
		if _, ok := t.dirs.Load(cur); ok {
			continue
		}
		if err := fc.sc.MakeDir(cur); err != nil {
			if isFTPConnErr(err) {
				return err
			}
			// 已存在（或没有权限建上级目录但目录本来就在）：确认是目录就继续。
			if !t.isDir(fc, cur) {
				return err
			}
		}
		t.dirs.Store(cur, true)
	}
	return nil
}

func (t *ftpTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	return t.with(ctx, false, func(fc *ftpConn) error {
		final := t.full(p)
		dir := path.Dir(final)
		tmp := path.Join(dir, tempName())
		if err := t.mkdirAll(fc, dir); err != nil {
			t.dirs.Delete(dir)
			return ftpErr("创建目录", dirName(p), err)
		}
		ok := false
		defer func() {
			if !ok {
				if fc.isDead() || ctx.Err() != nil {
					t.removeLater(ctx, tmp)
				} else if err := fc.sc.Delete(tmp); err != nil && isFTPConnErr(err) {
					fc.kill()
					t.removeLater(ctx, tmp)
				}
			}
		}()
		src := &srcErrReader{r: ctxReader{ctx, putReader(r, size)}}
		cnt := &countReader{r: src}
		err := fc.sc.Stor(tmp, cnt)
		if src.err != nil {
			// 数据源出错（引擎放弃、长度不符、取消）：库已正常结束数据连接，临时文件在 defer 里删除。
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, src.err))
		}
		if err != nil {
			return ftpErr("上传", p, err)
		}
		if size >= 0 && cnt.n != size {
			return fmt.Errorf("上传「%s」失败：%w", p, ErrSizeMismatch)
		}
		// 核对服务器上的大小（服务器不支持 SIZE 时跳过）。
		if n, err := fc.sc.FileSize(tmp); err == nil && n != cnt.n {
			return fmt.Errorf("上传「%s」失败：服务器上的文件大小 %d 与实际 %d 不一致", p, n, cnt.n)
		} else if err != nil && isFTPConnErr(err) {
			return ftpErr("上传", p, err)
		}
		if err := fc.sc.Rename(tmp, final); err != nil {
			if isFTPConnErr(err) {
				return ftpErr("保存", p, err)
			}
			// 有的服务器（如 IIS）改名不覆盖已存在的文件：先删再改名（中间有短暂空档，但不会出现半个文件）。
			if derr := fc.sc.Delete(final); derr == nil || ftpNotFound(derr) {
				err = fc.sc.Rename(tmp, final)
			}
			if err != nil {
				return ftpErr("保存", p, err)
			}
		}
		ok = true
		return nil
	})
}

// removeLater 用独立的短超时上下文换一条连接删除临时文件（尽力而为）。
// 上传被中断时，服务器那边原来的连接可能还没关掉文件；Windows 上的 FTP 服务器这时删不掉（文件被占用），
// 所以删除失败时稍等再试几次。
func (t *ftpTarget) removeLater(ctx context.Context, tmp string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	wait := 100 * time.Millisecond
	for attempt := 0; attempt < 5; attempt++ {
		gone := false
		_ = t.with(c, true, func(fc *ftpConn) error {
			derr := fc.sc.Delete(tmp)
			if derr == nil {
				gone = true
				return nil
			}
			if isFTPConnErr(derr) {
				return derr
			}
			// 550 可能是「不存在」，也可能是「被占用删不掉」：查一下文件还在不在
			if _, _, _, serr := t.ftpStat(fc, tmp); errors.Is(serr, ErrNotExist) {
				gone = true
			} else if serr != nil && isFTPConnErr(serr) {
				return serr
			}
			return nil
		})
		if gone {
			return
		}
		select {
		case <-c.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// ftpReader 读取期间独占连接，Close 时结束数据连接并把连接放回池里。
type ftpReader struct {
	t    *ftpTarget
	fc   *ftpConn
	resp *ftp.Response
	stop func() bool
	once sync.Once
}

func (r *ftpReader) Read(p []byte) (int, error) { return r.resp.Read(p) }

func (r *ftpReader) Close() error {
	var err error
	r.once.Do(func() {
		stopped := r.stop()
		err = r.resp.Close()
		if !stopped || err != nil {
			r.fc.kill()
			return
		}
		r.t.put(r.fc)
	})
	return err
}

func (t *ftpTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		fc, err := t.get(ctx)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, fc.kill)
		full := t.full(p)
		size, _, isDir, err := t.ftpStat(fc, full)
		if err == nil && isDir {
			err = ErrNotExist
		}
		var resp *ftp.Response
		if err == nil {
			resp, err = fc.sc.Retr(full)
		}
		if err != nil {
			stopped := stop()
			if !stopped || ctx.Err() != nil {
				fc.kill()
				return nil, ctx.Err()
			}
			if isFTPConnErr(err) {
				fc.kill()
				if attempt == 0 {
					continue
				}
				return nil, ftpErr("读取", p, err)
			}
			t.put(fc)
			if errors.Is(err, ErrNotExist) || ftpNotFound(err) {
				return nil, fmt.Errorf("读取「%s」失败：%w", p, ErrNotExist)
			}
			return nil, ftpErr("读取", p, err)
		}
		return newCheckedReadCloser(&ftpReader{t: t, fc: fc, resp: resp, stop: stop}, size), nil
	}
}

func (t *ftpTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = t.with(ctx, true, func(fc *ftpConn) error {
		size, mtime, isDir, err := t.ftpStat(fc, t.full(p))
		if err == nil && isDir {
			err = ErrNotExist
		}
		if err != nil {
			if errors.Is(err, ErrNotExist) {
				return fmt.Errorf("查询「%s」失败：%w", p, ErrNotExist)
			}
			return ftpErr("查询", p, err)
		}
		e = Entry{Path: p, Size: size, ModTime: mtime}
		return nil
	})
	return e, err
}

func (t *ftpTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	return t.with(ctx, true, func(fc *ftpConn) error {
		full := t.full(p)
		err := fc.sc.Delete(full)
		if err == nil {
			return nil
		}
		if !ftpNotFound(err) {
			return ftpErr("删除", p, err)
		}
		// DELE 失败：不存在视为成功，是目录则报错（DELE 本来就删不掉目录）。
		if t.isDir(fc, full) {
			return fmt.Errorf("删除「%s」失败：这是一个目录", p)
		}
		if _, _, _, serr := t.ftpStat(fc, full); errors.Is(serr, ErrNotExist) {
			return nil
		}
		return ftpErr("删除", p, err)
	})
}

func (t *ftpTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = t.with(ctx, true, func(fc *ftpConn) error {
		out = nil
		var walk func(rel string) error
		walk = func(rel string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// 先 CWD 再不带参数地列出：LIST 的参数在很多服务器上会被当成 ls 选项，带空格的路径不可靠。
			if err := fc.sc.ChangeDir(t.full(rel)); err != nil {
				if ftpNotFound(err) {
					return nil // 目录不存在（或遍历期间被删）
				}
				return err
			}
			entries, err := fc.sc.List("")
			if err != nil {
				if ftpNotFound(err) {
					return nil
				}
				return err
			}
			for _, e := range entries {
				name := e.Name
				if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
					continue
				}
				child := joinKey(rel, name)
				switch e.Type {
				case ftp.EntryTypeFolder:
					if wantDir(child, prefix) {
						if err := walk(child); err != nil {
							return err
						}
					}
					continue
				case ftp.EntryTypeLink:
					continue
				}
				if isTempName(name) {
					if isStaleTemp(name, e.Time) {
						fc.sc.Delete(t.full(child)) // 上次中断留下的临时文件
					}
					continue
				}
				if !strings.HasPrefix(child, prefix) {
					continue
				}
				if _, err := cleanPath(child); err != nil {
					continue
				}
				out = append(out, Entry{Path: child, Size: int64(e.Size), ModTime: e.Time})
			}
			return nil
		}
		if err := walk(prefixDir(prefix)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ftpErr("列出", prefix, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *ftpTarget) Close() error {
	t.mu.Lock()
	t.closed = true
	idle := t.idle
	t.idle = nil
	t.mu.Unlock()
	for _, fc := range idle {
		fc.quit()
	}
	return nil
}
