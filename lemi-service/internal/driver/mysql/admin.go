package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

func scanMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			if vals[i].Valid {
				m[strings.ToLower(c)] = vals[i].String
			} else {
				m[strings.ToLower(c)] = nil
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Processes 进程列表。
func (c *Conn) Processes(ctx context.Context) ([]map[string]any, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, LEFT(INFO, 2000) AS INFO FROM information_schema.PROCESSLIST ORDER BY TIME DESC`)
	if err != nil {
		rows, err = c.db.QueryContext(ctx, "SHOW FULL PROCESSLIST")
		if err != nil {
			return nil, err
		}
	}
	return scanMaps(rows)
}

// KillProcess 杀查询或连接。
func (c *Conn) KillProcess(ctx context.Context, id string, queryOnly bool) error {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return errors.New("进程 ID 不正确")
	}
	stmt := "KILL CONNECTION " + id
	if queryOnly {
		stmt = "KILL QUERY " + id
	}
	_, err := c.db.ExecContext(ctx, stmt)
	return err
}

func (c *Conn) kv(ctx context.Context, q string) (map[string]string, error) {
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v sql.NullString
		if rows.Scan(&k, &v) == nil {
			out[k] = v.String
		}
	}
	return out, rows.Err()
}

// Status 全局状态变量。
func (c *Conn) Status(ctx context.Context) (map[string]string, error) { return c.kv(ctx, "SHOW GLOBAL STATUS") }

// Variables 全局变量。
func (c *Conn) Variables(ctx context.Context) (map[string]string, error) {
	return c.kv(ctx, "SHOW GLOBAL VARIABLES")
}

// TableSizes 表大小排行。
func (c *Conn) TableSizes(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := c.db.QueryContext(ctx, `SELECT TABLE_SCHEMA AS db, TABLE_NAME AS name, ENGINE AS engine, TABLE_ROWS AS rows_est,
		DATA_LENGTH AS data_bytes, INDEX_LENGTH AS index_bytes, DATA_FREE AS free_bytes, (DATA_LENGTH + INDEX_LENGTH) AS total_bytes
		FROM information_schema.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA NOT IN ('mysql','sys','information_schema','performance_schema')
		ORDER BY total_bytes DESC LIMIT `+strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	return scanMaps(rows)
}

// SlowQueries 按耗时汇总的语句（需要 performance_schema）。
func (c *Conn) SlowQueries(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := c.db.QueryContext(ctx, `SELECT SCHEMA_NAME AS db, LEFT(DIGEST_TEXT, 2000) AS query, COUNT_STAR AS calls,
		ROUND(SUM_TIMER_WAIT / 1000000000, 1) AS total_ms, ROUND(AVG_TIMER_WAIT / 1000000000, 2) AS avg_ms,
		ROUND(MAX_TIMER_WAIT / 1000000000, 2) AS max_ms, SUM_ROWS_EXAMINED AS rows_examined, SUM_ROWS_SENT AS rows_sent, LAST_SEEN AS last_seen
		FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT IS NOT NULL ORDER BY SUM_TIMER_WAIT DESC LIMIT `+strconv.Itoa(limit))
	if err != nil {
		return nil, fmt.Errorf("读取慢查询需要开启 performance_schema 并有相应权限：%w", err)
	}
	return scanMaps(rows)
}

// Users 数据库账号。
func (c *Conn) Users(ctx context.Context) ([]driver.DBUser, error) {
	q := `SELECT User, Host, COALESCE(plugin, ''), COALESCE(account_locked, 'N') FROM mysql.user ORDER BY User, Host`
	if c.mariadb {
		q = `SELECT User, Host, COALESCE(plugin, ''), 'N' FROM mysql.user ORDER BY User, Host`
	}
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("读取账号列表需要 mysql.user 的查询权限：%w", err)
	}
	defer rows.Close()
	var out []driver.DBUser
	for rows.Next() {
		var u driver.DBUser
		var locked string
		if rows.Scan(&u.User, &u.Host, &u.Plugin, &locked) == nil {
			u.Locked = locked == "Y"
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// Grants 账号的授权语句。
func (c *Conn) Grants(ctx context.Context, user, host string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, "SHOW GRANTS FOR "+QuoteString(user)+"@"+QuoteString(host))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if rows.Scan(&g) == nil {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

var privWhitelist = map[string]bool{
	"ALL PRIVILEGES": true, "SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true, "DROP": true, "ALTER": true,
	"INDEX": true, "REFERENCES": true, "CREATE VIEW": true, "SHOW VIEW": true, "TRIGGER": true, "EXECUTE": true, "CREATE ROUTINE": true,
	"ALTER ROUTINE": true, "EVENT": true, "LOCK TABLES": true, "CREATE TEMPORARY TABLES": true, "PROCESS": true, "RELOAD": true,
	"SHOW DATABASES": true, "REPLICATION CLIENT": true, "REPLICATION SLAVE": true, "GRANT OPTION": true,
}

func str(p map[string]any, k string) string {
	if v, ok := p[k].(string); ok {
		return v
	}
	return ""
}

// UserSQL 生成账号操作语句。op：create | drop | password | grant | revoke | readonly | lock | unlock。
func (c *Conn) UserSQL(op string, p map[string]any) ([]string, error) {
	user, host := str(p, "user"), str(p, "host")
	if user == "" {
		return nil, errors.New("请填写用户名")
	}
	if host == "" {
		host = "%"
	}
	acct := QuoteString(user) + "@" + QuoteString(host)
	target := func() (string, error) {
		db, table := str(p, "database"), str(p, "table")
		switch {
		case db == "" || db == "*":
			return "*.*", nil
		case table == "" || table == "*":
			return QuoteIdent(db) + ".*", nil
		default:
			return QuoteIdent(db) + "." + QuoteIdent(table), nil
		}
	}
	privs := func() (string, error) {
		raw, _ := p["privileges"].([]any)
		var out []string
		for _, x := range raw {
			s := strings.ToUpper(strings.TrimSpace(fmt.Sprint(x)))
			if !privWhitelist[s] {
				return "", fmt.Errorf("不支持的权限：%s", s)
			}
			if s != "GRANT OPTION" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return "", errors.New("请至少选择一项权限")
		}
		return strings.Join(out, ", "), nil
	}
	switch op {
	case "create":
		pw := str(p, "password")
		if len(pw) < 8 {
			return nil, errors.New("口令至少 8 位")
		}
		return []string{"CREATE USER " + acct + " IDENTIFIED BY " + QuoteString(pw)}, nil
	case "drop":
		return []string{"DROP USER " + acct}, nil
	case "password":
		pw := str(p, "password")
		if len(pw) < 8 {
			return nil, errors.New("口令至少 8 位")
		}
		return []string{"ALTER USER " + acct + " IDENTIFIED BY " + QuoteString(pw)}, nil
	case "lock":
		return []string{"ALTER USER " + acct + " ACCOUNT LOCK"}, nil
	case "unlock":
		return []string{"ALTER USER " + acct + " ACCOUNT UNLOCK"}, nil
	case "grant", "revoke":
		pv, err := privs()
		if err != nil {
			return nil, err
		}
		t, _ := target()
		if op == "grant" {
			s := "GRANT " + pv + " ON " + t + " TO " + acct
			if b, _ := p["with_grant"].(bool); b {
				s += " WITH GRANT OPTION"
			}
			return []string{s}, nil
		}
		return []string{"REVOKE " + pv + " ON " + t + " FROM " + acct}, nil
	case "readonly":
		// 一键只读账号：SELECT、SHOW VIEW（可限定库）
		pw := str(p, "password")
		if len(pw) < 12 {
			return nil, errors.New("只读账号的口令至少 12 位")
		}
		t, _ := target()
		return []string{
			"CREATE USER " + acct + " IDENTIFIED BY " + QuoteString(pw),
			"GRANT SELECT, SHOW VIEW ON " + t + " TO " + acct,
		}, nil
	}
	return nil, fmt.Errorf("未知操作 %q", op)
}
