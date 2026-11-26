// Package sqlclass 对单条 SQL 语句做轻量分类：是否只读、属于 DML/DDL/管理语句、引用了哪些表、是否危险（无 WHERE 的 UPDATE/DELETE 等）。
//
// 只读的真正保证在数据库会话层（docs/spec.md §8.3），这里是第二道关：在只读会话里拒绝写文件、改全局变量等
// 只读事务挡不住的语句；也用于智能体的档位判断、库表白名单与审批卡片上的提示。
package sqlclass

import (
	"strings"
	"unicode"
)

// Kind 语句类别。
type Kind string

const (
	Read    Kind = "read"    // SELECT / SHOW / DESCRIBE / EXPLAIN / 只读 PRAGMA
	DML     Kind = "dml"     // INSERT / UPDATE / DELETE / REPLACE / MERGE / LOAD
	DDL     Kind = "ddl"     // CREATE / ALTER / DROP / TRUNCATE / RENAME
	Admin   Kind = "admin"   // GRANT / KILL / SET GLOBAL / FLUSH / 写文件 / ATTACH ...
	Tx      Kind = "tx"      // BEGIN / COMMIT / ROLLBACK / SAVEPOINT
	Session Kind = "session" // USE / SET（会话变量）
	Call    Kind = "call"    // CALL / EXEC（无法判断是否写）
	Other   Kind = "other"
)

// TableRef 是语句引用的表。
type TableRef struct {
	Database string `json:"database,omitempty"`
	Table    string `json:"table"`
}

// Info 是分类结果。
type Info struct {
	Kind      Kind       `json:"kind"`
	Keyword   string     `json:"keyword"` // 主关键字（大写）
	ReturnsRows bool     `json:"returns_rows"`
	Tables    []TableRef `json:"tables,omitempty"`
	Dangerous string     `json:"dangerous,omitempty"` // 危险原因（DROP、TRUNCATE、无 WHERE 的 UPDATE/DELETE）
	// TablesComplete 表引用是否解析完整（含无法解析的结构时为 false，白名单检查应拒绝）
	TablesComplete bool `json:"tables_complete"`
}

// ReadOnlyOK 在只读会话里是否允许执行。
func (i Info) ReadOnlyOK() bool {
	switch i.Kind {
	case Read:
		return true
	case Session:
		return i.Keyword == "USE"
	case Tx:
		return true
	}
	return false
}

// IsWrite 是否会修改数据或结构（含无法判断的 CALL 与管理语句）。
func (i Info) IsWrite() bool {
	switch i.Kind {
	case Read, Tx:
		return false
	case Session:
		return i.Keyword != "USE"
	}
	return true
}

type token struct {
	t    byte   // w=单词 q=引号标识符 s=字符串 n=数字 p=标点
	v    string // 单词为大写原文，标识符为去引号后的名字
	raw  string
	pos  int
}

