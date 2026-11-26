package selfupdate

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// requestShutdown 在 Windows 前台运行时关闭重启通道，serve 收到后正常退出。
func requestShutdown() error {
	closeRestartCh()
	return nil
}

// reexec 以相同参数启动新进程（共用当前控制台），本进程随后退出。
func reexec(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// SpawnDetached 启动一个不随本进程退出的子进程（不挂控制台、独立进程组，能脱离作业对象时脱离）。
func SpawnDetached(exe string, args ...string) error {
	start := func(flags uint32) error {
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	if err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB); err == nil {
		return nil
	}
	return start(base)
}
