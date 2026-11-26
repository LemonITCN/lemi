package dumptools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Conn 是客户端连接参数（密钥已解密）。
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	// TLSMode 与 Lemi 连接设置一致：disable | preferred | required | verify-ca | verify-full
	TLSMode string
	TLSCA   string // PEM
	TLSCert string // PEM
	TLSKey  string // PEM
	// Dial 非空时客户端不直接连数据库，而是连本机回环地址上的临时转发端口，转发经 Dial 拨真实目标
	// （SSH 隧道、代理、网络策略都在 Dial 里）。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DumpOptions 是 mysqldump 选项。
type DumpOptions struct {
	Databases []string // 必填：要导出的库
	// NoColumnStatistics 加 --column-statistics=0（MySQL 5.7 与 MariaDB 服务器）
	NoColumnStatistics bool
}

// session 是一次运行的临时文件与转发。
type session struct {
	dir      string
	defaults string
	fwd      *Forwarder
	warnings []string
}

func (s *session) close() {
	if s.fwd != nil {
		s.fwd.Close()
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// optQuote 按 MySQL 选项文件的规则给值加双引号并转义（my_default.cc：\n \t \r \b \s \" \' \\）。
func optQuote(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// prepare 写临时选项文件（0600，只含本次需要的 [client] 选项）并按需开转发。tmpRoot 为空时用系统临时目录。
func (t *Tool) prepare(ctx context.Context, c Conn, tmpRoot string) (*session, error) {
	if tmpRoot != "" {
		if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp(tmpRoot, "mysql-client-")
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	s := &session{dir: dir}
	ok := false
	defer func() {
		if !ok {
			s.close()
		}
	}()
	host, port := c.Host, c.Port
	if port == 0 {
		port = 3306
	}
	if c.Dial != nil {
		fwd, err := Forward(c.Dial, net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return nil, err
		}
		s.fwd = fwd
		h, p, _ := net.SplitHostPort(fwd.Addr())
		host = h
		port, _ = strconv.Atoi(p)
	}
	var lines []string
	add := func(k, v string) { lines = append(lines, k+"="+v) }
	add("user", optQuote(c.User))
	if c.Password != "" {
		add("password", optQuote(c.Password))
	}
	add("host", optQuote(host))
	add("port", strconv.Itoa(port))
	add("protocol", "TCP")
	add("default-character-set", "utf8mb4")
	writePEM := func(name, pem string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(pem), 0o600)
	}
	mode := c.TLSMode
	if mode == "" {
		mode = "preferred"
	}
	if mode == "verify-full" && c.Dial != nil {
		// 客户端连的是本机转发地址，主机名校验必然失败：只校验 CA
		mode = "verify-ca"
		s.warnings = append(s.warnings, "经 SSH 隧道或代理时 mysqldump 无法校验证书主机名，本次只校验 CA")
	}
	if t.MariaDB {
		switch mode {
		case "disable":
			add("skip-ssl", "")
		case "preferred":
		default:
			add("ssl", "")
			if mode == "verify-ca" || mode == "verify-full" {
				add("ssl-verify-server-cert", "")
			}
		}
		lines = fixFlags(lines)
	} else {
		m := map[string]string{"disable": "DISABLED", "preferred": "PREFERRED", "required": "REQUIRED", "verify-ca": "VERIFY_CA", "verify-full": "VERIFY_IDENTITY"}[mode]
		if m == "" {
			return nil, fmt.Errorf("未知的加密方式 %q", c.TLSMode)
		}
		add("ssl-mode", m)
	}
	if mode != "disable" {
		if c.TLSCA != "" && (mode == "verify-ca" || mode == "verify-full") {
			p, err := writePEM("ca.pem", c.TLSCA)
			if err != nil {
				return nil, err
			}
			add("ssl-ca", optQuote(p))
		}
		if c.TLSCert != "" && c.TLSKey != "" {
			cp, err := writePEM("cert.pem", c.TLSCert)
			if err != nil {
				return nil, err
			}
			kp, err := writePEM("key.pem", c.TLSKey)
			if err != nil {
				return nil, err
			}
			add("ssl-cert", optQuote(cp))
			add("ssl-key", optQuote(kp))
		}
	}
	s.defaults = filepath.Join(dir, "client.cnf")
	content := "[client]\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(s.defaults, []byte(content), 0o600); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// fixFlags 把没有值的选项写成不带等号的形式（MariaDB 客户端的开关选项）。
func fixFlags(lines []string) []string {
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "=")
	}
	return lines
}

// cleanEnv 去掉会影响客户端连接目标或口令的环境变量。
func cleanEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		uk := strings.ToUpper(k)
		if strings.HasPrefix(uk, "MYSQL_") || uk == "MARIADB_HOST" || uk == "LIBMYSQL_ENABLE_CLEARTEXT_PLUGIN" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// limitedBuffer 只保留前 max 字节（收集错误输出）。
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(l.buf.String())
}

// DumpArgs 返回 mysqldump 参数（选项文件参数必须在第一个）。
func (t *Tool) DumpArgs(defaults string, o DumpOptions) []string {
	args := []string{"--defaults-file=" + defaults,
		"--single-transaction", "--quick", "--routines", "--triggers", "--events", "--hex-blob",
		"--default-character-set=utf8mb4", "--max-allowed-packet=1G", "--no-tablespaces"}
	if !t.MariaDB {
		args = append(args, "--set-gtid-purged=OFF")
		if o.NoColumnStatistics {
			args = append(args, "--column-statistics=0")
		}
	}
	args = append(args, "--databases")
	args = append(args, o.Databases...)
	return args
}

// Dump 运行 mysqldump，把输出流式写进 out。返回警告（例如经隧道时改为只校验 CA）。
func (t *Tool) Dump(ctx context.Context, c Conn, o DumpOptions, tmpRoot string, out io.Writer) ([]string, error) {
	if len(o.Databases) == 0 {
		return nil, errors.New("没有要导出的库")
	}
	for _, d := range o.Databases {
		if d == "" || strings.HasPrefix(d, "-") || strings.ContainsRune(d, 0) {
			return nil, fmt.Errorf("库名 %q 不能用于 mysqldump", d)
		}
	}
	s, err := t.prepare(ctx, c, tmpRoot)
	if err != nil {
		return nil, err
	}
	defer s.close()
	cmd := exec.CommandContext(ctx, t.Mysqldump, t.DumpArgs(s.defaults, o)...)
	cmd.Env = cleanEnv()
	cmd.Dir = s.dir
	cmd.Stdout = out
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return s.warnings, ctx.Err()
		}
		return s.warnings, clientErr("mysqldump", err, stderr.String())
	}
	if msg := stderr.String(); msg != "" {
		// mysqldump 的警告（例如权限不足导致某些对象被跳过）
		for _, l := range strings.Split(msg, "\n") {
			if l = strings.TrimSpace(l); l != "" && len(s.warnings) < 20 {
				s.warnings = append(s.warnings, "mysqldump："+l)
			}
		}
	}
	return s.warnings, nil
}

