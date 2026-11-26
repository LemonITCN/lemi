package target

// SMB（Windows 共享、群晖 / 威联通等 NAS 的 SMB 共享）基于 github.com/cloudsoda/go-smb2（SMB 2/3，NTLM 认证）。
// 经 Deps.Dial 拿到 TCP 连接后握手、挂载共享；一个目标共用一条会话（go-smb2 支持同一会话上并发请求），
// 连接断开后下次操作自动重连，幂等操作重连后再试一次。
// 写入：同目录临时名（~$xxxx.tmp）→ 写完核对大小 → 改名。go-smb2 的改名不覆盖已存在的文件，目标已存在时
// 先删再改名（中间有短暂空档，但不会出现半个文件）。数据源出错或 ctx 取消时删除临时文件。
// 流式写入无法中途重试，由备份引擎整次重试。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudsoda/go-smb2"
)

// SMBConfig 是 SMB 共享的配置。
type SMBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`  // 缺省 445
	Share    string `json:"share"` // 共享名，例如 backup
	Username string `json:"username"`
	Password string `json:"password"`
	Domain   string `json:"domain"` // 域（工作组环境留空）
	Path     string `json:"path"`   // 共享里的子目录，留空为共享根目录
}

type smbTarget struct {
	addr   string
	host   string
	share  string
	user   string
	pw     string
	domain string
	base   string // 共享里的根目录（用 / 分隔，"" 表示共享根目录）
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)

	mu      sync.Mutex
	conn    net.Conn
	session *smb2.Session
	fs      *smb2.Share
	dirs    sync.Map
}

func openSMB(c SMBConfig, d Deps) (Target, error) {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return nil, errors.New("请填写 SMB 服务器地址")
	}
	if strings.ContainsAny(host, "/\\ ") {
		return nil, fmt.Errorf("SMB 服务器地址「%s」不正确，只填主机名或 IP", host)
	}
	port := c.Port
	if port == 0 {
		port = 445
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("SMB 端口 %d 不正确", c.Port)
	}
	share := strings.Trim(strings.TrimSpace(c.Share), `/\`)
	if share == "" {
		return nil, errors.New("请填写 SMB 共享名")
	}
	if strings.ContainsAny(share, `/\:*?"<>|`) {
		return nil, fmt.Errorf("SMB 共享名「%s」不正确", share)
	}
	if strings.TrimSpace(c.Username) == "" {
		return nil, errors.New("请填写 SMB 用户名（来宾访问填 guest）")
	}
	dir, _, err := cleanRemoteDir("SMB", c.Path)
	if err != nil {
		return nil, err
	}
	return &smbTarget{
		addr:   net.JoinHostPort(host, strconv.Itoa(port)),
		host:   strings.Trim(host, "[]"),
		share:  share,
		user:   strings.TrimSpace(c.Username),
		pw:     c.Password,
		domain: strings.TrimSpace(c.Domain),
		base:   dir,
		dial:   dialFunc(d),
	}, nil
}

