// Package mysql 是 MySQL / MariaDB 驱动。
package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	gomysql "github.com/go-sql-driver/mysql"
)

func init() { driver.Register(&Driver{}) }

// Driver 是 MySQL 驱动。
type Driver struct{}

var tlsModes = []driver.Option{
	{Value: "preferred", Label: "优先加密（服务器支持就加密，不校验证书）"},
	{Value: "disable", Label: "不加密"},
	{Value: "required", Label: "必须加密（不校验证书）"},
	{Value: "verify-ca", Label: "加密并校验 CA"},
	{Value: "verify-full", Label: "加密并校验 CA 与主机名"},
}

// Info 驱动描述。
func (d *Driver) Info() driver.Info {
	tlsShow := map[string][]string{"tls_mode": {"required", "verify-ca", "verify-full"}}
	return driver.Info{
		ID: "mysql", Name: "MySQL / MariaDB", Category: "relational", QueryLanguage: "sql:mysql", DefaultPort: 3306,
		Capabilities: []string{driver.CapNavigate, driver.CapIntrospect, driver.CapQuery, driver.CapEdit, driver.CapDDL,
			driver.CapDump, driver.CapBackup, driver.CapMonitor, driver.CapUsers, driver.CapReadOnly},
		Form: []driver.FormField{
			{Key: "host", Label: "主机", LabelEN: "Host", Type: "text", Group: "basic", Required: true, Placeholder: "127.0.0.1"},
			{Key: "port", Label: "端口", LabelEN: "Port", Type: "number", Group: "basic", Required: true, Default: 3306},
			{Key: "user", Label: "用户名", LabelEN: "User", Type: "text", Group: "basic", Required: true},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Group: "basic", Secret: true},
			{Key: "database", Label: "默认库", LabelEN: "Default database", Type: "text", Group: "basic", Help: "留空则显示全部库"},
			{Key: "tls_mode", Label: "加密方式", LabelEN: "TLS", Type: "select", Group: "tls", Default: "preferred", Options: tlsModes},
			{Key: "tls_ca", Label: "CA 证书（PEM）", LabelEN: "CA certificate (PEM)", Type: "textarea", Group: "tls", ShowIf: map[string][]string{"tls_mode": {"verify-ca", "verify-full"}}},
			{Key: "tls_cert", Label: "客户端证书（PEM，可选）", LabelEN: "Client certificate (PEM)", Type: "textarea", Group: "tls", ShowIf: tlsShow},
			{Key: "tls_key", Label: "客户端私钥（PEM，可选）", LabelEN: "Client key (PEM)", Type: "textarea", Group: "tls", Secret: true, ShowIf: tlsShow},
			{Key: "tls_server_name", Label: "证书主机名（可选）", LabelEN: "TLS server name", Type: "text", Group: "tls", ShowIf: map[string][]string{"tls_mode": {"verify-full"}}},
			{Key: "charset", Label: "字符集", LabelEN: "Charset", Type: "select", Group: "advanced", Default: "utf8mb4", Options: []driver.Option{{Value: "utf8mb4", Label: "utf8mb4"}, {Value: "utf8", Label: "utf8"}, {Value: "latin1", Label: "latin1"}, {Value: "gbk", Label: "gbk"}}},
			{Key: "timezone", Label: "会话时区", LabelEN: "Session time zone", Type: "text", Group: "advanced", Placeholder: "+08:00", Help: "TIMESTAMP 列按这个时区显示与写入；留空用服务器默认"},
			{Key: "connect_timeout", Label: "连接超时（秒）", LabelEN: "Connect timeout (s)", Type: "number", Group: "advanced", Default: 15},
		},
	}
}

// HostPort 实现 driver.NetworkDriver。
func (d *Driver) HostPort(p map[string]any) (string, int) {
	cfg := &driver.ConnConfig{Params: p}
	return cfg.Str("host"), cfg.Int("port", 3306)
}

// Conn 是打开的 MySQL 数据源。
type Conn struct {
	db      *sql.DB
	cfg     *driver.ConnConfig
	mu      sync.Mutex
	info    *driver.ServerInfo
	mariadb bool
	// plaintext 「优先加密」握手失败后退回了不加密连接（界面上提示）
	plaintext bool
	major     int
	minor     int
	patch     int
}

