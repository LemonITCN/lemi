package selfupdate

import (
	"errors"
	"sync"
	"sync/atomic"
)

// 升级后的重启。可执行文件已经替换好，接下来要让新程序接管，同时保证旧进程先正常退出
// （释放数据目录锁、把审计与元数据库写完）：
//
//   - Linux / macOS（前台运行或 systemd / launchd 服务）：给自己发 SIGTERM，走与 Ctrl+C、systemctl stop 相同的
//     正常退出流程；命令层在 serve / run-service 返回后调用 Reexec，用 execve 原地换成新程序（进程号不变，
//     服务管理器看到的是同一个进程，不会触发它的重启间隔）。
//   - Windows 前台运行：关闭 RestartRequested() 通道，serve 收到后正常退出；命令层再以同样的参数启动新进程。
//   - Windows 服务：命令层用 SetServiceRestart 注册的函数拉起一个独立的「lemi service restart」进程，
//     由它经服务控制管理器停止本服务（正常退出）再启动（运行的就是新程序）。

var (
	restartCh   = make(chan struct{})
	restartOnce sync.Once
	pending     atomic.Bool
	restartExe  atomic.Value // string
	svcMu       sync.Mutex
	svcRestart  func(exe string) error
)

// RestartRequested 在 Windows 前台运行时请求重启会关闭这个通道（其他情况不会关闭）；serve 把它当作停止信号。
func RestartRequested() <-chan struct{} { return restartCh }

// RestartPending 是否已请求「退出后以新程序重新启动」（命令层据此决定是否调用 Reexec）。
func RestartPending() bool { return pending.Load() }

// SetServiceRestart 登记以服务方式运行时的重启方法（Windows 服务用）。
func SetServiceRestart(fn func(exe string) error) {
	svcMu.Lock()
	svcRestart = fn
	svcMu.Unlock()
}

// RequestRestart 请求以新程序 exe 重启。调用后进程会开始正常退出。
func RequestRestart(exe string) error {
	if exe == "" {
		return errors.New("缺少可执行文件路径")
	}
	restartExe.Store(exe)
	svcMu.Lock()
	fn := svcRestart
	svcMu.Unlock()
	if fn != nil {
		return fn(exe)
	}
	pending.Store(true)
	return requestShutdown()
}

func closeRestartCh() { restartOnce.Do(func() { close(restartCh) }) }

// Reexec 在服务正常退出后启动新程序：Unix 上 execve 原地替换（不返回），Windows 上以相同参数启动新进程后返回。
func Reexec() error {
	exe, _ := restartExe.Load().(string)
	if exe == "" {
		return errors.New("没有请求过重启")
	}
	return reexec(exe)
}