// connect 返回已挂载的共享，没有连接时建立。
func (t *smbTarget) connect(ctx context.Context) (*smb2.Share, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fs != nil {
		return t.fs, nil
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := t.dial(dctx, "tcp", t.addr)
	if err != nil {
		return nil, fmt.Errorf("连接 SMB 服务器 %s 失败：%s", t.addr, netErrText(ctxErr(ctx, err)))
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: t.user, Password: t.pw, Domain: t.domain}}
	s, err := d.DialConn(dctx, conn, t.addr)
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch smbStatus(err) {
		case ntStatusLogonFailure, ntStatusWrongPassword:
			return nil, errors.New("连接 SMB 失败：用户名或口令不正确")
		case ntStatusAccountDisabled, ntStatusAccountLockedOut, ntStatusPasswordExpired, ntStatusAccountRestriction:
			return nil, fmt.Errorf("连接 SMB 失败：账号不可用（已停用、被锁定或口令已过期）：%s", smbErrText(err))
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, errors.New("连接 SMB 失败：用户名或口令不正确，或没有访问权限")
		}
		return nil, fmt.Errorf("连接 SMB 失败：%s", smbErrText(err))
	}
	fsys, err := s.WithContext(dctx).Mount(`\\` + t.host + `\` + t.share)
	if err != nil {
		s.Logoff()
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if smbStatus(err) == ntStatusBadNetworkName || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("SMB 共享「%s」不存在", t.share)
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("没有权限访问 SMB 共享「%s」", t.share)
		}
		return nil, fmt.Errorf("挂载 SMB 共享「%s」失败：%s", t.share, smbErrText(err))
	}
	t.conn, t.session, t.fs = conn, s, fsys
	return fsys, nil
}

// drop 作废当前连接（断线后下次操作重连）。
func (t *smbTarget) drop(fsys *smb2.Share) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fs != fsys || t.fs == nil {
		return
	}
	t.conn.Close()
	t.conn, t.session, t.fs = nil, nil, nil
	t.dirs.Clear()
}

// dropCurrent 作废当前连接（不管是哪一条）。
func (t *smbTarget) dropCurrent() {
	t.mu.Lock()
	fsys := t.fs
	t.mu.Unlock()
	if fsys != nil {
		t.drop(fsys)
	}
}

var errSMBConnLost = errors.New("与 SMB 服务器的连接已断开")

func isSMBConnErr(err error) bool {
	var te *smb2.TransportError
	var ne net.Error
	return errors.As(err, &te) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.As(err, &ne) || errors.Is(err, errSMBConnLost)
}

// with 执行一次操作；连接断开时作废连接，retry 为 true（幂等操作）时重连后再试一次。
func (t *smbTarget) with(ctx context.Context, retry bool, fn func(fsys *smb2.Share) error) error {
	for attempt := 0; ; attempt++ {
		fsys, err := t.connect(ctx)
		if err != nil {
			return err
		}
		err = fn(fsys.WithContext(ctx))
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && isSMBConnErr(err) {
			t.drop(fsys)
			if retry && attempt == 0 {
				continue
			}
		}
		return err
	}
}

// smbPath 把相对根目录的路径转成共享内的路径（go-smb2 会把 / 换成 \）。
func (t *smbTarget) smbPath(rel string) string {
	switch {
	case t.base == "":
		return rel
	case rel == "":
		return t.base
	}
	return t.base + "/" + rel
}

// 用到的 NTSTATUS（[MS-ERREF] 2.3.1）。
const (
	ntStatusLogonFailure       = 0xC000006D
	ntStatusWrongPassword      = 0xC000006A
	ntStatusAccountRestriction = 0xC000006E
	ntStatusPasswordExpired    = 0xC0000071
	ntStatusAccountDisabled    = 0xC0000072
	ntStatusAccountLockedOut   = 0xC0000234
	ntStatusBadNetworkName     = 0xC00000CC
	ntStatusDiskFull           = 0xC000007F
)

func smbStatus(err error) uint32 {
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

func smbErrText(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return netErrText(err)
	}
	var te *smb2.TransportError
	if errors.As(err, &te) {
		return "连接已断开：" + netErrText(te.Err)
	}
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		return re.Error()
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return smbErrText(pe.Err)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return smbErrText(le.Err)
	}
	return netErrText(err)
}

func smbErr(op, p string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s「%s」失败：没有权限", op, p)
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	if isSMBConnErr(err) {
		return fmt.Errorf("%s「%s」失败：%w（%s）", op, p, errSMBConnLost, smbErrText(err))
	}
	if smbStatus(err) == ntStatusDiskFull {
		return fmt.Errorf("%s「%s」失败：SMB 共享空间不足", op, p)
	}
	return fmt.Errorf("%s「%s」失败：%s", op, p, smbErrText(err))
}

func (t *smbTarget) mkdirAll(fsys *smb2.Share, dir string) error {
	if dir == "" {
		return nil
	}
	if _, ok := t.dirs.Load(dir); ok {
		return nil
	}
	if err := fsys.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t.dirs.Store(dir, true)
	return nil
}

func (t *smbTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	final := t.smbPath(p)
	dir := dirName(final)
	tmp := joinKey(dir, tempName())
	return t.with(ctx, false, func(fsys *smb2.Share) error {
		if err := t.mkdirAll(fsys, dir); err != nil {
			t.dirs.Delete(dir)
			return smbErr("创建目录", dirName(p), err)
		}
		f, err := fsys.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.dirs.Delete(dir)
			return smbErr("创建临时文件", p, err)
		}
		ok := false
		defer func() {
			if !ok {
				if cerr := f.Close(); cerr != nil || ctx.Err() != nil {
					// 关不掉文件（ctx 已取消或连接出错）：断开连接让服务器释放句柄，否则删不掉临时文件。
					t.dropCurrent()
				}
				t.removeQuiet(ctx, tmp)
			}
		}()
		src := &srcErrReader{r: ctxReader{ctx, putReader(r, size)}}
		n, err := f.ReadFrom(src)
		if src.err != nil {
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, src.err))
		}
		if err != nil {
			return smbErr("上传", p, err)
		}
		if size >= 0 && n != size {
			return fmt.Errorf("上传「%s」失败：%w", p, ErrSizeMismatch)
		}
		st, err := f.Stat()
		if err != nil {
			return smbErr("上传", p, err)
		}
		if st.Size() != n {
			return fmt.Errorf("上传「%s」失败：服务器上的文件大小 %d 与实际 %d 不一致", p, st.Size(), n)
		}
		if err := f.Close(); err != nil {
			return smbErr("上传", p, err)
		}
		if err := fsys.Rename(tmp, final); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return smbErr("保存", p, err)
			}
			// 目标已存在：先删再改名。
			if rerr := fsys.Remove(final); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return smbErr("保存", p, rerr)
			}
			if err := fsys.Rename(tmp, final); err != nil {
				return smbErr("保存", p, err)
			}
		}
		ok = true
		return nil
	})
}

// removeQuiet 用独立的短超时上下文删除临时文件（连接断了就重连，尽力而为）。
func (t *smbTarget) removeQuiet(ctx context.Context, name string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = t.with(c, true, func(fsys *smb2.Share) error {
		if err := fsys.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (t *smbTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	var rc io.ReadCloser
	err = t.with(ctx, true, func(fsys *smb2.Share) error {
		f, err := fsys.Open(t.smbPath(p))
		if err != nil {
			return smbErr("读取", p, err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return smbErr("读取", p, err)
		}
		if !st.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("读取「%s」失败：%w（不是普通文件）", p, ErrNotExist)
		}
		rc = newCheckedReadCloser(f, st.Size())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func (t *smbTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = t.with(ctx, true, func(fsys *smb2.Share) error {
		st, err := fsys.Stat(t.smbPath(p))
		if err != nil {
			return smbErr("查询", p, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("查询「%s」失败：%w（不是普通文件）", p, ErrNotExist)
		}
		e = Entry{Path: p, Size: st.Size(), ModTime: st.ModTime()}
		return nil
	})
	return e, err
}

func (t *smbTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	return t.with(ctx, true, func(fsys *smb2.Share) error {
		name := t.smbPath(p)
		st, err := fsys.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return smbErr("删除", p, err)
		}
		if st.IsDir() {
			// go-smb2 的 Remove 会删掉空目录，这里先拦住。
			return fmt.Errorf("删除「%s」失败：这是一个目录", p)
		}
		if err := fsys.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return smbErr("删除", p, err)
		}
		return nil
	})
}

func (t *smbTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = t.with(ctx, true, func(fsys *smb2.Share) error {
		out = nil
		var walk func(rel string) error
		walk = func(rel string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			infos, err := fsys.ReadDir(t.smbPath(rel))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			for _, fi := range infos {
				name := fi.Name()
				if name == "" || name == "." || name == ".." {
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
						fsys.Remove(t.smbPath(child)) // 上次中断留下的临时文件
					}
					continue
				}
				if !fi.Mode().IsRegular() || !strings.HasPrefix(child, prefix) {
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
			return smbErr("列出", prefix, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *smbTarget) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fs == nil {
		return nil
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.fs.WithContext(c).Umount()
	t.session.WithContext(c).Logoff()
	err := t.conn.Close()
	t.conn, t.session, t.fs = nil, nil, nil
	return err
}
