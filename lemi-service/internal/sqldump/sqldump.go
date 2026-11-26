// Package sqldump 是 Lemi 自研的 SQL 导出器与导入器（MySQL/MariaDB 与 SQLite）。
//
// 导出文件与 Navicat Premium「转储 SQL 文件」格式一致（注释头、`-- ----------------------------` 分节注释、
// 每张表的数据用 BEGIN;/COMMIT; 包住、INSERT 带列名列表、例程与触发器用 `delimiter ;;` … `;;` … `delimiter ;`），
// 可以在 Navicat「运行 SQL 文件」和官方 mysql 客户端导入。导入器能导入 Lemi、Navicat、mysqldump 三种文件。
//
// 为了可靠，以下几处有意与 Navicat 不同：
//
//   - 注释头第一行写 `Lemi Dump SQL`，其余字段照 Navicat。
//   - MySQL：`SET NAMES utf8mb4;` 之后加 `SET FOREIGN_KEY_CHECKS = 0;`、`SET time_zone = '+00:00';`
//     （导出会话也设成 +00:00，TIMESTAMP 在不同时区的服务器之间往返不偏移）、
//     `SET SQL_MODE = 'NO_AUTO_VALUE_ON_ZERO';`（自增列里值为 0 的行不被改号；同时清掉 NO_BACKSLASH_ESCAPES，
//     保证文件里的反斜杠转义被正确解析）。
//   - 文件末尾恢复 FOREIGN_KEY_CHECKS（SQLite 恢复 foreign_keys）后写结束标记 `-- Lemi dump completed: <UTC 时间> UTC`。
//     导入器看到 Lemi 注释头却没看到结束标记时返回错误（文件被截断）。mysqldump 文件同理检查 `-- Dump completed`。
//   - 建表语句用服务器 `SHOW CREATE TABLE` 原文（Navicat 会重排），分区、检查约束、函数索引、不可见列、生成列都不丢。
//   - 一致性：在 REPEATABLE READ 的 `START TRANSACTION WITH CONSISTENT SNAPSHOT` 里读取全部数据；
//     MyISAM 等非事务表写进 Manifest.Warnings。
//   - 值：FLOAT 列用 `col * 1e0` 读出精确值（文本协议只给 6 位有效数字），写出能精确还原成同一个 float32 的最短十进制；
//     DOUBLE 写最短往返表示；-0 写成 `-0e0`；二进制一律 `0x..`（空值 `”`）；BIT 写 `b'..'`；
//     GEOMETRY 写 `ST_GeomFromWKB(0x<ST_AsWKB>, srid)`（MySQL 8 地理坐标系按坐标系轴序往返，逐字节一致）；
//     生成列不导数据，不可见列显式写进列名列表。
//   - 顺序：表结构与数据（按表名）→ 函数 → 存储过程 → 视图（按依赖拓扑排序）→ 事件 → 触发器。
//     Navicat 把视图放在函数前面，但视图里调用了存储函数时 MySQL 建视图会报「FUNCTION does not exist」，
//     所以函数与存储过程提前；触发器放最后，避免导数据时被触发。
//   - 例程、触发器、事件按它们自己的 sql_mode 重建（与当前不同时先写 `SET SQL_MODE = '...';`，之后恢复），
//     事件按它自己的 time_zone 重建，行为与原库一致。默认去掉视图、例程、触发器、事件的 DEFINER，换服务器还原不需要 SUPER 权限。
//   - 单张表的数据每约 64 MiB 插一次 `COMMIT;`/`BEGIN;`，大表导入不会变成一个巨大事务。
//   - SQLite：没有 INTEGER PRIMARY KEY 的 rowid 表在 INSERT 里带上 rowid（Navicat 不带，还原后 rowid 会重新编号）；
//     自增值写 `UPDATE "sqlite_sequence" ...` 并在没有该行时补一行；含 \0、\r 或非法 UTF-8 的 TEXT 写成 CAST(X'..' AS TEXT)。
//
// 已知限制（服务器自身或格式决定，导出前后的 TableChecksum 能发现）：
//
//   - MySQL 5.7 的 JSON 文本表示有损：double -0.0 输出成 -0，读回来是整数 0（mysqldump 同样）。8.0+ 与 MariaDB 没有这个问题。
//   - MariaDB 系统版本表只导当前行（历史行无法用普通 INSERT 写回）；序列只恢复参数与下一个值（缓存号段会跳过，与 mariadb-dump 一致）。
//   - 例程的 character_set_client / collation_connection 按导入会话（utf8mb4）重建，正文文字不变。
//   - 例程的 sql_mode 去掉 NO_AUTO_CREATE_USER（8.0 起不存在，保留会导致 5.7 的备份还原到 8.0 时 SET 报错）。
//   - 导入时库名映射只作用于 CREATE / DROP / ALTER DATABASE 与 USE，不改写语句里写死的 `库`.`表` 限定名。
//
// 导出与导入都会改动传入连接的会话状态（字符集、时区、sql_mode、当前库、事务），结束时尽量恢复；
// 调用方应当给它们一条专用连接，用完关闭。连接不能开 allowAllFiles / LOCAL INFILE。
package sqldump

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Dialect 是数据库方言。
type Dialect string

