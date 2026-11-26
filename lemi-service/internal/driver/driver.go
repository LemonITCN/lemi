// Package driver 定义数据库驱动插件的接口与注册表（docs/spec.md §6）。
//
// 每个驱动在自己的子包里实现 Driver，并在 init 里 Register。主干（连接管理、表格、智能体、备份）只依赖这里的接口，
// 按驱动声明的能力决定提供哪些功能；以后增加 PostgreSQL、MongoDB、Elasticsearch 不需要改主干。
package driver

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/LemonITCN/lemi/lemi-service/internal/netdial"
)

// 能力。
const (
	CapNavigate   = "navigate"   // 对象树
	CapIntrospect = "introspect" // 结构元数据
	CapQuery      = "query"      // 会话执行
	CapEdit       = "edit"       // 表格变更集
	CapDDL        = "ddl"        // DDL 与表结构设计器
	CapDump       = "dump"       // 自研 SQL 导出导入
	CapBackup     = "backup"     // 备份
	CapMonitor    = "monitor"    // 服务器监控
	CapUsers      = "users"      // 账号与权限
	CapReadOnly   = "readonly"   // 会话层强制只读
)

// Option 是下拉选项。
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// FormField 是连接表单的一个字段。前端按它渲染，增加驱动不用改前端表单。
type FormField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	LabelEN     string   `json:"label_en,omitempty"`
	Type        string   `json:"type"` // text | password | number | select | bool | textarea | file | sqlite_file
	Group       string   `json:"group,omitempty"` // basic | tls | advanced
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"` // 加密保存、只写不读
	Default     any      `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []Option `json:"options,omitempty"`
	// ShowIf 仅当另一个字段等于某些值时显示，例如 {"tls_mode": ["verify-ca","verify-full"]}
	ShowIf map[string][]string `json:"show_if,omitempty"`
}

// Info 是驱动的描述。
type Info struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Category      string      `json:"category"`       // relational | document | search | keyvalue
	QueryLanguage string      `json:"query_language"` // sql:mysql | sql:sqlite | ...
	DefaultPort   int         `json:"default_port,omitempty"`
	FileBased     bool        `json:"file_based,omitempty"`
	Form          []FormField `json:"form"`
	Capabilities  []string    `json:"capabilities"`
}

// Has 是否具备某能力。
func (i Info) Has(c string) bool {
	for _, x := range i.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// SecretKeys 返回需要加密保存的参数名。
func (i Info) SecretKeys() []string {
	var out []string
	for _, f := range i.Form {
		if f.Secret {
			out = append(out, f.Key)
		}
	}
	return out
}

// SQLiteEnv 是 SQLite 文件的允许范围。
type SQLiteEnv struct {
	AllowedDirs []string // 管理员配置的服务器目录
	UploadDir   string   // <数据目录>/sqlite
}

// ConnConfig 是打开一个连接所需的全部参数（密钥已解密）。
type ConnConfig struct {
	ID       string
	Params   map[string]any
	Dialer   *netdial.Dialer // 网络类驱动用它拨号（经网络策略、代理、SSH）
	ReadOnly bool            // 连接级强制只读
	SQLite   SQLiteEnv
}

// Str 取字符串参数。
func (c *ConnConfig) Str(k string) string {
	switch v := c.Params[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return toString(v)
	}
}

// Int 取整数参数。
func (c *ConnConfig) Int(k string, def int) int {
	switch v := c.Params[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n := 0
		for _, r := range v {
			if r < '0' || r > '9' {
				return def
			}
			n = n*10 + int(r-'0')
		}
		if v == "" {
			return def
		}
		return n
	}
	return def
}

// Bool 取布尔参数。
func (c *ConnConfig) Bool(k string) bool {
	switch v := c.Params[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// ServerInfo 是服务器信息。
type ServerInfo struct {
	Product string         `json:"product"` // MySQL | MariaDB | SQLite ...
	Version string         `json:"version"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// Conn 是一个打开的数据源（内部是连接池）。
type Conn interface {
	Ping(ctx context.Context) error
	Server(ctx context.Context) (ServerInfo, error)
	Close() error
}

// Driver 是驱动。
type Driver interface {
	Info() Info
	Open(ctx context.Context, cfg *ConnConfig) (Conn, error)
}

var (
	regMu   sync.RWMutex
	drivers = map[string]Driver{}
)

// Register 注册驱动。
func Register(d Driver) {
	regMu.Lock()
	defer regMu.Unlock()
	drivers[d.Info().ID] = d
}

// Get 取驱动。
func Get(id string) (Driver, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := drivers[id]
	return d, ok
}

// All 全部驱动（按 ID 排序）。
func All() []Driver {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().ID < out[j].Info().ID })
	return out
}

// ErrUnsupported 驱动不支持该操作。
var ErrUnsupported = errors.New("该数据库类型不支持此操作")
