//go:build !windows

package config

func isWindowsService() bool { return false }