// tokenize 词法切分：跳过注释（条件注释 /*! */ 的内容当作正文），识别字符串与引号标识符。
func tokenize(sql, dialect string) []token {
	var out []token
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-' && (dialect != "mysql" || i+2 >= n || sql[i+2] == ' ' || sql[i+2] == '\t' || sql[i+2] == '\n' || sql[i+2] == '\r'):
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '#' && dialect == "mysql":
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if dialect == "mysql" && i+2 < n && (sql[i+2] == '!' || (sql[i+2] == 'M' && i+3 < n && sql[i+3] == '!')) {
				// 条件注释：跳过开头标记与版本号，内容按正文处理；结尾的 */ 单独跳过
				j := i + 3
				if sql[i+2] == 'M' {
					j++
				}
				for j < n && sql[j] >= '0' && sql[j] <= '9' {
					j++
				}
				i = j
				continue
			}
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += end + 4
			}
		case c == '*' && i+1 < n && sql[i+1] == '/':
			i += 2
		case c == '\'' || c == '"' || c == '`' || (c == '[' && dialect == "sqlite"):
			q := c
			if c == '[' {
				q = ']'
			}
			j := i + 1
			var b strings.Builder
			for j < n {
				if sql[j] == '\\' && dialect == "mysql" && q != '`' && j+1 < n {
					b.WriteByte(sql[j+1])
					j += 2
					continue
				}
				if sql[j] == q {
					if j+1 < n && sql[j+1] == q && q != ']' {
						b.WriteByte(q)
						j += 2
						continue
					}
					break
				}
				b.WriteByte(sql[j])
				j++
			}
			t := byte('s')
			if c == '`' || c == '[' || (c == '"' && dialect == "sqlite") {
				t = 'q'
			}
			out = append(out, token{t: t, v: b.String(), raw: sql[i:min(j+1, n)], pos: i})
			i = j + 1
		case c == '_' || c == '$' || unicode.IsLetter(rune(c)) || c >= 0x80:
			j := i
			for j < n && (sql[j] == '_' || sql[j] == '$' || sql[j] >= 0x80 || unicode.IsLetter(rune(sql[j])) || unicode.IsDigit(rune(sql[j]))) {
				j++
			}
			w := sql[i:j]
			out = append(out, token{t: 'w', v: strings.ToUpper(w), raw: w, pos: i})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < n && (sql[j] == '.' || sql[j] == 'x' || sql[j] == 'X' || (sql[j] >= '0' && sql[j] <= '9') || (sql[j] >= 'a' && sql[j] <= 'f') || (sql[j] >= 'A' && sql[j] <= 'F')) {
				j++
			}
			out = append(out, token{t: 'n', v: sql[i:j], raw: sql[i:j], pos: i})
			i = j
		default:
			out = append(out, token{t: 'p', v: string(c), raw: string(c), pos: i})
			i++
		}
	}
	return out
}

func isWord(t token, ws ...string) bool {
	if t.t != 'w' {
		return false
	}
	for _, w := range ws {
		if t.v == w {
			return true
		}
	}
	return false
}

