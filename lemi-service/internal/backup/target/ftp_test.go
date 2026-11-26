package target

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ftpTestServer 是测试用的最小 FTP 服务器：USER/PASS/FEAT/TYPE/OPTS/AUTH TLS/PBSZ/PROT/EPSV/PASV/STOR/RETR/
// LIST/MLSD/MLST/DELE/RNFR/RNTO/MKD/SIZE/MDTM/CWD/PWD/NOOP/QUIT，文件存在临时目录里。
type ftpTestServer struct {
	t           *testing.T
	ln          net.Listener
	root        string
	user, pass  string
	mlst        bool        // 声明并支持 MLST/MLSD
	tlsCfg      *tls.Config // 非空时支持加密
	implicit    bool        // 隐式 TLS：控制连接一连上就是 TLS
	noOverwrite bool        // RNTO 不覆盖已存在的文件（像 IIS）
	noEPSV      bool        // 不支持 EPSV，只能 PASV
	stors       atomic.Int64
	dataTLS     atomic.Int64 // 加密的数据连接数
	wg          sync.WaitGroup
}

func startFTPServer(t *testing.T, s *ftpTestServer) *ftpTestServer {
	t.Helper()
	s.t = t
	s.root = t.TempDir()
	if s.user == "" {
		s.user, s.pass = "backup", "ftp 口令"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	t.Cleanup(func() {
		ln.Close()
		s.wg.Wait()
	})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if s.implicit {
				c = tls.Server(c, s.tlsCfg)
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(c)
			}()
		}
	}()
	return s
}

func (s *ftpTestServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// local 把 FTP 路径（绝对或相对 cwd）转成本地路径。
func (s *ftpTestServer) local(cwd, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = path.Join(cwd, p)
	}
	return filepath.Join(s.root, filepath.FromSlash(path.Clean("/"+p)))
}

type ftpSession struct {
	s        *ftpTestServer
	conn     net.Conn
	r        *bufio.Reader
	cwd      string
	userOK   bool
	loggedIn bool
	prot     bool
	pasv     net.Listener
	rnfr     string
}

func (fs *ftpSession) reply(format string, args ...any) {
	fmt.Fprintf(fs.conn, format+"\r\n", args...)
}

func (s *ftpTestServer) serve(c net.Conn) {
	fs := &ftpSession{s: s, conn: c, r: bufio.NewReader(c), cwd: "/"}
	defer func() {
		c.Close()
		if fs.pasv != nil {
			fs.pasv.Close()
		}
	}()
	fs.reply("220 Lemi test FTP ready")
	for {
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, err := fs.r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		cmd = strings.ToUpper(cmd)
		if !fs.loggedIn {
			switch cmd {
			case "USER", "PASS", "AUTH", "FEAT", "QUIT", "PBSZ", "PROT":
			default:
				fs.reply("530 Please login with USER and PASS")
				continue
			}
		}
		if fs.handle(cmd, arg) {
			return
		}
	}
}

// openData 接受数据连接（客户端在发命令前已经连上了被动端口）。
func (fs *ftpSession) openData() (net.Conn, error) {
	if fs.pasv == nil {
		return nil, errors.New("no pasv")
	}
	fs.pasv.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	c, err := fs.pasv.Accept()
	fs.pasv.Close()
	fs.pasv = nil
	if err != nil {
		return nil, err
	}
	if fs.prot {
		fs.s.dataTLS.Add(1)
		tc := tls.Server(c, fs.s.tlsCfg)
		return tc, nil
	}
	return c, nil
}

func (fs *ftpSession) dropData() {
	if fs.pasv != nil {
		fs.pasv.Close()
		fs.pasv = nil
	}
}

func mlsxFacts(fi os.FileInfo) string {
	typ := "file"
	if fi.IsDir() {
		typ = "dir"
	}
	return fmt.Sprintf("type=%s;size=%d;modify=%s;", typ, fi.Size(), fi.ModTime().UTC().Format("20060102150405"))
}

