package driver

import "context"

// Monitor 是服务器监控能力（§10）。
type Monitor interface {
	Processes(ctx context.Context) ([]map[string]any, error)
	KillProcess(ctx context.Context, id string, queryOnly bool) error
	Status(ctx context.Context) (map[string]string, error)    // 全局状态变量
	Variables(ctx context.Context) (map[string]string, error) // 全局变量
	TableSizes(ctx context.Context, limit int) ([]map[string]any, error)
	SlowQueries(ctx context.Context, limit int) ([]map[string]any, error)
}

// DBUser 是数据库账号。
type DBUser struct {
	User   string   `json:"user"`
	Host   string   `json:"host"`
	Plugin string   `json:"plugin,omitempty"`
	Locked bool     `json:"locked,omitempty"`
	Grants []string `json:"grants,omitempty"`
}

// UserAdmin 是数据库账号与权限管理能力（§10）。
type UserAdmin interface {
	Users(ctx context.Context) ([]DBUser, error)
	Grants(ctx context.Context, user, host string) ([]string, error)
	// UserSQL 生成账号操作语句（create / drop / password / grant / revoke / readonly），由调用方预览后执行。
	UserSQL(op string, p map[string]any) ([]string, error)
}

// NetworkDriver 表示驱动经网络连接（连接表单需要 SSH 隧道与代理设置）。
type NetworkDriver interface {
	// HostPort 从参数里取数据库地址。
	HostPort(params map[string]any) (string, int)
}