// Classify 对单条语句分类。dialect 为 mysql 或 sqlite。
func Classify(sql, dialect string) Info {
	toks := tokenize(sql, dialect)
	// 去掉开头的括号（例如 (SELECT ...) UNION ...）
	k := 0
	for k < len(toks) && toks[k].t == 'p' && toks[k].v == "(" {
		k++
	}
	if k >= len(toks) || toks[k].t != 'w' {
		return Info{Kind: Other, TablesComplete: true}
	}
	kw := toks[k].v
	info := Info{Keyword: kw, TablesComplete: true}
	rest := toks[k+1:]
	has := func(ws ...string) bool {
		for _, t := range rest {
			if isWord(t, ws...) {
				return true
			}
		}
		return false
	}
	switch kw {
	case "SELECT", "VALUES", "TABLE":
		info.Kind, info.ReturnsRows = Read, true
		if hasSeq(rest, "INTO", "OUTFILE") || hasSeq(rest, "INTO", "DUMPFILE") {
			info.Kind, info.ReturnsRows = Admin, false
			info.Dangerous = "写服务器文件"
		}
	case "WITH":
		// 公共表表达式：看主语句是什么
		info.Kind, info.ReturnsRows = Read, true
		if main := cteMain(rest); main != "" && main != "SELECT" {
			info.Keyword = main
			info.Kind, info.ReturnsRows = DML, false
		}
	case "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "HELP", "CHECKSUM", "ANALYZE":
		info.Kind, info.ReturnsRows = Read, true
		if kw == "EXPLAIN" && len(rest) > 0 && isWord(rest[0], "ANALYZE") {
			// EXPLAIN ANALYZE 会真正执行语句：只有被分析的语句只读时才算只读
			sub := Classify(sql[rest[0].pos+len(rest[0].raw):], dialect)
			if sub.Kind != Read {
				info.Kind = sub.Kind
			}
		}
		if kw == "ANALYZE" && dialect == "mysql" {
			info.Kind = Admin // ANALYZE TABLE 会更新统计信息
		}
	case "PRAGMA":
		info.Kind, info.ReturnsRows = Read, true
		for _, t := range rest {
			if t.t == 'p' && t.v == "=" {
				info.Kind, info.ReturnsRows = Admin, false
			}
		}
		if len(rest) > 0 && rest[0].t == 'w' && writePragmas[rest[0].v] {
			info.Kind = Admin
		}
	case "INSERT", "REPLACE", "UPDATE", "DELETE", "MERGE", "UPSERT":
		info.Kind = DML
		if hasWord(rest, "RETURNING") {
			info.ReturnsRows = true
		}
		if (kw == "UPDATE" || kw == "DELETE") && !has("WHERE") {
			info.Dangerous = "没有 WHERE 条件，会影响整张表"
		}
	case "LOAD":
		info.Kind = Admin
		info.Dangerous = "从文件导入"
	case "CREATE", "ALTER", "RENAME", "COMMENT":
		info.Kind = DDL
		if kw == "CREATE" && has("USER") && dialect == "mysql" && isWord(nextWord(rest), "USER") {
			info.Kind = Admin
		}
	case "DROP", "TRUNCATE":
		info.Kind = DDL
		info.Dangerous = "删除对象或清空数据"
		if kw == "DROP" && isWord(nextWord(rest), "USER", "ROLE") {
			info.Kind = Admin
		}
	case "GRANT", "REVOKE", "KILL", "FLUSH", "RESET", "SHUTDOWN", "RESTART", "INSTALL", "UNINSTALL", "PURGE", "CHANGE", "START", "STOP", "LOCK", "UNLOCK", "HANDLER", "CACHE", "OPTIMIZE", "REPAIR", "CHECK", "BINLOG", "CLONE", "IMPORT", "XA", "ATTACH", "DETACH", "VACUUM", "REINDEX":
		info.Kind = Admin
		switch kw {
		case "START":
			if isWord(nextWord(rest), "TRANSACTION") {
				info.Kind = Tx
			}
		case "CHECK":
			info.Kind, info.ReturnsRows = Read, true
		case "VACUUM":
			if hasWord(rest, "INTO") {
				info.Dangerous = "写服务器文件"
			}
		case "ATTACH":
			info.Dangerous = "附加服务器上的其他数据库文件"
		}
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END":
		info.Kind = Tx
	case "USE":
		info.Kind = Session
	case "SET":
		info.Kind = Session
		if hasWord(rest, "GLOBAL", "PERSIST", "PERSIST_ONLY") || hasSeqP(rest, "@", "@", "GLOBAL") || hasWord(rest, "PASSWORD") {
			info.Kind = Admin
		}
		if len(rest) > 0 && isWord(rest[0], "TRANSACTION", "SESSION") && hasWord(rest, "READ") && hasWord(rest, "WRITE") {
			info.Kind = Admin // 只读会话里不允许把事务改回读写
		}
	case "CALL", "EXEC", "EXECUTE", "DO", "PREPARE", "DEALLOCATE":
		info.Kind, info.ReturnsRows = Call, kw == "CALL"
	default:
		info.Kind = Other
	}
	if dialect == "sqlite" {
		for _, t := range toks {
			if t.t == 'w' && t.v == "LOAD_EXTENSION" {
				info.Kind = Admin
				info.Dangerous = "加载扩展"
			}
		}
	}
	info.Tables, info.TablesComplete = tableRefs(toks[k:])
	return info
}

var writePragmas = map[string]bool{"WRITABLE_SCHEMA": true, "JOURNAL_MODE": true, "WAL_CHECKPOINT": true, "INCREMENTAL_VACUUM": true, "OPTIMIZE": true, "SHRINK_MEMORY": true}

func nextWord(ts []token) token {
	for _, t := range ts {
		if t.t == 'w' {
			if t.v == "TEMPORARY" || t.v == "OR" || t.v == "REPLACE" || t.v == "IF" || t.v == "NOT" || t.v == "EXISTS" {
				continue
			}
			return t
		}
	}
	return token{}
}

func hasWord(ts []token, ws ...string) bool {
	for _, t := range ts {
		if isWord(t, ws...) {
			return true
		}
	}
	return false
}

func hasSeq(ts []token, a, b string) bool {
	for i := 0; i+1 < len(ts); i++ {
		if isWord(ts[i], a) && isWord(ts[i+1], b) {
			return true
		}
	}
	return false
}

func hasSeqP(ts []token, a, b, c string) bool {
	for i := 0; i+2 < len(ts); i++ {
		if ts[i].v == a && ts[i+1].v == b && strings.EqualFold(ts[i+2].v, c) {
			return true
		}
	}
	return false
}