func buildTLS(cfg *driver.ConnConfig, host string) (*tls.Config, bool, error) {
	mode := cfg.Str("tls_mode")
	if mode == "" {
		mode = "preferred"
	}
	switch mode {
	case "disable":
		return nil, false, nil
	case "preferred":
		return legacyTLS(&tls.Config{InsecureSkipVerify: true}), true, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if certPEM, keyPEM := cfg.Str("tls_cert"), cfg.Str("tls_key"); certPEM != "" || keyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, false, fmt.Errorf("客户端证书或私钥无法解析：%w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	switch mode {
	case "required":
		tc.InsecureSkipVerify = true
		legacyTLS(tc)
	case "verify-ca", "verify-full":
		pool := x509.NewCertPool()
		if ca := cfg.Str("tls_ca"); ca != "" {
			if !pool.AppendCertsFromPEM([]byte(ca)) {
				return nil, false, errors.New("CA 证书无法解析（需要 PEM 格式）")
			}
		} else if sys, err := x509.SystemCertPool(); err == nil {
			pool = sys
		}
		tc.RootCAs = pool
		if mode == "verify-full" {
			tc.ServerName = host
			if sn := cfg.Str("tls_server_name"); sn != "" {
				tc.ServerName = sn
			}
		} else {
			// 只校验证书链，不校验主机名
			tc.InsecureSkipVerify = true
			tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("服务器没有提供证书")
				}
				certs := make([]*x509.Certificate, len(raw))
				for i, b := range raw {
					c, err := x509.ParseCertificate(b)
					if err != nil {
						return err
					}
					certs[i] = c
				}
				inter := x509.NewCertPool()
				for _, c := range certs[1:] {
					inter.AddCert(c)
				}
				_, err := certs[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter})
				return err
			}
		}
	default:
		return nil, false, fmt.Errorf("未知的加密方式 %q", mode)
	}
	return tc, false, nil
}