// handle 处理一条命令，返回 true 表示结束会话。
func (fs *ftpSession) handle(cmd, arg string) bool {
	s := fs.s
	switch cmd {
	case "USER":
		fs.userOK = arg == s.user
		fs.reply("331 Password required")
	case "PASS":
		if fs.userOK && arg == s.pass {
			fs.loggedIn = true
			fs.reply("230 Logged in")
		} else {
			fs.reply("530 Login incorrect")
		}
	case "FEAT":
		var b strings.Builder
		b.WriteString("211-Features:\r\n EPSV\r\n PASV\r\n SIZE\r\n MDTM\r\n UTF8\r\n")
		if s.mlst {
			b.WriteString(" MLST type*;size*;modify*;\r\n")
		}
		if s.tlsCfg != nil {
			b.WriteString(" AUTH TLS\r\n PBSZ\r\n PROT\r\n")
		}
		b.WriteString("211 End")
		fs.reply("%s", b.String())
	case "AUTH":
		if s.tlsCfg == nil || s.implicit {
			fs.reply("502 Not implemented")
			return false
		}
		fs.reply("234 Proceed with negotiation")
		tc := tls.Server(fs.conn, s.tlsCfg)
		if err := tc.Handshake(); err != nil {
			return true
		}
		fs.conn = tc
		fs.r = bufio.NewReader(tc)
	case "PBSZ":
		fs.reply("200 PBSZ=0")
	case "PROT":
		fs.prot = strings.EqualFold(arg, "P")
		fs.reply("200 Protection level set")
	case "TYPE", "OPTS":
		fs.reply("200 OK")
	case "NOOP":
		fs.reply("200 NOOP ok")
	case "PWD":
		fs.reply("257 \"%s\" is the current directory", fs.cwd)
	case "CWD":
		p := fs.s.local(fs.cwd, arg)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if strings.HasPrefix(arg, "/") {
				fs.cwd = path.Clean(arg)
			} else {
				fs.cwd = path.Join(fs.cwd, arg)
			}
			fs.reply("250 Directory changed")
		} else {
			fs.reply("550 Failed to change directory")
		}
	case "EPSV", "PASV":
		if cmd == "EPSV" && s.noEPSV {
			fs.reply("500 EPSV not understood")
			return false
		}
		fs.dropData()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fs.reply("425 Cannot open passive connection")
			return false
		}
		fs.pasv = ln
		p := ln.Addr().(*net.TCPAddr).Port
		if cmd == "EPSV" {
			fs.reply("229 Entering Extended Passive Mode (|||%d|)", p)
		} else {
			// 故意给一个错误的地址，客户端应该忽略它，连控制连接的地址。
			fs.reply("227 Entering Passive Mode (10,9,8,7,%d,%d)", p/256, p%256)
		}
	case "STOR":
		lp := s.local(fs.cwd, arg)
		f, err := os.OpenFile(lp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			fs.dropData()
			fs.reply("553 Could not create file")
			return false
		}
		s.stors.Add(1)
		fs.reply("150 Ok to send data")
		dc, err := fs.openData()
		if err != nil {
			f.Close()
			fs.reply("425 Cannot open data connection")
			return false
		}
		_, cerr := io.Copy(f, dc)
		dc.Close()
		f.Close()
		if cerr != nil {
			fs.reply("426 Connection closed; transfer aborted")
			return false
		}
		fs.reply("226 Transfer complete")
	case "RETR":
		lp := s.local(fs.cwd, arg)
		st, err := os.Stat(lp)
		if err != nil || st.IsDir() {
			fs.dropData()
			fs.reply("550 Failed to open file")
			return false
		}
		fs.reply("150 Opening BINARY mode data connection")
		dc, err := fs.openData()
		if err != nil {
			fs.reply("425 Cannot open data connection")
			return false
		}
		f, _ := os.Open(lp)
		io.Copy(dc, f)
		f.Close()
		dc.Close()
		fs.reply("226 Transfer complete")
	case "LIST", "MLSD":
		if cmd == "MLSD" && !s.mlst {
			fs.dropData()
			fs.reply("500 Unknown command")
			return false
		}
		target := fs.cwd
		if arg != "" && !strings.HasPrefix(arg, "-") {
			target = arg
		}
		lp := s.local(fs.cwd, target)
		ents, err := os.ReadDir(lp)
		if err != nil {
			fs.dropData()
			fs.reply("550 Directory not found")
			return false
		}
		fs.reply("150 Here comes the directory listing")
		dc, err := fs.openData()
		if err != nil {
			fs.reply("425 Cannot open data connection")
			return false
		}
		w := bufio.NewWriter(dc)
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if cmd == "MLSD" {
				fmt.Fprintf(w, "%s %s\r\n", mlsxFacts(fi), e.Name())
				continue
			}
			mode := "-rw-r--r--"
			if fi.IsDir() {
				mode = "drwxr-xr-x"
			}
			mt := fi.ModTime().UTC()
			fmt.Fprintf(w, "%s    1 ftp      ftp      %12d %s %2d %5d %s\r\n", mode, fi.Size(), mt.Format("Jan"), mt.Day(), mt.Year(), e.Name())
		}
		w.Flush()
		dc.Close()
		fs.reply("226 Directory send OK")
	case "MLST":
		if !s.mlst {
			fs.reply("500 Unknown command")
			return false
		}
		st, err := os.Stat(s.local(fs.cwd, arg))
		if err != nil {
			fs.reply("550 No such file")
			return false
		}
		fs.reply("250-Listing %s\r\n %s %s\r\n250 End", arg, mlsxFacts(st), arg)
	case "SIZE", "MDTM":
		st, err := os.Stat(s.local(fs.cwd, arg))
		if err != nil || st.IsDir() {
			fs.reply("550 Could not get file size")
			return false
		}
		if cmd == "SIZE" {
			fs.reply("213 %d", st.Size())
		} else {
			fs.reply("213 %s", st.ModTime().UTC().Format("20060102150405"))
		}
	case "DELE":
		lp := s.local(fs.cwd, arg)
		st, err := os.Stat(lp)
		if err != nil || st.IsDir() {
			fs.reply("550 Delete operation failed")
			return false
		}
		os.Remove(lp)
		fs.reply("250 Delete operation successful")
	case "RNFR":
		if _, err := os.Stat(s.local(fs.cwd, arg)); err != nil {
			fs.reply("550 RNFR command failed")
			return false
		}
		fs.rnfr = s.local(fs.cwd, arg)
		fs.reply("350 Ready for RNTO")
	case "RNTO":
		to := s.local(fs.cwd, arg)
		if fs.rnfr == "" {
			fs.reply("503 RNFR required first")
			return false
		}
		if _, err := os.Stat(to); err == nil && s.noOverwrite {
			fs.reply("553 Rename failed: file exists")
			return false
		}
		if err := os.Rename(fs.rnfr, to); err != nil {
			fs.reply("550 Rename failed")
			return false
		}
		fs.rnfr = ""
		fs.reply("250 Rename successful")
	case "MKD":
		if err := os.Mkdir(s.local(fs.cwd, arg), 0o755); err != nil {
			fs.reply("550 Create directory operation failed")
			return false
		}
		fs.reply("257 \"%s\" created", arg)
	case "QUIT":
		fs.reply("221 Goodbye")
		return true
	default:
		fs.reply("502 Command not implemented")
	}
	return false
}

