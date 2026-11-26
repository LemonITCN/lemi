//go:build !windows

package selfupdate

import "os"

// replaceExecutable 用同目录下的新文件原子替换可执行文件。
// 运行中的进程继续使用旧文件（旧 inode），重启后才是新版本；绝不能原地覆盖写，
// 否则 macOS 会因代码签名缓存与文件内容不符直接杀掉进程。
func replaceExecutable(exe, newFile string) error {
	return os.Rename(newFile, exe)
}