// cteMain 找 WITH ... AS (...) 之后的主语句关键字。
func cteMain(ts []token) string {
	depth := 0
	for _, t := range ts {
		if t.t == 'p' {
			switch t.v {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth == 0 && t.t == 'w' {
			switch t.v {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE", "TABLE", "VALUES":
				return t.v
			}
		}
	}
	return ""
}

// tableRefs 粗略提取表引用：FROM / JOIN / UPDATE / INTO / TABLE / DESCRIBE 之后的名字（逗号分隔列表也算）。
// 子查询跳过；遇到解析不了的情形返回 complete=false。
func tableRefs(ts []token) ([]TableRef, bool) {
	var refs []TableRef
	complete := true
	seen := map[string]bool{}
	add := func(db, t string) {
		key := strings.ToLower(db + "." + t)
		if t == "" || seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, TableRef{Database: db, Table: t})
	}
	readName := func(i int) (string, string, int, bool) {
		if i >= len(ts) {
			return "", "", i, false
		}
		name := func(t token) (string, bool) {
			if t.t == 'q' {
				return t.v, true
			}
			if t.t == 'w' {
				return t.raw, true
			}
			return "", false
		}
		a, ok := name(ts[i])
		if !ok {
			return "", "", i, false
		}
		if i+2 < len(ts) && ts[i+1].t == 'p' && ts[i+1].v == "." {
			if b, ok := name(ts[i+2]); ok {
				return a, b, i + 3, true
			}
		}
		return "", a, i + 1, true
	}
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.t != 'w' {
			continue
		}
		switch t.v {
		case "FROM", "JOIN", "INTO", "UPDATE", "TABLE", "DESCRIBE", "DESC", "TRUNCATE":
			if t.v == "TABLE" && i > 0 && isWord(ts[i-1], "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE", "LOCK", "CHECK", "OPTIMIZE", "REPAIR", "ANALYZE", "CHECKSUM") {
				// 结构语句：TABLE 后面是表名
			} else if t.v == "TABLE" && i > 0 {
				continue
			}
			j := i + 1
			for j < len(ts) && isWord(ts[j], "IF", "NOT", "EXISTS", "IGNORE", "LOW_PRIORITY", "QUICK", "ONLY", "LATERAL", "TEMPORARY") {
				j++
			}
			for {
				if j < len(ts) && ts[j].t == 'p' && ts[j].v == "(" {
					// 子查询或派生表：内部的 FROM 会在后续循环里再被识别
					break
				}
				if j < len(ts) && isWord(ts[j], "SELECT", "WITH", "VALUES", "DUAL") {
					break
				}
				if j < len(ts) && ts[j].t == 'w' && reserved[ts[j].v] {
					break
				}
				db, tb, next, ok := readName(j)
				if !ok {
					if t.v == "FROM" || t.v == "JOIN" {
						complete = false
					}
					break
				}
				add(db, tb)
				j = next
				// 跳过别名
				if j < len(ts) && isWord(ts[j], "AS") {
					j++
				}
				if j < len(ts) && (ts[j].t == 'q' || (ts[j].t == 'w' && !reserved[ts[j].v])) {
					j++
				}
				if j < len(ts) && ts[j].t == 'p' && ts[j].v == "," && (t.v == "FROM" || t.v == "UPDATE" || t.v == "TRUNCATE" || t.v == "TABLE") {
					j++
					continue
				}
				break
			}
		}
	}
	return refs, complete
}

var reserved = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`SELECT FROM WHERE GROUP ORDER BY HAVING LIMIT OFFSET JOIN INNER LEFT RIGHT FULL OUTER CROSS NATURAL ON USING
		UNION ALL EXCEPT INTERSECT SET VALUES VALUE INTO AS AND OR NOT NULL IS IN EXISTS BETWEEN LIKE CASE WHEN THEN ELSE END
		FOR UPDATE DELETE INSERT REPLACE WINDOW PARTITION LOCK SHARE NOWAIT SKIP STRAIGHT_JOIN FORCE USE IGNORE INDEX KEY
		RETURNING DEFAULT DUPLICATE WITH RECURSIVE LATERAL TABLE`) {
		m[w] = true
	}
	return m
}()

// SelectCol 是简单查询里的一列。
type SelectCol struct {
	Star   bool   // *
	Name   string // 原始列名
	Alias  string // 结果列名
}

