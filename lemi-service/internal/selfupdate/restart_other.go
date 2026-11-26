//go:build !unix && !windows

package selfupdate

import "errors"

var errUnsupported = errors.New("当前系统不支持自动重启，请手动重启 Lemi")

func requestShutdown() error { return errUnsupported }

func reexec(string) error { return errUnsupported }

// SpawnDetached 当前系统不支持。
func SpawnDetached(string, ...string) error { return errUnsupported }
