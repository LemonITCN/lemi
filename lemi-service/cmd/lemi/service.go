package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/kardianos/service"
)

func init() {
	commands["service"] = command{Usage: "service <install|uninstall|start|stop|restart|status> [--data 目录] [--listen 地址]   系统服务", Run: runService}
	commands["run-service"] = command{Usage: "run-service [--data 目录] [--listen 地址]   （由服务管理器调用）", Run: runAsService}
}

type program struct {
	cfg  *config.Config
	stop chan struct{}
	done chan error
}

func (p *program) Start(s service.Service) error {
	p.stop = make(chan struct{})
	p.done = make(chan error, 1)
	go func() { p.done <- serve(p.cfg, p.stop) }()
	return nil
}

func (p *program) Stop(s service.Service) error {
	close(p.stop)
	return <-p.done
}

func svcConfig(data, listen string) *service.Config {
	args := []string{"run-service"}
	if data != "" {
		args = append(args, "--data", data)
	}
	if listen != "" {
		args = append(args, "--listen", listen)
	}
	return &service.Config{
		Name:        "lemi",
		DisplayName: "Lemi",
		Description: "Lemi 数据库管理、备份与 AI 智能体",
		Arguments:   args,
		Option:      service.KeyValue{"Restart": "on-failure", "LimitNOFILE": 65536},
	}
}

func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：lemi service install|uninstall|start|stop|restart|status [--data 目录] [--listen 地址]")
	}
	action := args[0]
	_, data, listen, err := commonFlags("service", args[1:])
	if err != nil {
		return err
	}
	if *data != "" {
		if abs, err := filepath.Abs(*data); err == nil {
			*data = abs
		}
	}
	s, err := service.New(&program{}, svcConfig(*data, *listen))
	if err != nil {
		return err
	}
	if action == "status" {
		st, err := s.Status()
		if err != nil {
			return err
		}
		switch st {
		case service.StatusRunning:
			fmt.Println("运行中")
		case service.StatusStopped:
			fmt.Println("已停止")
		default:
			fmt.Println("未知（可能没有安装）")
		}
		return nil
	}
	if err := service.Control(s, action); err != nil {
		return fmt.Errorf("%s 失败：%w（安装与卸载通常需要管理员 / root 权限）", action, err)
	}
	switch action {
	case "install":
		cfg, _ := config.Load(*data, *listen)
		dir := ""
		if cfg != nil {
			dir = cfg.DataDir
		}
		fmt.Printf("已安装系统服务 lemi。数据目录：%s\n用 lemi service start 启动；首次启动的初始化令牌在数据目录的 setup-token 文件里。\n", dir)
	default:
		fmt.Println("完成。")
	}
	return nil
}

func runAsService(args []string) error {
	_, data, listen, err := commonFlags("run-service", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*data, *listen)
	if err != nil {
		return err
	}
	p := &program{cfg: cfg}
	s, err := service.New(p, svcConfig(*data, *listen))
	if err != nil {
		return err
	}
	if !service.Interactive() {
		return s.Run()
	}
	fmt.Fprintln(os.Stderr, "提示：run-service 通常由服务管理器调用，前台运行请用 lemi serve。")
	return serve(cfg, nil)
}