const (
	MySQL  Dialect = "mysql"
	SQLite Dialect = "sqlite"
)

// ToolName 写进 Manifest.Tool。
const ToolName = "lemi-sqldump/1"

const (
	headerTitle     = "Lemi Dump SQL"
	endMarkerPrefix = "-- Lemi dump completed: "
	hashPrefix      = "v1:"
	defaultMaxStmt  = 1 << 20
	txnChunkBytes   = 64 << 20
)

// ExportOptions 是导出选项。
type ExportOptions struct {
	Dialect Dialect
	// Databases MySQL：一个或多个库；多个库（或 CreateDatabase=true）时每个库一节，节首写 CREATE DATABASE IF NOT EXISTS + USE。SQLite 忽略。
	Databases      []string
	CreateDatabase bool
	// Tables 只导部分表或视图（只在单库时有效），空 = 全部。指定时不导例程与事件，只导这些表上的触发器。
	Tables         []string
	NoStructure    bool
	NoData         bool
	NoDrop         bool // 默认写 DROP ... IF EXISTS
	ExtendedInsert bool // 合并插入（Navicat「扩展插入」）；false = 每行一条 INSERT（Navicat 默认）
	// MaxStatementBytes 合并插入单条上限，默认 1 MiB。
	MaxStatementBytes int
	// KeepDefiner 默认去掉视图、例程、触发器、事件的 DEFINER。
	KeepDefiner               bool
	ConnectionName, HostLabel string // 写进注释头
	Progress                  func(Progress)
	Now                       func() time.Time
}

// Progress 是导出进度。Rows、Bytes 是累计值。
type Progress struct {
	Database, Table         string
	TablesDone, TablesTotal int
	Rows                    int64
	Bytes                   int64
}

// Manifest 是导出清单（写进备份的 .manifest.json）。时间是 Unix 毫秒。
type Manifest struct {
	Tool, Dialect, ServerVersion string
	StartedAt, FinishedAt        int64
	Databases                    []DBManifest
	// Warnings 例如非事务表不在一致性快照内。
	Warnings []string `json:",omitempty"`
	// Bytes 导出文件总字节数。
	Bytes int64
}

// DBManifest 是一个库的清单。
type DBManifest struct {
	Name                              string
	Tables                            []TableManifest
	Views, Routines, Triggers, Events int
}

// TableManifest 是一张表的行数与内容哈希（与 TableChecksum 算法相同）。只导结构时 Hash 为空。
type TableManifest struct {
	Name string
	Rows int64
	Hash string
}

// ImportOptions 是导入选项。
type ImportOptions struct {
	Dialect Dialect
	// TargetDatabase 文件里没有 USE 时导入到这个库（MySQL）。
	TargetDatabase string
	// DatabaseMap 文件里的库名 → 目标库名（作用于 CREATE / DROP / ALTER DATABASE 与 USE）。
	DatabaseMap map[string]string
	// ContinueOnError 默认出错即停。连接断开、取消、死锁等致命错误无论如何都停。
	ContinueOnError bool
	// CommitEvery 默认 1000 条语句或 16 MiB 提交一次（文件自带 BEGIN/COMMIT 时尊重文件）。
	CommitEvery int
	// TotalBytes 文件总大小，只用于进度。
	TotalBytes int64
	Progress   func(ImportProgress)
}

// ImportProgress 是导入进度。
type ImportProgress struct {
	Bytes, TotalBytes, Statements, Errors int64
	Table                                 string
}

// StmtError 是一条语句的错误。Index 从 1 开始。
type StmtError struct {
	Index   int64
	Line    int
	Snippet string
	Error   string
}

// ImportResult 是导入结果。
type ImportResult struct {
	Statements, Errors int64
	ErrorList          []StmtError // 最多 100 条
	Bytes              int64
	// LemiDump 文件带 Lemi 注释头；Complete 文件完整（Lemi 与 mysqldump 文件看到了结束标记；其他文件读到末尾即为真）。
	LemiDump, Complete bool
	// Format 识别出的文件来源：lemi / navicat / mysqldump / unknown。
	Format string
	// Skipped 跳过的语句数（例如 Navicat SQLite 文件里重建 sqlite_sequence 的语句）。
	Skipped int64
}

