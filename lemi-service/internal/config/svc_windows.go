//go:build windows

package config

import "golang.org/x/sys/windows/svc"

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}
