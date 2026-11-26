// Package sqlite 是 SQLite 驱动。文件只能来自管理员配置的白名单目录或 Lemi 数据目录里的上传文件（docs/spec.md §7.4）。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	_ "modernc.org/sqlite"
)

func init() { driver.Register(&Driver{}) }

// Driver 是 SQLite 驱动。
type Driver struct{}

// Info 驱动描述。
func (d *Driver) Info() driver.Info {
	return driver.Info{
		ID: "sqlite", Name: "SQLite", Category: "relational", QueryLanguage: "sql:sqlite", FileBased: true,
		Capabilities: []string{driver.CapNavigate, driver.CapIntrospect, driver.CapQuery, driver.CapEdit, driver.CapDDL,
			driver.CapDump, driver.CapBackup, driver.CapMonitor, driver.CapReadOnly},
		Form: []driver.FormField{
			{Key: "file", Label: "数据库文件", LabelEN: "Database file", Type: "sqlite_file", Group: "basic", Required: true,
				Help: "上传的文件，或服务器上白名单目录里的文件（白名单目录由实例管理员用 LEMI_SQLITE_DIRS 指定）"},
			{Key: "readonly", Label: "以只读方式打开", LabelEN: "Open read-only", Type: "bool", Group: "basic"},
		},
	}
}

// ResolvePath 把表单里的文件引用解析成真实路径：upload:<id> 指 Lemi 数据目录里上传的文件；
// 其他值是服务器上的绝对路径，必须（跟随符号链接后）位于管理员配置的白名单目录之内。拒绝设备文件等非普通文件。
func ResolvePath(env driver.SQLiteEnv, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "\x00") {
		return "", errors.New("请选择数据库文件")
	}
	var full string
	var bases []string
	if strings.HasPrefix(ref, "upload:") {
		id := strings.TrimPrefix(ref, "upload:")
		if id == "" {
			return "", errors.New("上传文件标识不正确")
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return "", errors.New("上传文件标识不正确")
			}
		}
		if env.UploadDir == "" {
			return "", errors.New("没有上传目录")
		}
		full, bases = filepath.Join(env.UploadDir, id+".db"), []string{env.UploadDir}
	} else {
		if !filepath.IsAbs(ref) {
			return "", errors.New("请填写服务器上的绝对路径")
		}
		if len(env.AllowedDirs) == 0 {
			return "", errors.New("实例管理员没有配置允许打开的 SQLite 目录（LEMI_SQLITE_DIRS），只能使用上传的文件")
		}
		full, bases = filepath.Clean(ref), env.AllowedDirs
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("文件不存在")
		}
		return "", fmt.Errorf("无法访问文件：%w", err)
	}
	ok := false
	for _, b := range bases {
		if br, err := filepath.EvalSymlinks(b); err == nil && within(br, real) {
			ok = true
			break
		}
	}
	if !ok {
		return "", errors.New("文件不在允许的目录内")
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("不是普通文件")
	}
	return real, nil
}

