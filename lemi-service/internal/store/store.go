// Package store 是 Lemi 自己的元数据库：数据目录里的一个 SQLite 文件（WAL 模式）。
//
// 迁移脚本放在 migrations/ 下，按文件名排序执行，已执行的记在 schema_migrations。
// 文件名编号段：001–099 主干，100–199 连接与驱动，200–299 备份/健康检查/通知，300–399 智能体/MCP/报告/定时任务。
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store 是元数据库。
type Store struct {
	W    *sqlx.DB // 写连接（只有一条，写操作串行）
	R    *sqlx.DB // 读连接池
	path string
	wmu  sync.Mutex
}

// ErrNotFound 记录不存在。
var ErrNotFound = errors.New("记录不存在")

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(15000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "temp_store(memory)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
		q.Add("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Open 打开（不存在则创建）元数据库并执行迁移。
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w, err := sqlx.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("打开元数据库失败：%w", err)
	}
	_ = os.Chmod(path, 0o600)
	r, err := sqlx.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	s := &Store{W: w, R: r, path: path}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭。
func (s *Store) Close() error {
	var e1, e2 error
	if s.R != nil {
		e1 = s.R.Close()
	}
	if s.W != nil {
		e2 = s.W.Close()
	}
	return errors.Join(e1, e2)
}

// Path 返回数据库文件路径。
func (s *Store) Path() string { return s.path }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.W.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	done := map[string]bool{}
	var ids []string
	if err := s.W.SelectContext(ctx, &ids, `SELECT id FROM schema_migrations`); err != nil {
		return err
	}
	for _, id := range ids {
		done[id] = true
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if done[n] {
			continue
		}
		b, err := migrationFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		tx, err := s.W.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行迁移 %s 失败：%w", n, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, n, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx 在写事务里执行 fn。
func (s *Store) Tx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.W.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Exec 执行一条写语句。
func (s *Store) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.W.ExecContext(ctx, q, args...)
}

// NamedExec 执行一条带命名参数的写语句。
func (s *Store) NamedExec(ctx context.Context, q string, arg any) (sql.Result, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.W.NamedExecContext(ctx, q, arg)
}

// Get 读一行；不存在返回 ErrNotFound。
func (s *Store) Get(ctx context.Context, dst any, q string, args ...any) error {
	err := s.R.GetContext(ctx, dst, q, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Select 读多行。
func (s *Store) Select(ctx context.Context, dst any, q string, args ...any) error {
	return s.R.SelectContext(ctx, dst, q, args...)
}

// GetW 在写连接上读（需要读到刚写入的数据时用）。
func (s *Store) GetW(ctx context.Context, dst any, q string, args ...any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	err := s.W.GetContext(ctx, dst, q, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Insert 按结构体的 db 标签插入一行。
func Insert(ctx context.Context, e sqlx.ExtContext, table string, v any) error {
	cols, err := columnsOf(v)
	if err != nil {
		return err
	}
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (:" + strings.Join(cols, ", :") + ")"
	_, err = sqlx.NamedExecContext(ctx, e, q, v)
	return err
}

// Upsert 按结构体插入或按主键 id 覆盖。
func Upsert(ctx context.Context, e sqlx.ExtContext, table string, v any) error {
	cols, err := columnsOf(v)
	if err != nil {
		return err
	}
	var sets []string
	for _, c := range cols {
		if c != "id" {
			sets = append(sets, c+" = excluded."+c)
		}
	}
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (:" + strings.Join(cols, ", :") + ") ON CONFLICT(id) DO UPDATE SET " + strings.Join(sets, ", ")
	_, err = sqlx.NamedExecContext(ctx, e, q, v)
	return err
}

// Put 是 Upsert 的便捷写法（自己加锁）。
func (s *Store) Put(ctx context.Context, table string, v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return Upsert(ctx, s.W, table, v)
}

// Add 是 Insert 的便捷写法（自己加锁）。
func (s *Store) Add(ctx context.Context, table string, v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return Insert(ctx, s.W, table, v)
}

// Now 返回当前 Unix 毫秒。
func Now() int64 { return time.Now().UnixMilli() }
