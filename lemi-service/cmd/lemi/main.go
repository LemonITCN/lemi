// lemi：Web 数据库管理、备份与 AI 智能体。一个可执行文件即整个系统。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/LemonITCN/lemi/lemi-service/internal/app"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
)

// command 是一个子命令。其他文件可在 init 里往 commands 添加。
type command struct {
	Usage string
	Run   func(args []string) error
}

var commands = map[string]command{}

func init() {
	commands["serve"] = command{Usage: "serve [--data 目录] [--listen 地址]     前台运行（默认命令）", Run: runServe}
	commands["version"] = command{Usage: "version                                  显示版本", Run: func([]string) error {
		fmt.Printf("lemi %s", version.Version)
		if version.Commit != "" {
			fmt.Printf(" (%s %s)", version.Commit, version.Date)
		}
		fmt.Println()
		return nil
	}}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Lemi %s\n\n用法：lemi <命令> [选项]\n\n", version.Version)
	var names []string
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  lemi %s\n", commands[n].Usage)
	}
	fmt.Fprintf(os.Stderr, "\n所有配置项都可以用环境变量设置（LEMI_DATA_DIR、LEMI_LISTEN、LEMI_TLS_MODE …），见 docs/spec.md §3.2。\n")
}

func main() {
	args := os.Args[1:]
	name := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		name, args = args[0], args[1:]
	}
	if name == "help" || name == "-h" || name == "--help" {
		usage()
		return
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n", name)
		usage()
		os.Exit(2)
	}
	if err := cmd.Run(args); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

// commonFlags 解析 --data 与 --listen。
func commonFlags(name string, args []string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	data := fs.String("data", "", "数据目录（默认按平台，见文档）")
	listen := fs.String("listen", "", "监听地址，例如 0.0.0.0:7700")
	err := fs.Parse(args)
	return fs, data, listen, err
}

func runServe(args []string) error {
	_, data, listen, err := commonFlags("serve", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*data, *listen)
	if err != nil {
		return err
	}
	return serve(cfg, nil)
}

// serve 运行服务直到收到退出信号或 stop 关闭。
func serve(cfg *config.Config, stop <-chan struct{}) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if stop != nil {
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Run(ctx)
}