// testTLSConfig 生成一张自签名证书（127.0.0.1）。
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "lemi-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

type dialCounter struct{ n atomic.Int64 }

func (d *dialCounter) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.n.Add(1)
	var nd net.Dialer
	return nd.DialContext(ctx, network, addr)
}

func openFTPT(t *testing.T, c FTPConfig, d Deps) Target {
	t.Helper()
	b, _ := json.Marshal(c)
	tg, err := Open(KindFTP, b, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tg.Close() })
	return tg
}

func (s *ftpTestServer) config() FTPConfig {
	return FTPConfig{Host: "127.0.0.1", Port: s.port(), Username: s.user, Password: s.pass, Path: "backups/数据 库"}
}

func testFTPSuite(t *testing.T, s *ftpTestServer, c FTPConfig) {
	var dc dialCounter
	tg := openFTPT(t, c, Deps{Dial: dc.dial})
	runSuite(t, tg, suiteOpts{bigSize: 2<<20 + 17})
	runStreamSuite(t, tg, streamOpts{bigSize: 2<<20 + 9, abortAt: 1 << 20, noLeftover: func(t *testing.T) { noTempFiles(t, s.root) }})
	if _, err := os.Stat(filepath.Join(s.root, "backups", "数据 库", "dir", "sub", "c.bin")); err != nil {
		t.Fatalf("文件不在配置的目录下：%v", err)
	}
	noTempFiles(t, s.root)
	if dc.n.Load() < 2 {
		t.Fatalf("控制连接与数据连接都应经 Deps.Dial 拨号，实际只拨了 %d 次", dc.n.Load())
	}
}

func TestFTPSuiteMLSD(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true})
	testFTPSuite(t, s, s.config())
}

func TestFTPSuiteLIST(t *testing.T) {
	// 不支持 MLST/MLSD、不支持 EPSV、改名不覆盖：退回 LIST、SIZE/MDTM、PASV、先删再改名。
	s := startFTPServer(t, &ftpTestServer{noOverwrite: true, noEPSV: true})
	testFTPSuite(t, s, s.config())
}

