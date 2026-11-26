//go:build unix

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// requestShutdown 给自己发 SIGTERM：前台运行时由 serve 的信号处理接住，服务方式运行时由服务库接住，
// 两条路径都会正常关闭服务后返回到命令层。
func requestShutdown() error {
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}

func reexec(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}

// SpawnDetached 启动一个不随本进程退出的子进程（Unix 上目前没有用到，保留与 Windows 一致的接口）。
func SpawnDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
