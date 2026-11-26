package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot 用 VACUUM INTO 把元数据库一致地复制到 dir/lemi-<UTC 时间>.db，只保留最近 keep 份。
// 复制运行中的数据目录时 lemi.db 可能处于写入中途，快照一定是完整的。
func (s *Store) Snapshot(ctx context.Context, dir string, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "lemi-" + time.Now().UTC().Format("20060102-150405") + ".db"
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	s.wmu.Lock()
	_, err := s.W.ExecContext(ctx, "VACUUM INTO ?", tmp)
	s.wmu.Unlock()
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("生成元数据库快照失败：%w", err)
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lemi-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return final, nil
}

// RunSnapshots 每小时生成一次快照，直到 ctx 结束。启动后先等 1 分钟，避开启动期的写入高峰。
func (s *Store) RunSnapshots(ctx context.Context, dir string, log *slog.Logger) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Snapshot(ctx, dir, 3); err != nil {
			log.Warn("元数据库快照失败", "err", err)
		}
		t.Reset(time.Hour)
	}
}

// LatestSnapshot 返回最新快照路径（没有则为空）。
func LatestSnapshot(dir string) string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lemi-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1])
}