func TestFTPExplicitTLS(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true, tlsCfg: testTLSConfig(t)})
	c := s.config()
	c.TLS, c.InsecureSkipVerify = "explicit", true
	var dc dialCounter
	tg := openFTPT(t, c, Deps{Dial: dc.dial})
	if err := Probe(context.Background(), tg); err != nil {
		t.Fatalf("显式 TLS Probe 失败：%v", err)
	}
	runStreamSuite(t, tg, streamOpts{bigSize: 1<<20 + 9, abortAt: 256 << 10, noLeftover: func(t *testing.T) { noTempFiles(t, s.root) }})
	if s.dataTLS.Load() == 0 {
		t.Fatal("显式 TLS 下数据连接也应加密")
	}
	if dc.n.Load() < 2 {
		t.Fatal("显式 TLS 下数据连接也应经 Deps.Dial 拨号")
	}
	// 不跳过证书校验时，自签名证书应被拒绝。
	c.InsecureSkipVerify = false
	err := Probe(context.Background(), openFTPT(t, c, Deps{}))
	if err == nil || !strings.Contains(err.Error(), "证书") {
		t.Fatalf("自签名证书应校验失败，实际 %v", err)
	}
}

func TestFTPImplicitTLS(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true, tlsCfg: testTLSConfig(t), implicit: true})
	c := s.config()
	c.TLS, c.InsecureSkipVerify = "implicit", true
	var dc dialCounter
	tg := openFTPT(t, c, Deps{Dial: dc.dial})
	if err := Probe(context.Background(), tg); err != nil {
		t.Fatalf("隐式 TLS Probe 失败：%v", err)
	}
	mustPut(t, tg, "a/b.bin", randData(t, 300<<10))
	if s.dataTLS.Load() == 0 {
		t.Fatal("隐式 TLS 下数据连接也应加密")
	}
}

func TestFTPWrongPassword(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true})
	c := s.config()
	c.Password = "wrong-password"
	err := Probe(context.Background(), openFTPT(t, c, Deps{}))
	if err == nil || !strings.Contains(err.Error(), "用户名或口令不正确") {
		t.Fatalf("口令错误提示不对：%v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Fatal("错误信息泄露了口令")
	}
}

func TestFTPAbsolutePathAndPool(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true})
	os.MkdirAll(filepath.Join(s.root, "srv", "ftp"), 0o755)
	c := s.config()
	c.Path = "/srv/ftp"
	var dc dialCounter
	tg := openFTPT(t, c, Deps{Dial: dc.dial})
	mustPut(t, tg, "x.bin", []byte("x"))
	if _, err := os.Stat(filepath.Join(s.root, "srv", "ftp", "x.bin")); err != nil {
		t.Fatalf("绝对路径不对：%v", err)
	}
	// 并发操作各用一条连接，空闲连接复用。
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := "c/" + strconv.Itoa(i) + ".bin"
			data := randData(t, 10000+i)
			if err := tg.Put(context.Background(), p, strings.NewReader(string(data)), -1); err != nil {
				errs <- err
				return
			}
			rc, err := tg.Get(context.Background(), p)
			if err != nil {
				errs <- err
				return
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || string(got) != string(data) {
				errs <- fmt.Errorf("并发读写 %s 内容不一致：%v", p, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	before := dc.n.Load()
	for i := 0; i < 5; i++ {
		if _, err := tg.Stat(context.Background(), "x.bin"); err != nil {
			t.Fatal(err)
		}
	}
	if dc.n.Load() != before {
		t.Fatal("串行的查询应复用空闲连接，不应重新拨号")
	}
}

func TestFTPConfigErrors(t *testing.T) {
	bad := []FTPConfig{
		{Host: "", Username: "u"},
		{Host: "h"},
		{Host: "h", Username: "u", Port: 70000},
		{Host: "h", Username: "u", TLS: "ssl"},
		{Host: "h", Username: "u", Path: "../x"},
		{Host: "h/x", Username: "u"},
		{Host: "h", Username: "u\r\nDELE x"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindFTP, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
	b, _ := json.Marshal(FTPConfig{Host: "h", Username: "u", TLS: "implicit"})
	tg, err := Open(KindFTP, b, Deps{})
	if err != nil || tg.(*ftpTarget).addr != "h:990" {
		t.Fatalf("隐式 TLS 默认端口应为 990：%v", err)
	}
}

func TestFTPCanceledMidStream(t *testing.T) {
	s := startFTPServer(t, &ftpTestServer{mlst: true})
	tg := openFTPT(t, s.config(), Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- tg.Put(ctx, "slow.bin", pr, -1) }()
	pw.Write(randData(t, 200<<10))
	cancel() // 数据源还没结束，取消后连接被关掉，再写入的数据发不出去
	more := randData(t, 200<<10)
	go pw.Write(more)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Put 没有返回")
	}
	pw.CloseWithError(errors.New("测试结束"))
	if _, err := tg.Stat(context.Background(), "slow.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("取消的上传不能留下文件：%v", err)
	}
	noTempFiles(t, s.root)
}