func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// CheckHeader 校验文件头是 SQLite 3。
func CheckHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	if n == 0 {
		return nil // 空文件：SQLite 会当成新库
	}
	if n < 16 || string(buf) != "SQLite format 3\x00" {
		return errors.New("不是 SQLite 3 数据库文件")
	}
	return nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("mode", "rw")
		q.Add("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Conn 是打开的 SQLite 数据源。
type Conn struct {
	rw, ro *sql.DB
	path   string
	ver    string
}

// Open 打开文件。
func (d *Driver) Open(ctx context.Context, cfg *driver.ConnConfig) (driver.Conn, error) {
	path, err := ResolvePath(cfg.SQLite, cfg.Str("file"))
	if err != nil {
		return nil, err
	}
	if err := CheckHeader(path); err != nil {
		return nil, err
	}
	readOnly := cfg.ReadOnly || cfg.Bool("readonly")
	ro, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	ro.SetMaxOpenConns(4)
	ro.SetConnMaxIdleTime(5 * time.Minute)
	c := &Conn{ro: ro, rw: ro, path: path}
	if !readOnly {
		rw, err := sql.Open("sqlite", dsn(path, false))
		if err != nil {
			ro.Close()
			return nil, err
		}
		rw.SetMaxOpenConns(4)
		rw.SetConnMaxIdleTime(5 * time.Minute)
		c.rw = rw
	}
	if err := c.Ping(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Path 文件真实路径。
func (c *Conn) Path() string { return c.path }

// Ping 检查文件可读。
func (c *Conn) Ping(ctx context.Context) error {
	var n int
	return c.ro.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&n)
}

// Server 版本信息。
func (c *Conn) Server(ctx context.Context) (driver.ServerInfo, error) {
	if c.ver == "" {
		_ = c.ro.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&c.ver)
	}
	info := driver.ServerInfo{Product: "SQLite", Version: c.ver, Extra: map[string]any{}}
	if fi, err := os.Stat(c.path); err == nil {
		info.Extra["size_bytes"] = fi.Size()
	}
	return info, nil
}

// Close 关闭。
func (c *Conn) Close() error {
	var e1, e2 error
	if c.rw != nil && c.rw != c.ro {
		e1 = c.rw.Close()
	}
	if c.ro != nil {
		e2 = c.ro.Close()
	}
	return errors.Join(e1, e2)
}

// Dialect 方言。
func (c *Conn) Dialect() string { return "sqlite" }

// DB 读写池（只读打开时是只读池）。
func (c *Conn) DB() *sql.DB { return c.rw }

// ReadDB 只读池。
func (c *Conn) ReadDB() *sql.DB { return c.ro }

// QuoteIdent 双引号引用。
func (c *Conn) QuoteIdent(s string) string { return QuoteIdent(s) }

// QuoteIdent 双引号引用标识符。
func QuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// QuoteString 单引号字符串。
func (c *Conn) QuoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ColumnKind 按 SQLite 类型亲和性归类。
func (c *Conn) ColumnKind(t string) string { return ColumnKind(t) }

// ColumnKind 按声明类型归类。
func ColumnKind(t string) string {
	u := strings.ToUpper(t)
	switch {
	case u == "":
		return "other"
	case strings.Contains(u, "INT"):
		return "int"
	case strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB"):
		return "string"
	case strings.Contains(u, "TEXT"):
		return "text"
	case strings.Contains(u, "BLOB"):
		return "binary"
	case strings.Contains(u, "REAL") || strings.Contains(u, "FLOA") || strings.Contains(u, "DOUB"):
		return "float"
	case strings.Contains(u, "DECIMAL") || strings.Contains(u, "NUMERIC"):
		return "decimal"
	case strings.Contains(u, "BOOL"):
		return "bool"
	case u == "DATE":
		return "date"
	case strings.Contains(u, "DATETIME") || strings.Contains(u, "TIMESTAMP"):
		return "datetime"
	case strings.Contains(u, "JSON"):
		return "json"
	}
	return "other"
}

// ExplainSQL 执行计划。
func (c *Conn) ExplainSQL(stmt string) (string, string) { return "EXPLAIN QUERY PLAN " + stmt, "table" }

// Hooks 会话钩子。
func (c *Conn) Hooks() driver.SessionHooks { return hooks{} }

type hooks struct{}

func (hooks) Init(ctx context.Context, conn *sql.Conn, o driver.SessionOptions) error { return nil }
func (hooks) SessionID(ctx context.Context, conn *sql.Conn) string                    { return "" }
func (hooks) Cancel(ctx context.Context, db *sql.DB, id string) error                 { return nil }
func (hooks) Use(ctx context.Context, conn *sql.Conn, db string) error {
	if db != "" && db != "main" {
		return errors.New("SQLite 连接只有 main 库")
	}
	return nil
}
func (hooks) Current(ctx context.Context, conn *sql.Conn) string { return "main" }
func (hooks) WrapReadOnly() (string, string)                     { return "", "" }
func (hooks) NullSafeEq(col string) string                       { return col + " IS ?" }
