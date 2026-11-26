package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/selfupdate"
)

// TestMain：设置了 LEMI_TEST_RESTART_DIR 时，测试程序充当被升级的 lemi（见 TestRestartAfterUpdate）。
func TestMain(m *testing.M) {
	if dir := os.Getenv("LEMI_TEST_RESTART_DIR"); dir != "" {
		os.Exit(restartChild(dir, os.Getenv("LEMI_TEST_RESTART_LISTEN")))
	}
	os.Exit(m.Run())
}

// restartChild 第一代：正常启动，健康后请求「升级后重启」；第二代（由第一代重新启动）：确认健康后正常退出。
// 每一代把自己的进程号写进 genN 文件，正常退出时写 genN-exit。
func restartChild(dir, listen string) int {
	gen := 1
	if _, err := os.Stat(filepath.Join(dir, "gen1")); err == nil {
		gen = 2
	}
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("gen%d", gen)), []byte(strconv.Itoa(os.Getpid())), 0o600)
	args := []string{"--data", filepath.Join(dir, "data"), "--listen", listen}
	waitHealthy := func() bool {
		for i := 0; i < 200; i++ {
			if runHealthcheck(append(args, "--timeout", "1s")) == nil {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	var err error
	if gen == 1 {
		go func() {
			if waitHealthy() {
				exe, _ := os.Executable()
				_ = selfupdate.RequestRestart(exe)
			}
		}()
		err = serveRestartable(args)
	} else {
		cfg, cerr := config.Load(args[1], args[3])
		if cerr != nil {
			return 3
		}
		stop := make(chan struct{})
		go func() {
			waitHealthy()
			close(stop)
		}()
		err = serve(cfg, stop)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "子进程出错：", err)
		return 1
	}
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("gen%d-exit", gen)), []byte("ok"), 0o600)
	return 0
}

// 升级后重启的完整流程：请求重启 → 正常退出（释放数据目录锁）→ 新程序以同样的参数启动并健康。
// Unix 上是 execve 原地替换（进程号不变）；Windows 前台运行是启动新进程后退出。
func TestRestartAfterUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过")
	}
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "LEMI_TEST_RESTART_DIR="+dir, "LEMI_TEST_RESTART_LISTEN="+listen, "LEMI_UPDATE_CHECK=false")
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("子进程失败：%v\n%s", err, out.String())
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("子进程超时\n%s", out.String())
	}
	// Windows 上第一代退出时第二代可能还在运行，等它写出退出标记
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "gen2-exit")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("新程序没有启动或没有正常退出\n%s", out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "gen1-exit")); err == nil && !isWindows() {
		t.Fatal("Unix 上第一代应当被 exec 替换，不应走到正常退出")
	}
	p1, _ := os.ReadFile(filepath.Join(dir, "gen1"))
	p2, _ := os.ReadFile(filepath.Join(dir, "gen2"))
	if !isWindows() && string(p1) != string(p2) {
		t.Fatalf("Unix 上应当原地 exec（进程号不变）：%s → %s", p1, p2)
	}
}

func isWindows() bool { return os.PathSeparator == '\\' }

// serve 与 run-service 必须是支持升级后重启的版本（update.go 的 init 要晚于 main.go、service.go 执行）。
func TestRestartableCommandsInstalled(t *testing.T) {
	for name, want := range map[string]func([]string) error{"serve": serveRestartable, "run-service": runServiceRestartable} {
		c, ok := commands[name]
		if !ok {
			t.Fatalf("缺少命令 %s", name)
		}
		if reflect.ValueOf(c.Run).Pointer() != reflect.ValueOf(want).Pointer() {
			t.Fatalf("命令 %s 没有换成支持升级后重启的版本（检查文件名顺序与 init）", name)
		}
		if c.Usage == "" {
			t.Fatalf("命令 %s 的说明丢了", name)
		}
	}
	for _, name := range []string{"update", "healthcheck"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("缺少命令 %s", name)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	healthy := true
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || !healthy {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("LEMI_TLS_MODE", "off")
	args := []string{"--data", t.TempDir(), "--listen", "0.0.0.0:" + port, "--timeout", "3s"}
	if err := runHealthcheck(args); err != nil {
		t.Fatalf("服务正常时应当通过：%v", err)
	}
	healthy = false
	if err := runHealthcheck(args); err == nil {
		t.Fatal("服务不正常时应当失败")
	}
}
