package dumptools

import "golang.org/x/sys/windows"

// windowsBuild 返回 Windows 内部版本号（Windows 11 从 22000 起）。
func windowsBuild() uint32 { return windows.RtlGetVersion().BuildNumber }