const maxErrorList = 100

// Export 把数据库导出成 SQL 写进 w。
func Export(ctx context.Context, conn *sql.Conn, w io.Writer, opts ExportOptions) (*Manifest, error) {
	if opts.MaxStatementBytes <= 0 {
		opts.MaxStatementBytes = defaultMaxStmt
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	switch opts.Dialect {
	case MySQL:
		return exportMySQL(ctx, conn, w, opts)
	case SQLite:
		return exportSQLite(ctx, conn, w, opts)
	}
	return nil, fmt.Errorf("不支持的方言 %q", opts.Dialect)
}

// TableChecksum 计算表的行数与内容哈希，算法与导出时写进 Manifest 的相同，备份「还原校验」用它比对还原后的表。
// 哈希与行顺序无关：每行按列规范化编码后取 SHA-256 前 8 字节，所有行求和 mod 2^64。生成列不参与。
func TableChecksum(ctx context.Context, conn *sql.Conn, d Dialect, db, table string) (rows int64, hash string, err error) {
	switch d {
	case MySQL:
		return mysqlChecksum(ctx, conn, db, table)
	case SQLite:
		return sqliteChecksum(ctx, conn, table)
	}
	return 0, "", fmt.Errorf("不支持的方言 %q", d)
}

// ---------- 写出 ----------

// out 是带字节计数的缓冲写出。
type out struct {
	bw  *bufio.Writer
	n   int64
	err error
}

func newOut(w io.Writer) *out { return &out{bw: bufio.NewWriterSize(w, 256<<10)} }

func (o *out) write(b []byte) {
	if o.err != nil {
		return
	}
	n, err := o.bw.Write(b)
	o.n += int64(n)
	o.err = err
}

func (o *out) str(s string) {
	if o.err != nil {
		return
	}
	n, err := o.bw.WriteString(s)
	o.n += int64(n)
	o.err = err
}

func (o *out) flush() error {
	if o.err != nil {
		return o.err
	}
	o.err = o.bw.Flush()
	return o.err
}

// commentSafe 让任意名字可以安全地放进注释：去掉换行（防止名字逃出 -- 行注释变成 SQL），拆开 */。
func commentSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "*/", "* /")
}

func (o *out) section(title string) {
	o.str("\n-- ----------------------------\n-- ")
	o.str(commentSafe(title))
	o.str("\n-- ----------------------------\n")
}

type headerInfo struct {
	connName, serverType, versionNum, versionText, host, schema string
	now                                                         time.Time
	sqlite                                                      bool
}

func (o *out) header(h headerInfo) {
	o.str("/*\n " + headerTitle + "\n\n")
	o.str(" Source Server         : " + commentSafe(h.connName) + "\n")
	o.str(" Source Server Type    : " + h.serverType + "\n")
	o.str(" Source Server Version : " + h.versionNum + " (" + commentSafe(h.versionText) + ")\n")
	if !h.sqlite {
		o.str(" Source Host           : " + commentSafe(h.host) + "\n")
	}
	o.str(" Source Schema         : " + commentSafe(h.schema) + "\n\n")
	o.str(" Target Server Type    : " + h.serverType + "\n")
	o.str(" Target Server Version : " + h.versionNum + " (" + commentSafe(h.versionText) + ")\n")
	o.str(" File Encoding         : 65001\n\n")
	o.str(" Date: " + h.now.UTC().Format("02/01/2006 15:04:05") + "\n*/\n\n")
}

func (o *out) footer(now time.Time) {
	o.str("\n" + endMarkerPrefix + now.UTC().Format("2006-01-02 15:04:05") + " UTC\n")
}

// ---------- 进度 ----------

type progressor struct {
	fn   func(Progress)
	p    Progress
	last time.Time
}

func (p *progressor) emit(force bool) {
	if p.fn == nil {
		return
	}
	now := time.Now()
	if !force && now.Sub(p.last) < 200*time.Millisecond {
		return
	}
	p.last = now
	p.fn(p.p)
}

// ---------- 杂项 ----------

func isCanceled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

var errNoDatabases = errors.New("没有指定要导出的库")

func hexUpper(dst, src []byte) []byte {
	const digits = "0123456789ABCDEF"
	for _, c := range src {
		dst = append(dst, digits[c>>4], digits[c&15])
	}
	return dst
}
