package driver

import (
	"context"
	"database/sql"
	"strconv"
)

// SQLConn 是关系型数据源的能力（query / introspect / edit / ddl）。
type SQLConn interface {
	Conn
	// Dialect 方言名：mysql | sqlite
	Dialect() string
	// DB 底层连接池（读写）。
	DB() *sql.DB
	// ReadDB 只读连接池（SQLite 用只读方式打开的另一个池；MySQL 与 DB 相同，只读靠会话设置）。
	ReadDB() *sql.DB
	// Hooks 会话相关的方言差异。
	Hooks() SessionHooks
	// QuoteIdent 引用标识符。
	QuoteIdent(s string) string
	// QuoteString 引用字符串字面量。
	QuoteString(s string) string

	Databases(ctx context.Context) ([]DatabaseInfo, error)
	Objects(ctx context.Context, database string) (*ObjectList, error)
	Table(ctx context.Context, database, table string) (*TableInfo, error)
	DDL(ctx context.Context, database, kind, name string) (string, error)
	// ColumnKind 把数据库类型名归类为前端用的种类（int / decimal / string / binary / datetime ...）。
	ColumnKind(dbType string) string
	// ExplainSQL 返回执行计划语句与格式（json | table）。
	ExplainSQL(stmt string) (string, string)
	// AlterSQL 按表结构差异生成 DDL（表结构设计器）。old 为 nil 表示新建表。
	AlterSQL(ctx context.Context, database string, old *TableInfo, next *TableDesign) ([]string, error)
}

// SessionHooks 会话层的方言差异，由通用会话引擎调用。
type SessionHooks interface {
	// Init 新会话的初始化（时区、只读、执行时长上限、默认库）。
	Init(ctx context.Context, c *sql.Conn, o SessionOptions) error
	// SessionID 当前连接在服务器上的 ID（取消用），没有可返回空。
	SessionID(ctx context.Context, c *sql.Conn) string
	// Cancel 用另一条连接取消正在执行的语句。
	Cancel(ctx context.Context, db *sql.DB, sessionID string) error
	// Use 切换当前库。
	Use(ctx context.Context, c *sql.Conn, database string) error
	// Current 当前库。
	Current(ctx context.Context, c *sql.Conn) string
	// WrapReadOnly 只读会话里每条语句前后的包裹语句（例如 START TRANSACTION READ ONLY / COMMIT），不需要返回空。
	WrapReadOnly() (begin, end string)
	// NullSafeEq 空值安全的相等比较表达式（MySQL a <=> ?，SQLite a IS ?）。
	NullSafeEq(quotedCol string) string
}

// SessionOptions 会话选项。
type SessionOptions struct {
	ReadOnly bool
	Database string
	Timeout  int // 单条语句超时秒数，0 = 不限
}