// Restore 用 mysql 客户端执行 in 里的 SQL（database 为空表示按文件里的 USE）。
func (t *Tool) Restore(ctx context.Context, c Conn, database, tmpRoot string, in io.Reader) ([]string, error) {
	s, err := t.prepare(ctx, c, tmpRoot)
	if err != nil {
		return nil, err
	}
	defer s.close()
	args := []string{"--defaults-file=" + s.defaults, "--default-character-set=utf8mb4", "--binary-mode", "--max-allowed-packet=1G", "--batch", "--silent"}
	if database != "" {
		if strings.HasPrefix(database, "-") {
			return nil, fmt.Errorf("库名 %q 不能用于 mysql 客户端", database)
		}
		args = append(args, "--database="+database)
	}
	cmd := exec.CommandContext(ctx, t.Mysql, args...)
	cmd.Env = cleanEnv()
	cmd.Dir = s.dir
	cmd.Stdin = in
	cmd.Stdout = io.Discard
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return s.warnings, ctx.Err()
		}
		return s.warnings, clientErr("mysql", err, stderr.String())
	}
	return s.warnings, nil
}

func clientErr(name string, err error, stderr string) error {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		// 用选项文件传口令时客户端不会打印口令；这句提示没有信息量
		if l == "" || strings.Contains(l, "Using a password on the command line") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > 8 {
		lines = append(lines[:8], "…")
	}
	if len(lines) == 0 {
		return fmt.Errorf("%s 运行失败：%v", name, err)
	}
	msg := fmt.Sprintf("%s 运行失败：%s", name, strings.Join(lines, "；"))
	// 官方客户端依赖系统的 ncurses（不随包打包，见 docs/spec.md §13）
	if strings.Contains(stderr, "libncurses") || strings.Contains(stderr, "libtinfo") {
		msg += "。缺少系统库 ncurses：Debian / Ubuntu 执行 apt install libncurses6，RHEL / Rocky / Alma 执行 dnf install ncurses-libs；也可以改用 Lemi 自带的导出器备份"
	}
	return errors.New(msg)
}
