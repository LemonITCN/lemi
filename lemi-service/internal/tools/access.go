package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
)

// ConnAccess 是智能体对一个连接的实际权限。
type ConnAccess struct {
	Conn      *conns.Connection
	UserLevel string   // 用户对连接的权限（none / read / write / manage）
	AI        string   // 实际 AI 档位
	Allow     []string // 库表白名单（空 = 不限）
}

// EffectiveAI 计算实际档位 = min(用户对连接的权限, 连接 AI 档位, 授权档位, 会话模式)。
// 用户只读（查看者、个人收紧为 read、连接设为只读）时最多只读；「完全放开」需要实例管理员允许，否则按「增删改自动」。
func EffectiveAI(c *conns.Connection, userLevel string, grant Grant, plan, allowFull bool) string {
	lvl := c.AILevel
	if !conns.ValidAILevel(lvl) {
		return conns.AIHidden
	}
	switch userLevel {
	case conns.LevelNone, "":
		return conns.AIHidden
	case conns.LevelRead:
		lvl = conns.MinAI(lvl, conns.AIRead)
	}
	if c.ReadOnly {
		lvl = conns.MinAI(lvl, conns.AIRead)
	}
	if lvl == conns.AIFull && !allowFull {
		lvl = conns.AIWriteAuto
	}
	if grant != nil {
		g, ok := grant[c.ID]
		if !ok || !conns.ValidAILevel(g) {
			g = conns.AIHidden
		}
		lvl = conns.MinAI(lvl, g)
	}
	if plan {
		lvl = conns.MinAI(lvl, conns.AIRead)
	}
	return lvl
}

func (env *Env) access(c *conns.Connection) *ConnAccess {
	ul := env.Svc.Conns.Access(env.Ctx, env.User, c)
	if c.WorkspaceID != env.WorkspaceID {
		ul = conns.LevelNone
	}
	allowFull := env.Svc.Settings.System(env.Ctx).AllowAIFullAccess
	return &ConnAccess{Conn: c, UserLevel: ul, AI: EffectiveAI(c, ul, env.Grant, env.Plan, allowFull), Allow: c.AIAllowList()}
}

// Access 取连接并计算权限；不可见的连接与不存在一样报错。
func (env *Env) Access(connID string) (*ConnAccess, error) {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return nil, fail("请提供 connection_id（用 list_connections 查看可用连接）")
	}
	c, err := env.Svc.Conns.Get(env.Ctx, connID)
	if err != nil {
		return nil, fail("连接 %s 不存在或对智能体不可见（用 list_connections 查看可用连接）", connID)
	}
	a := env.access(c)
	if a.AI == conns.AIHidden {
		return nil, fail("连接 %s 不存在或对智能体不可见（用 list_connections 查看可用连接）", connID)
	}
	return a, nil
}