// SimpleSelect 是「单表、无聚合」的查询，结果可以回写（§9.4）。
type SimpleSelect struct {
	Database string
	Table    string
	Cols     []SelectCol
}

// ParseSimpleSelect 识别 SELECT 列 FROM 单表 [WHERE …] [ORDER BY …] [LIMIT …] 形式。
// 有 JOIN、子查询、GROUP BY、DISTINCT、UNION、聚合或表达式列时返回 false 与原因。
func ParseSimpleSelect(sql, dialect string) (*SimpleSelect, string) {
	ts := tokenize(sql, dialect)
	if len(ts) == 0 || !isWord(ts[0], "SELECT") {
		return nil, "不是 SELECT 语句"
	}
	for _, t := range ts {
		if isWord(t, "JOIN", "UNION", "GROUP", "DISTINCT", "HAVING", "INTERSECT", "EXCEPT", "WINDOW", "OVER") {
			return nil, "查询包含 " + t.v + "，结果不能直接修改"
		}
	}
	// 找顶层 FROM
	depth, from := 0, -1
	for i := 1; i < len(ts); i++ {
		if ts[i].t == 'p' {
			if ts[i].v == "(" {
				depth++
			} else if ts[i].v == ")" {
				depth--
			}
			continue
		}
		if depth == 0 && isWord(ts[i], "FROM") {
			from = i
			break
		}
	}
	if from < 0 {
		return nil, "查询没有 FROM"
	}
	for i := from; i < len(ts); i++ {
		if ts[i].t == 'p' && (ts[i].v == "(" || ts[i].v == ",") {
			if i == from+1 || (ts[i].v == "," && depth == 0) {
				return nil, "查询涉及多张表或子查询，结果不能直接修改"
			}
		}
		if isWord(ts[i], "SELECT") {
			return nil, "查询包含子查询，结果不能直接修改"
		}
	}
	sel := &SimpleSelect{}
	// 表名
	j := from + 1
	name := func(t token) (string, bool) {
		if t.t == 'q' {
			return t.v, true
		}
		if t.t == 'w' && !reserved[t.v] {
			return t.raw, true
		}
		return "", false
	}
	if j >= len(ts) {
		return nil, "查询没有表名"
	}
	a, ok := name(ts[j])
	if !ok {
		return nil, "无法识别查询的表"
	}
	if j+2 < len(ts) && ts[j+1].v == "." {
		b, ok := name(ts[j+2])
		if !ok {
			return nil, "无法识别查询的表"
		}
		sel.Database, sel.Table = a, b
	} else {
		sel.Table = a
	}
	// 列
	var cur []token
	flush := func() bool {
		if len(cur) == 0 {
			return false
		}
		c := SelectCol{}
		switch {
		case len(cur) == 1 && cur[0].t == 'p' && cur[0].v == "*":
			c.Star = true
		case len(cur) == 3 && cur[1].v == "." && cur[2].v == "*":
			c.Star = true
		default:
			k := 0
			if len(cur) >= 3 && cur[1].v == "." { // t.col
				k = 2
			}
			n, ok := name(cur[k])
			if !ok {
				return false
			}
			c.Name, c.Alias = n, n
			rest := cur[k+1:]
			if len(rest) > 0 && isWord(rest[0], "AS") {
				rest = rest[1:]
			}
			if len(rest) == 1 {
				if al, ok := name(rest[0]); ok {
					c.Alias = al
				} else if rest[0].t == 's' {
					c.Alias = rest[0].v
				} else {
					return false
				}
			} else if len(rest) > 1 {
				return false
			}
		}
		sel.Cols = append(sel.Cols, c)
		cur = nil
		return true
	}
	for i := 1; i < from; i++ {
		if ts[i].t == 'p' && ts[i].v == "," {
			if !flush() {
				return nil, "结果里有表达式列，不能直接修改"
			}
			continue
		}
		if ts[i].t == 'p' && ts[i].v == "(" {
			return nil, "结果里有函数或表达式列，不能直接修改"
		}
		cur = append(cur, ts[i])
	}
	if !flush() {
		return nil, "结果里有表达式列，不能直接修改"
	}
	return sel, ""
}
