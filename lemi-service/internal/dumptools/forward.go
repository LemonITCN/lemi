package dumptools

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// Forwarder 在 127.0.0.1 的随机端口上监听，把每个本机连接经 Dial 转发到目标地址。
// mysqldump/mysql 只能直连，经 SSH 隧道或代理的连接靠它；只为一次备份/还原服务，结束即关闭。
type Forwarder struct {
	ln     net.Listener
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	target string
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
	closed bool
}

// Forward 开始转发。
func Forward(dial func(ctx context.Context, network, addr string) (net.Conn, error), target string) (*Forwarder, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Forwarder{ln: ln, dial: dial, target: target, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	f.wg.Add(1)
	go f.loop()
	return f, nil
}

// Addr 本机监听地址（127.0.0.1:端口）。
func (f *Forwarder) Addr() string { return f.ln.Addr().String() }

func (f *Forwarder) track(c net.Conn, add bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if add {
		if f.closed {
			return false
		}
		f.conns[c] = struct{}{}
	} else {
		delete(f.conns, c)
	}
	return true
}

func (f *Forwarder) loop() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		// 只接受本机连接（监听在回环地址上，这里再确认一次）
		if ta, ok := c.RemoteAddr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() {
			c.Close()
			continue
		}
		if !f.track(c, true) {
			c.Close()
			return
		}
		f.wg.Add(1)
		go f.serve(c)
	}
}

func (f *Forwarder) serve(local net.Conn) {
	defer f.wg.Done()
	defer f.track(local, false)
	defer local.Close()
	dctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	remote, err := f.dial(dctx, "tcp", f.target)
	cancel()
	if err != nil {
		return
	}
	if !f.track(remote, true) {
		remote.Close()
		return
	}
	defer f.track(remote, false)
	defer remote.Close()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(remote, local)
	go pipe(local, remote)
	<-done
	<-done
}

// Close 停止监听并断开全部转发中的连接。
func (f *Forwarder) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	f.cancel()
	f.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	f.wg.Wait()
}
