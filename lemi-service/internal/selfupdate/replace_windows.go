package selfupdate

import (
	"fmt"
	"os"
	"time"
)

// replaceExecutable 在 Windows 上替换可执行文件：运行中的 exe 不能删除或覆盖，但可以改名，
// 所以先把它改名为 <exe>.old，再把新文件放到原位置；失败时改回去。
// .old 在新进程启动后由 CleanupOld 删除。
func replaceExecutable(exe, newFile string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		// 上一次的 .old 还被占用（旧进程没退干净），换个名字
		old = fmt.Sprintf("%s.%d.old", exe, time.Now().UnixNano())
	}
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(newFile, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("%w（恢复原文件也失败：%v，原文件在 %s）", err, rerr, old)
		}
		return err
	}
	return nil
}