// Open 打开数据源。
func (d *Driver) Open(ctx context.Context, cfg *driver.ConnConfig) (driver.Conn, error) {
	host, port := d.HostPort(cfg.Params)
	if host == "" {
		return nil, errors.New("请填写主机")
	}
	if cfg.Dialer == nil {
		return nil, errors.New("内部错误：缺少网络拨号器")
	}
	mc := gomysql.NewConfig()
	mc.User = cfg.Str("user")
	mc.Passwd = cfg.Str("password")
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	mc.DBName = cfg.Str("database")
	charset := cfg.Str("charset")
	switch charset {
	case "", "utf8mb4":
		mc.Collation = "utf8mb4_general_ci"
	case "utf8":
		mc.Collation = "utf8_general_ci"
	case "latin1":
		mc.Collation = "latin1_swedish_ci"
	case "gbk":
		mc.Collation = "gbk_chinese_ci"
	default:
		return nil, errors.New("不支持的字符集")
	}
	mc.Loc = time.UTC
	mc.ParseTime = false
	mc.InterpolateParams = true
	mc.MultiStatements = false
	mc.AllowAllFiles = false // 永不允许 LOAD DATA LOCAL INFILE 读取本机文件
	mc.AllowNativePasswords = true
	mc.CheckConnLiveness = true
	mc.ClientFoundRows = true // 影响行数按匹配行计（表格提交时据此判断行是否还在）
	mc.MaxAllowedPacket = 64 << 20
	to := cfg.Int("connect_timeout", 15)
	if to <= 0 || to > 120 {
		to = 15
	}
	mc.Timeout = time.Duration(to) * time.Second
	tc, fallback, err := buildTLS(cfg, host)
	if err != nil {
		return nil, err
	}
	mc.TLS = tc
	mc.AllowFallbackToPlaintext = fallback
	mc.AllowCleartextPasswords = tc != nil && !fallback // 明文口令插件只在确定加密时允许
	mc.DialFunc = cfg.Dialer.DialContext
	if tz := strings.TrimSpace(cfg.Str("timezone")); tz != "" {
		if !validTZ(tz) {
			return nil, errors.New("会话时区格式不正确（例如 +08:00 或 Asia/Shanghai）")
		}
		mc.Params = map[string]string{"time_zone": "'" + tz + "'"}
	}
	connector, err := gomysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(time.Hour)
	c := &Conn{db: db, cfg: cfg}
	if err := c.Ping(ctx); err != nil {
		db.Close()
		// 「优先加密」：加密建立不起来时退回不加密（与 MySQL 客户端 --ssl-mode=PREFERRED 的定义一致）
		if fallback && isTLSErr(err) {
			mc.TLS = nil
			mc.AllowCleartextPasswords = false
			connector, cerr := gomysql.NewConnector(mc)
			if cerr != nil {
				return nil, cerr
			}
			db = sql.OpenDB(connector)
			db.SetMaxOpenConns(10)
			db.SetMaxIdleConns(3)
			db.SetConnMaxIdleTime(5 * time.Minute)
			db.SetConnMaxLifetime(time.Hour)
			c = &Conn{db: db, cfg: cfg, plaintext: true}
			if err2 := c.Ping(ctx); err2 != nil {
				db.Close()
				return nil, friendlyErr(err2)
			}
		} else {
			return nil, friendlyErr(err)
		}
	}
	if _, err := c.Server(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

// legacyTLS 不校验证书的两种模式（优先加密、必须加密）兼容老服务器：MySQL 5.7 的部分构建只支持 TLS 1.0/1.1
// 或 RSA 密钥交换套件。这两种模式本来就不防中间人，放宽版本不降低它们的安全性；校验证书的模式仍要求 TLS 1.2+。
func legacyTLS(tc *tls.Config) *tls.Config {
	tc.MinVersion = tls.VersionTLS10
	var suites []uint16
	for _, s := range tls.CipherSuites() {
		suites = append(suites, s.ID)
	}
	for _, s := range tls.InsecureCipherSuites() {
		switch s.ID {
		case tls.TLS_RSA_WITH_AES_128_CBC_SHA, tls.TLS_RSA_WITH_AES_256_CBC_SHA, tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384:
			suites = append(suites, s.ID)
		}
	}
	tc.CipherSuites = suites
	return tc
}

func isTLSErr(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "tls:") || strings.Contains(m, "handshake") || strings.Contains(m, "x509")
}

func validTZ(tz string) bool {
	for _, r := range tz {
		if !(r == '+' || r == '-' || r == ':' || r == '/' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return len(tz) <= 64
}

func friendlyErr(err error) error {
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1045:
			return errors.New("用户名或口令不正确（MySQL 1045）")
		case 1049:
			return fmt.Errorf("默认库不存在：%s", me.Message)
		case 1044:
			return fmt.Errorf("没有访问该库的权限：%s", me.Message)
		}
	}
	return err
}

// Ping 检查连通。
func (c *Conn) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return c.db.PingContext(ctx)
}

// Server 服务器信息。
func (c *Conn) Server(ctx context.Context) (driver.ServerInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info != nil {
		return *c.info, nil
	}
	var ver, comment string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION(), @@version_comment").Scan(&ver, &comment); err != nil {
		return driver.ServerInfo{}, err
	}
	info := driver.ServerInfo{Product: "MySQL", Version: ver, Extra: map[string]any{"comment": comment, "tls_fallback_plaintext": c.plaintext}}
	if strings.Contains(strings.ToLower(ver+comment), "mariadb") {
		info.Product = "MariaDB"
		c.mariadb = true
	}
	c.major, c.minor, c.patch = parseVersion(ver)
	c.info = &info
	return info, nil
}