// DatabaseInfo 是库（MySQL 的 schema、SQLite 的 main/附加库）。
type DatabaseInfo struct {
	Name      string `json:"name"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	System    bool   `json:"system,omitempty"` // mysql / sys / information_schema / performance_schema
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Tables    int    `json:"tables,omitempty"`
}

// ObjectList 是一个库里的全部对象。
type ObjectList struct {
	Tables     []ObjectInfo `json:"tables"`
	Views      []ObjectInfo `json:"views"`
	Procedures []ObjectInfo `json:"procedures"`
	Functions  []ObjectInfo `json:"functions"`
	Triggers   []ObjectInfo `json:"triggers"`
	Events     []ObjectInfo `json:"events"`
}

// ObjectInfo 是一个对象的概要。
type ObjectInfo struct {
	Name      string `json:"name"`
	Comment   string `json:"comment,omitempty"`
	Rows      int64  `json:"rows,omitempty"` // 估计值
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Engine    string `json:"engine,omitempty"`
	Table     string `json:"table,omitempty"` // 触发器所属的表
	Detail    string `json:"detail,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// TableInfo 是表的完整结构。
type TableInfo struct {
	Database    string            `json:"database"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // table | view
	Comment     string            `json:"comment,omitempty"`
	Engine      string            `json:"engine,omitempty"`
	Charset     string            `json:"charset,omitempty"`
	Collation   string            `json:"collation,omitempty"`
	RowsEst     int64             `json:"rows_est,omitempty"`
	AutoInc     int64             `json:"auto_increment,omitempty"`
	Columns     []ColumnInfo      `json:"columns"`
	Indexes     []IndexInfo       `json:"indexes"`
	ForeignKeys []ForeignKeyInfo  `json:"foreign_keys"`
	Referenced  []ForeignKeyInfo  `json:"referenced_by"` // 其他表引用本表的外键
	Triggers    []ObjectInfo      `json:"triggers"`
	Key         []string          `json:"key"` // 编辑用的行键（主键 / 非空唯一键 / rowid）
	KeyKind     string            `json:"key_kind,omitempty"` // primary | unique | rowid
	DDL         string            `json:"ddl,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	WithoutRowid bool             `json:"without_rowid,omitempty"`
}

// ColumnInfo 是列。
type ColumnInfo struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"` // 完整类型，例如 varchar(64)、int unsigned
	DataType      string   `json:"data_type"`
	Kind          string   `json:"kind"`
	Nullable      bool     `json:"nullable"`
	Default       *string  `json:"default"` // nil = 没有默认值；"NULL" 字面量单独处理
	DefaultIsExpr bool     `json:"default_is_expr,omitempty"`
	AutoIncrement bool     `json:"auto_increment,omitempty"`
	Generated     string   `json:"generated,omitempty"` // virtual | stored
	GenerationExpr string  `json:"generation_expr,omitempty"`
	Comment       string   `json:"comment,omitempty"`
	Charset       string   `json:"charset,omitempty"`
	Collation     string   `json:"collation,omitempty"`
	Length        int64    `json:"length,omitempty"`
	Precision     int      `json:"precision,omitempty"`
	Scale         int      `json:"scale,omitempty"`
	Unsigned      bool     `json:"unsigned,omitempty"`
	EnumValues    []string `json:"enum_values,omitempty"`
	Extra         string   `json:"extra,omitempty"`
	PrimaryKey    bool     `json:"primary_key,omitempty"`
	Invisible     bool     `json:"invisible,omitempty"`
}

// IndexInfo 是索引。
type IndexInfo struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Lengths []int    `json:"lengths,omitempty"` // 前缀长度，0 = 整列
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Type    string   `json:"type,omitempty"` // BTREE | HASH | FULLTEXT | SPATIAL
	Comment string   `json:"comment,omitempty"`
}

// ForeignKeyInfo 是外键。
type ForeignKeyInfo struct {
	Name       string   `json:"name"`
	Table      string   `json:"table"` // 外键所在的表（referenced_by 里有意义）
	Columns    []string `json:"columns"`
	RefDB      string   `json:"ref_database"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	OnDelete   string   `json:"on_delete,omitempty"`
	OnUpdate   string   `json:"on_update,omitempty"`
}

// TableDesign 是表结构设计器提交的目标结构。
type TableDesign struct {
	Name        string           `json:"name"`
	Comment     string           `json:"comment"`
	Engine      string           `json:"engine"`
	Charset     string           `json:"charset"`
	Collation   string           `json:"collation"`
	AutoInc     int64            `json:"auto_increment"`
	Columns     []ColumnDesign   `json:"columns"`
	Indexes     []IndexInfo      `json:"indexes"`
	ForeignKeys []ForeignKeyInfo `json:"foreign_keys"`
}

// ColumnDesign 是设计器里的列；OrigName 为空表示新列。
type ColumnDesign struct {
	OrigName      string  `json:"orig_name"`
	Name          string  `json:"name"`
	Type          string  `json:"type"` // 完整类型文本，例如 varchar(64)、decimal(10,2) unsigned
	Nullable      bool    `json:"nullable"`
	Default       *string `json:"default"`
	DefaultIsExpr bool    `json:"default_is_expr"`
	AutoIncrement bool    `json:"auto_increment"`
	Comment       string  `json:"comment"`
	Charset       string  `json:"charset"`
	Collation     string  `json:"collation"`
	PrimaryKey    bool    `json:"primary_key"`
}

func toString(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