// Visible 当前工作空间里对智能体可见的连接。
func (env *Env) Visible() ([]*ConnAccess, error) {
	list, err := env.Svc.Conns.List(env.Ctx, env.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []*ConnAccess
	for i := range list {
		a := env.access(&list[i])
		if a.AI != conns.AIHidden {
			out = append(out, a)
		}
	}
	return out, nil
}

// CanSeeDB 白名单里是否有这个库。
func (a *ConnAccess) CanSeeDB(db string) bool { return conns.AllowsDatabase(a.Allow, a.dbName(db)) }

// CanSeeTable 白名单是否允许这张表。
func (a *ConnAccess) CanSeeTable(db, table string) bool {
	return conns.AllowsTable(a.Allow, a.dbName(db), table)
}

func (a *ConnAccess) dbName(db string) string {
	if a.Conn.Driver == "sqlite" && (db == "" || strings.EqualFold(db, "main")) {
		return "main"
	}
	return db
}

// CanWrite 是否可以写（至少「读写·逐条确认」）。
func (a *ConnAccess) CanWrite() bool { return conns.AIRank(a.AI) >= conns.AIRank(conns.AIWriteConfirm) }

// GrantLevels 授权（MCP、定时任务）里可选的档位：没有「逐条确认」，因为没有人能在 Lemi 里逐条审批。
var GrantLevels = []string{conns.AIHidden, conns.AIRead, conns.AIWriteAuto, conns.AIFull}

// ValidGrantLevel 是否是授权可选的档位。
func ValidGrantLevel(l string) bool {
	for _, x := range GrantLevels {
		if x == l {
			return true
		}
	}
	return false
}

// MaxGrantLevel 授权里一个连接最高能选的档位：不高于实际档位 eff，且只能是 GrantLevels 之一。
func MaxGrantLevel(eff string) string {
	best := conns.AIHidden
	for _, l := range GrantLevels {
		if conns.AIRank(l) <= conns.AIRank(eff) {
			best = l
		}
	}
	return best
}

// Label 档位中文名。
func Label(l string) string {
	switch l {
	case conns.AIHidden:
		return "不可见"
	case conns.AIRead:
		return "只读"
	case conns.AIWriteConfirm:
		return "读写·逐条确认"
	case conns.AIWriteAuto:
		return "读写·增删改自动（DDL 确认）"
	case conns.AIFull:
		return "完全放开"
	}
	return l
}

// CheckTables 检查语句引用的表都在白名单内。白名单为空时不限；有白名单时，解析不出完整表引用的语句一律拒绝。
// curDB 是语句执行时的当前库。
func (a *ConnAccess) CheckTables(stmt, dialect, curDB string, info sqlclass.Info) error {
	if len(a.Allow) == 0 {
		return nil
	}
	switch info.Keyword {
	case "SHOW", "PRAGMA", "HELP":
		return fail("该连接设置了库表白名单，不能执行 %s；请用 list_databases / list_tables / describe_tables 查看结构", info.Keyword)
	case "USE":
		return nil
	}
	if !info.TablesComplete {
		return fail("该连接设置了库表白名单，而这条语句的表引用无法完整解析（例如表值函数、动态 SQL），已拒绝；请把查询改写成直接引用表的形式")
	}
	ctes := map[string]bool{}
	for _, n := range sqlclass.CTENames(stmt, dialect) {
		ctes[strings.ToLower(n)] = true
	}
	for _, t := range info.Tables {
		if t.Database == "" && ctes[strings.ToLower(t.Table)] {
			continue
		}
		db := t.Database
		if db == "" {
			db = curDB
		}
		if db == "" && dialect == "mysql" {
			return fail("语句里的表 %s 没有指定库，且当前没有选中库；请写成 库.表 或传 db 参数", t.Table)
		}
		if !a.CanSeeTable(db, t.Table) {
			return fail("表 %s.%s 不在该连接的 AI 库表白名单内，智能体不能访问", db, t.Table)
		}
	}
	if info.Kind == sqlclass.Read && len(info.Tables) == 0 && strings.Contains(strings.ToUpper(stmt), "INFORMATION_SCHEMA") {
		return fail("该连接设置了库表白名单，不能直接查询 information_schema")
	}
	return nil
}

// ApprovalRequest 是一次写入审批。
type ApprovalRequest struct {
	Tool        string              `json:"tool"`
	Key         string              `json:"key"` // 「本会话总是允许」的记忆键：工具 + 连接 + 语句类别
	Connection  string              `json:"connection_id"`
	ConnName    string              `json:"connection_name"`
	Driver      string              `json:"driver"`
	Production  bool                `json:"production"`
	Database    string              `json:"db,omitempty"`
	SQL         string              `json:"sql"`
	Class       string              `json:"class"` // dml | ddl | admin | call | other | session
	Keyword     string              `json:"keyword"`
	Tables      []sqlclass.TableRef `json:"tables,omitempty"`
	Dangerous   string              `json:"dangerous,omitempty"`
	Reason      string              `json:"reason"`
	Estimate    string              `json:"estimate,omitempty"` // 预计影响
	RelatedDDL  map[string]string   `json:"related_ddl,omitempty"`
	AILevel     string              `json:"ai_level"`
	Explanation string              `json:"explanation,omitempty"` // 智能体给的写入说明
}

// needsConfirm 按档位判断写语句是否需要确认，返回原因（空 = 自动）。
func needsConfirm(level string, info sqlclass.Info) string {
	switch level {
	case conns.AIWriteConfirm:
		return "该连接的 AI 档位是「读写·逐条确认」"
	case conns.AIWriteAuto:
		if info.Kind == sqlclass.DML {
			if info.Dangerous != "" {
				return "这条修改" + info.Dangerous
			}
			return ""
		}
		return "DDL、管理类语句与存储过程调用需要确认"
	case conns.AIFull:
		return ""
	}
	return "需要确认"
}

// checkWrite 写语句的档位判断与审批。info 已经通过白名单检查。
func (env *Env) checkWrite(ctx context.Context, tool string, a *ConnAccess, stmt, db string, info sqlclass.Info, explanation string, sc driver.SQLConn) error {
	if env.Plan {
		return fail("当前是计划模式（只读分析），不能执行写入或 DDL；请先给出方案，等用户切换回普通模式后再执行")
	}
	if !a.CanWrite() {
		return fail("连接「%s」对智能体是%s档位，不能执行写入语句（%s）", a.Conn.Name, Label(a.AI), info.Keyword)
	}
	reason := needsConfirm(a.AI, info)
	if reason == "" {
		return nil
	}
	switch env.Approval {
	case ApprovalDelegated:
		if a.AI == conns.AIWriteConfirm {
			return fail("连接「%s」要求每条写入都在 Lemi 里确认，MCP 不能执行写入；请在 Lemi 网页里用内置智能体完成", a.Conn.Name)
		}
		return nil // MCP：由外部智能体的确认机制把关
	case ApprovalUnattended:
		return fail("定时任务无人值守，需要确认的写入一律拒绝（%s）", reason)
	}
	if env.Approve == nil {
		return fail("当前环境无法向用户确认写入（%s）", reason)
	}
	req := &ApprovalRequest{Tool: tool, Key: tool + "|" + a.Conn.ID + "|" + string(info.Kind), Connection: a.Conn.ID, ConnName: a.Conn.Name, Driver: a.Conn.Driver,
		Production: a.Conn.Env == "prod", Database: db, SQL: stmt, Class: string(info.Kind), Keyword: info.Keyword, Tables: info.Tables,
		Dangerous: info.Dangerous, Reason: reason, AILevel: a.AI, Explanation: explanation}
	if sc != nil {
		req.Estimate = estimate(ctx, env, a, sc, stmt, db, info)
		if info.Kind == sqlclass.DDL || info.Kind == sqlclass.DML {
			req.RelatedDDL = relatedDDL(ctx, a, sc, db, info)
		}
	}
	return env.Approve(ctx, req)
}

// estimate 预计影响：MySQL 的增删改用 EXPLAIN 估算扫描行数（不执行语句）。
func estimate(ctx context.Context, env *Env, a *ConnAccess, sc driver.SQLConn, stmt, db string, info sqlclass.Info) string {
	if sc.Dialect() != "mysql" || info.Kind != sqlclass.DML || (info.Keyword != "UPDATE" && info.Keyword != "DELETE") {
		return ""
	}
	cs, err := env.Svc.Conns.OpenConsole(ctx, env.User, a.Conn, a.UserLevel, db, env.Source, true, 15)
	if err != nil {
		return ""
	}
	defer env.Svc.Conns.CloseConsole(cs.ID)
	res, err := cs.S.Exec(ctx, "EXPLAIN "+stmt, nil, 50)
	if err != nil || res.Kind != "rows" {
		return ""
	}
	ri := -1
	for i, c := range res.Columns {
		if strings.EqualFold(c.Name, "rows") {
			ri = i
		}
	}
	if ri < 0 {
		return ""
	}
	var total int64
	for _, r := range res.Rows {
		var n int64
		fmt.Sscan(driver.CellText(r[ri]), &n)
		total += n
	}
	return fmt.Sprintf("按执行计划估计扫描约 %d 行（估计值，实际影响行数以执行结果为准）", total)
}

// relatedDDL 取语句涉及的表的当前结构（审批卡片与 DDL 结果里附上）。
func relatedDDL(ctx context.Context, a *ConnAccess, sc driver.SQLConn, db string, info sqlclass.Info) map[string]string {
	out := map[string]string{}
	for i, t := range info.Tables {
		if i >= 5 {
			break
		}
		d := t.Database
		if d == "" {
			d = db
		}
		if sc.Dialect() == "sqlite" && d == "" {
			d = "main"
		}
		if d == "" {
			continue
		}
		ddl, err := sc.DDL(ctx, d, "table", t.Table)
		if err != nil || ddl == "" {
			continue
		}
		if len(ddl) > 8000 {
			ddl = ddl[:8000] + "…"
		}
		out[strings.Trim(d+"."+t.Table, ".")] = ddl
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