func parseVersion(v string) (int, int, int) {
	v = strings.TrimPrefix(v, "5.5.5-") // MariaDB 10 的兼容前缀
	parts := strings.SplitN(v, ".", 3)
	num := func(s string) int {
		n := 0
		for _, r := range s {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	var a, b, c int
	if len(parts) > 0 {
		a = num(parts[0])
	}
	if len(parts) > 1 {
		b = num(parts[1])
	}
	if len(parts) > 2 {
		c = num(parts[2])
	}
	return a, b, c
}

// IsMariaDB 是否 MariaDB。
func (c *Conn) IsMariaDB() bool { return c.mariadb }

// Version 版本号三段。
func (c *Conn) Version() (int, int, int) { return c.major, c.minor, c.patch }

// Close 关闭连接池。
func (c *Conn) Close() error { return c.db.Close() }

// Dialect 方言。
func (c *Conn) Dialect() string { return "mysql" }

// DB 连接池。
func (c *Conn) DB() *sql.DB { return c.db }

// ReadDB 只读也用同一个池（只读靠会话设置保证）。
func (c *Conn) ReadDB() *sql.DB { return c.db }

// QuoteIdent 反引号引用。
func (c *Conn) QuoteIdent(s string) string { return QuoteIdent(s) }

// QuoteIdent 反引号引用标识符。
func QuoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// QuoteString 单引号字符串（按默认 sql_mode 转义反斜杠）。
func (c *Conn) QuoteString(s string) string { return QuoteString(s) }

// QuoteString 单引号字符串。
func QuoteString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; ch {
		case 0:
			b.WriteString(`\0`)
		case '\'':
			b.WriteString(`\'`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0x1a:
			b.WriteString(`\Z`)
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// ColumnKind 类型归类。
func (c *Conn) ColumnKind(t string) string { return ColumnKind(t) }

// ColumnKind 把 MySQL 类型名归类。
func ColumnKind(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if i := strings.IndexAny(t, "( "); i > 0 {
		t = t[:i]
	}
	switch t {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "UNSIGNED", "INT1", "INT2", "INT3", "INT4", "INT8":
		return "int"
	case "DECIMAL", "NUMERIC", "DEC", "FIXED":
		return "decimal"
	case "FLOAT", "DOUBLE", "REAL":
		return "float"
	case "BIT":
		return "bit"
	case "BOOL", "BOOLEAN":
		return "bool"
	case "CHAR", "VARCHAR", "NCHAR", "NVARCHAR":
		return "string"
	case "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT":
		return "text"
	case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		return "binary"
	case "DATE":
		return "date"
	case "DATETIME", "TIMESTAMP":
		return "datetime"
	case "TIME":
		return "time"
	case "YEAR":
		return "year"
	case "JSON":
		return "json"
	case "ENUM":
		return "enum"
	case "SET":
		return "set"
	case "GEOMETRY", "POINT", "LINESTRING", "POLYGON", "MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION", "GEOMCOLLECTION":
		return "geometry"
	case "VECTOR":
		return "binary"
	}
	return "other"
}

// ExplainSQL 执行计划。
func (c *Conn) ExplainSQL(stmt string) (string, string) {
	return "EXPLAIN FORMAT=JSON " + stmt, "json"
}

// Hooks 会话钩子。
func (c *Conn) Hooks() driver.SessionHooks { return hooks{c: c} }

type hooks struct{ c *Conn }

func (h hooks) Init(ctx context.Context, conn *sql.Conn, o driver.SessionOptions) error {
	if o.ReadOnly {
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			return fmt.Errorf("设置只读会话失败：%w", err)
		}
	}
	if o.Timeout > 0 {
		if h.c.mariadb {
			_, _ = conn.ExecContext(ctx, fmt.Sprintf("SET SESSION max_statement_time = %d", o.Timeout))
		} else {
			_, _ = conn.ExecContext(ctx, fmt.Sprintf("SET SESSION max_execution_time = %d", o.Timeout*1000))
		}
	}
	if o.Database != "" {
		if _, err := conn.ExecContext(ctx, "USE "+QuoteIdent(o.Database)); err != nil {
			return err
		}
	}
	return nil
}

func (h hooks) SessionID(ctx context.Context, conn *sql.Conn) string {
	var id string
	_ = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	return id
}

func (h hooks) Cancel(ctx context.Context, db *sql.DB, id string) error {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, "KILL QUERY "+id)
	return err
}

func (h hooks) Use(ctx context.Context, conn *sql.Conn, db string) error {
	_, err := conn.ExecContext(ctx, "USE "+QuoteIdent(db))
	return err
}

func (h hooks) Current(ctx context.Context, conn *sql.Conn) string {
	var db sql.NullString
	_ = conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db)
	return db.String
}

func (h hooks) WrapReadOnly() (string, string) { return "START TRANSACTION READ ONLY", "COMMIT" }

func (h hooks) NullSafeEq(col string) string { return col + " <=> ?" }
