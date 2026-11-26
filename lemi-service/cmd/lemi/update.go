package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/selfupdate"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
	"github.com/kardianos/service"
)

// 本文件名排在 main.go、service.go 之后，init 晚于它们执行（go 命令按文件名顺序初始化），
// 所以能把 serve / run-service 换成支持「升级后重启」的版本。update_test.go 校验了这一点。
func init() {
	commands["update"] = command{Usage: "update [--check]                         检查并升级到最新正式版（校验发布签名）", Run: runUpdate}
	commands["healthcheck"] = command{Usage: "healthcheck [--timeout 5s]               检查本机 Lemi 是否正常（Docker HEALTHCHECK 用）", Run: runHealthcheck}
	setRun("serve", serveRestartable)
	setRun("run-service", runServiceRestartable)
}

func setRun(name string, fn func([]string) error) {
	c := commands[name]
	c.Run = fn
	commands[name] = c
}

// serveRestartable 与 runServe 相同，另外：在线升级请求重启时正常退出，再换成新程序运行。
func serveRestartable(args []string) error {
	_, data, listen, err := commonFlags("serve", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*data, *listen)
	if err != nil {
		return err
	}
	return afterServe(serve(cfg, selfupdate.RestartRequested()))
}

// runServiceRestartable 与 runAsService 相同，另外处理升级后的重启：
// Unix 上服务正常退出后原地 exec 新程序；Windows 服务交给独立的「lemi service restart」进程经服务管理器重启。
func runServiceRestartable(args []string) error {
	if runtime.GOOS == "windows" && !service.Interactive() {
		selfupdate.SetServiceRestart(func(exe string) error {
			return selfupdate.SpawnDetached(exe, "service", "restart")
		})
	}
	return afterServe(runAsService(args))
}

func afterServe(err error) error {
	if err != nil || !selfupdate.RestartPending() {
		return err
	}
	fmt.Fprintln(os.Stderr, "Lemi 已升级，正在以新版本重新启动 …")
	if err := selfupdate.Reexec(); err != nil {
		return fmt.Errorf("新版本已安装，但重新启动失败（%w），请手动启动 Lemi", err)
	}
	return nil
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "只检查，不升级")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	u := selfupdate.New()
	fmt.Printf("当前版本：%s（%s）\n", version.Version, u.Platform())
	cctx, ccancel := context.WithTimeout(ctx, time.Minute)
	rel, err := u.Check(cctx)
	ccancel()
	if err != nil {
		return err
	}
	if rel == nil {
		fmt.Println("还没有发布任何正式版。")
		return nil
	}
	if !u.Newer(rel) {
		fmt.Printf("已经是最新版本（最新正式版 %s）。\n", rel.Version)
		return nil
	}
	fmt.Printf("最新版本：%s  %s\n", rel.Version, rel.URL)
	if *check {
		return nil
	}
	if config.InDocker() {
		return errors.New("Docker 部署不在容器里自升级：拉取新镜像后重建容器（docker compose pull && docker compose up -d）")
	}
	if !selfupdate.Supported(u.GOOS, u.GOARCH) {
		return fmt.Errorf("当前平台（%s）没有官方发布包，请从源码构建", u.Platform())
	}
	if err := u.CanReplace(); err != nil {
		return err
	}
	fmt.Println("下载并校验发布签名 …")
	actx, acancel := context.WithTimeout(ctx, 15*time.Minute)
	res, err := u.Apply(actx, rel)
	acancel()
	if err != nil {
		return err
	}
	fmt.Printf("已升级到 %s：%s\n", res.To, res.Exe)

	// 装成系统服务并在运行时，经服务管理器重启，让新版本生效
	s, err := service.New(&program{}, svcConfig("", ""))
	if err == nil {
		if st, err := s.Status(); err == nil && st == service.StatusRunning {
			fmt.Println("正在重启系统服务 lemi …")
			if err := service.Control(s, "restart"); err != nil {
				return fmt.Errorf("重启系统服务失败：%w（请手动执行 lemi service restart）", err)
			}
			fmt.Println("系统服务已重启。")
			return nil
		}
	}
	fmt.Println("如果 Lemi 正在前台运行，请重新启动它；装成系统服务的用 lemi service restart。")
	return nil
}

// runHealthcheck 请求本机 /healthz：监听地址取自配置（LEMI_LISTEN 等），通配地址换成回环地址；
// 启用 TLS 时只连本机并跳过证书校验（证书是给域名签的，这里只关心服务是否存活）。
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 5*time.Second, "超时")
	data := fs.String("data", "", "数据目录（读取其中的 lemi.toml）")
	listen := fs.String("listen", "", "监听地址（默认取配置）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*data, *listen)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听地址 %q 不正确：%w", cfg.Listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	scheme := "http"
	tr := &http.Transport{Proxy: nil}
	if cfg.TLS.Mode != "off" {
		scheme = "https"
		sni := ""
		if cfg.TLS.Mode == "acme" && len(cfg.TLS.ACMEDomains) > 0 {
			sni = cfg.TLS.ACMEDomains[0]
		}
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, ServerName: sni} //nolint:gosec // 只连本机回环或配置的监听地址
	}
	client := &http.Client{Transport: tr, Timeout: *timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	url := scheme + "://" + net.JoinHostPort(host, port) + "/healthz"
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("不健康：%w", err)
	}
	defer resp.Body.Close()
	var body struct {
		OK bool `json:"ok"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &body) != nil || !body.OK {
		return fmt.Errorf("不健康：HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	fmt.Println("ok")
	return nil
}
