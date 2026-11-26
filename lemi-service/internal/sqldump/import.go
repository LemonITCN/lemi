package sqldump

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"modernc.org/sqlite"

	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
)

const (
	defaultCommitEvery = 1000
	commitBytes        = 16 << 20
	snippetRunes       = 200
)

// ErrIncomplete 表示文件被截断（Lemi 或 mysqldump 文件没有结束标记）。
var ErrIncomplete = errors.New("文件不完整：没有结束标记，可能被截断")

// tailReader 统计读到的字节并保留最后 512 字节，用来检查结束标记。
type tailReader struct {
	r    io.Reader
	n    int64
	tail []byte
}

func (t *tailReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.n += int64(n)
		t.tail = append(t.tail, p[:n]...)
		if len(t.tail) > 1024 {
			t.tail = append(t.tail[:0], t.tail[len(t.tail)-512:]...)
		}
	}
	return n, err
}

func (t *tailReader) lastLine() string {
	s := strings.TrimRight(string(t.tail), " \t\r\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// detectFormat 根据文件开头判断来源。
func detectFormat(head []byte) string {
	s := string(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")))
	s = strings.TrimLeft(s, " \t\r\n")
	if strings.HasPrefix(s, "/*M!999999") { // mariadb-dump 的沙盒模式行
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimLeft(s[i+1:], " \t\r\n")
		}
	}
	if strings.HasPrefix(s, "/*") {
		end := strings.Index(s, "*/")
		if end < 0 {
			end = len(s)
		}
		c := s[:end]
		first := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(c, "/*"), "\n", 3)[0])
		if first == "" {
			if parts := strings.SplitN(strings.TrimPrefix(c, "/*"), "\n", 3); len(parts) > 1 {
				first = strings.TrimSpace(parts[1])
			}
		}
		if first == headerTitle {
			return "lemi"
		}
		if strings.Contains(c, "Navicat") {
			return "navicat"
		}
	}
	if strings.HasPrefix(s, "-- MySQL dump") || strings.HasPrefix(s, "-- MariaDB dump") {
		return "mysqldump"
	}
	return "unknown"
}

// ---------- 记号 ----------

type tok struct {
	s          string // 关键字（大写）或标识符（去掉引号）
	quoted     bool
	start, end int
}

// leadTokens 取语句开头的若干记号；跳过注释，MySQL 条件注释的标记透明处理。
func leadTokens(text string, max int, mysqlMode bool) []tok {
	var out []tok
	i := 0
	for i < len(text) && len(out) < max {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#' && mysqlMode, c == '-' && i+1 < len(text) && text[i+1] == '-':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			if mysqlMode && i+2 < len(text) && (text[i+2] == '!' || (text[i+2] == 'M' && i+3 < len(text) && text[i+3] == '!')) {
				i += 3
				if text[i-1] == 'M' {
					i++
				}
				for i < len(text) && text[i] >= '0' && text[i] <= '9' {
					i++
				}
				continue
			}
			j := strings.Index(text[i+2:], "*/")
			if j < 0 {
				return out
			}
			i += j + 4
		case c == '*' && mysqlMode && i+1 < len(text) && text[i+1] == '/':
			i += 2
		case c == '`' || c == '"' || (c == '[' && !mysqlMode):
			end := c
			if c == '[' {
				end = ']'
			}
			var b strings.Builder
			j := i + 1
			for j < len(text) {
				if text[j] == end {
					if end != ']' && j+1 < len(text) && text[j+1] == end {
						b.WriteByte(end)
						j += 2
						continue
					}
					break
				}
				b.WriteByte(text[j])
				j++
			}
			out = append(out, tok{s: b.String(), quoted: true, start: i, end: min(j+1, len(text))})
			i = j + 1
		case c == '_' || c == '$' || c >= 0x80 || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z':
			j := i
			for j < len(text) {
				d := text[j]
				if d == '_' || d == '$' || d >= 0x80 || d >= '0' && d <= '9' || d|0x20 >= 'a' && d|0x20 <= 'z' {
					j++
					continue
				}
				break
			}
			out = append(out, tok{s: strings.ToUpper(text[i:j]), start: i, end: j})
			i = j
		default:
			out = append(out, tok{s: string(c), start: i, end: i + 1})
			i++
		}
	}
	return out
}

func (t tok) is(kw string) bool { return !t.quoted && t.s == kw }

// identOf 取标识符原文（未加引号的保留原大小写）。
func identOf(text string, t tok) string {
	if t.quoted {
		return t.s
	}
	return text[t.start:t.end]
}

// dbNameToken 找出 CREATE/DROP/ALTER DATABASE 与 USE 语句里的库名记号。
func dbNameToken(ts []tok) (tok, bool) {
	if len(ts) >= 2 && ts[0].is("USE") {
		return ts[1], true
	}
	if len(ts) < 3 || !(ts[1].is("DATABASE") || ts[1].is("SCHEMA")) {
		return tok{}, false
	}
	i := 2
	switch {
	case ts[0].is("CREATE"):
		if len(ts) > i+2 && ts[i].is("IF") && ts[i+1].is("NOT") && ts[i+2].is("EXISTS") {
			i += 3
		}
	case ts[0].is("DROP"):
		if len(ts) > i+1 && ts[i].is("IF") && ts[i+1].is("EXISTS") {
			i += 2
		}
	case ts[0].is("ALTER"):
		if i < len(ts) && !ts[i].quoted {
			switch ts[i].s {
			case "CHARACTER", "DEFAULT", "COLLATE", "CHARSET", "READ", "ENCRYPTION", "COMMENT", "UPGRADE":
				return tok{}, false
			}
		}
	default:
		return tok{}, false
	}
	if i >= len(ts) {
		return tok{}, false
	}
	t := ts[i]
	if !t.quoted && (t.s == "" || !isWordByte(t.s[0])) {
		return tok{}, false
	}
	return t, true
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}

// tableOf 从语句开头猜当前表（只用于进度显示）。
func tableOf(text string, ts []tok) string {
	n := len(ts)
	for i := 0; i < n; i++ {
		t := ts[i]
		if t.is("TABLE") || t.is("INTO") || t.is("TABLES") {
			j := i + 1
			for j < n && (ts[j].is("IF") || ts[j].is("NOT") || ts[j].is("EXISTS")) {
				j++
			}
			if j < n {
				name := identOf(text, ts[j])
				if j+2 < n && ts[j+1].s == "." {
					name = identOf(text, ts[j+2])
				}
				return name
			}
		}
	}
	return ""
}

func snippet(s string) string {
	if utf8.RuneCountInString(s) <= snippetRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == snippetRunes {
			return s[:i]
		}
		n++
	}
	return s
}

// ---------- 错误分类 ----------

// fatalErr 判断错误是否必须停止导入（连接断开、取消、死锁回滚整个事务等），不能当成单条语句失败跳过。
func fatalErr(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1040, 1041, 1042, 1043, 1053, 1152, 1153, 1154, 1155, 1156, 1157, 1158, 1159, 1160, 1161,
			1180, 1181, 1182, 1183, 1205, 1213, 1317, 1927, 2006, 2013, 3024:
			return true
		}
		return false
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case 5, 6, 7, 8, 9, 10, 11, 13, 14, 26:
			return true
		}
		return false
	}
	return true
}

// ---------- 导入 ----------

type importer struct {
	ctx   context.Context
	conn  *sql.Conn
	opts  ImportOptions
	mysql bool
	res   *ImportResult
	sp    *sqlsplit.Splitter

	inFileTx   bool // 文件自己开了事务
	autoTx     bool // SQLite：我们开的批量事务
	pending    int
	pendingLen int
	table      string
	lastProg   time.Time
}

// Import 执行 SQL 文件。返回的 ImportResult 在出错时也有效（记录已执行的部分）。
func Import(ctx context.Context, conn *sql.Conn, r io.Reader, opts ImportOptions) (*ImportResult, error) {
	if opts.Dialect != MySQL && opts.Dialect != SQLite {
		return nil, fmt.Errorf("不支持的方言 %q", opts.Dialect)
	}
	if opts.CommitEvery <= 0 {
		opts.CommitEvery = defaultCommitEvery
	}
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(4096)
	res := &ImportResult{Format: detectFormat(head)}
	res.LemiDump = res.Format == "lemi"
	tr := &tailReader{r: br}
	im := &importer{ctx: ctx, conn: conn, opts: opts, mysql: opts.Dialect == MySQL, res: res}

	split := sqlsplit.SQLite
	if im.mysql {
		split = sqlsplit.MySQL
		saved, err := saveMySession(ctx, conn)
		if err != nil {
			return res, isCanceled(ctx, err)
		}
		defer saved.restore(conn)
		var fk, uc sql.NullInt64
		_ = conn.QueryRowContext(ctx, "SELECT @@session.foreign_key_checks, @@session.unique_checks").Scan(&fk, &uc)
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if fk.Valid {
				_, _ = conn.ExecContext(c, fmt.Sprintf("SET SESSION foreign_key_checks = %d", fk.Int64))
			}
			if uc.Valid {
				_, _ = conn.ExecContext(c, fmt.Sprintf("SET SESSION unique_checks = %d", uc.Int64))
			}
		}()
		for _, q := range []string{"SET NAMES utf8mb4", "SET SESSION autocommit = 0"} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				return res, isCanceled(ctx, fmt.Errorf("%s: %w", q, err))
			}
		}
		if opts.TargetDatabase != "" {
			if _, err := conn.ExecContext(ctx, "USE "+quoteMySQLIdent(opts.TargetDatabase)); err != nil {
				return res, isCanceled(ctx, fmt.Errorf("切换到目标库 %s: %w", opts.TargetDatabase, err))
			}
		}
	}
	im.sp = sqlsplit.NewSplitter(tr, split)
	if im.mysql {
		im.syncBackslash()
	}

	err := im.run()
	res.Bytes = im.sp.Offset()
	if err != nil {
		im.abort(err)
		im.progress(true)
		return res, err
	}
	if err := im.commit(); err != nil {
		return res, isCanceled(ctx, err)
	}
	switch res.Format {
	case "lemi":
		res.Complete = strings.HasPrefix(tr.lastLine(), endMarkerPrefix)
	case "mysqldump":
		res.Complete = strings.HasPrefix(tr.lastLine(), "-- Dump completed")
	default:
		res.Complete = true
	}
	im.progress(true)
	if !res.Complete {
		return res, ErrIncomplete
	}
	return res, nil
}

// syncBackslash 按会话当前 sql_mode 决定切分器是否识别反斜杠转义。
func (im *importer) syncBackslash() {
	var mode string
	if err := im.conn.QueryRowContext(im.ctx, "SELECT @@session.sql_mode").Scan(&mode); err == nil {
		im.sp.SetNoBackslashEscapes(strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES"))
	}
}

func (im *importer) progress(force bool) {
	if im.opts.Progress == nil {
		return
	}
	now := time.Now()
	if !force && now.Sub(im.lastProg) < 200*time.Millisecond {
		return
	}
	im.lastProg = now
	im.opts.Progress(ImportProgress{
		Bytes: im.sp.Offset(), TotalBytes: im.opts.TotalBytes,
		Statements: im.res.Statements, Errors: im.res.Errors, Table: im.table,
	})
}

func (im *importer) exec(q string) error {
	_, err := im.conn.ExecContext(im.ctx, q)
	return err
}

// commit 提交我们开的批量事务（MySQL autocommit=0 时的隐式事务；SQLite 的 BEGIN）。
func (im *importer) commit() error {
	im.pending, im.pendingLen = 0, 0
	if im.mysql {
		return im.exec("COMMIT")
	}
	if im.autoTx {
		im.autoTx = false
		return im.exec("COMMIT")
	}
	return nil
}

// abort 在停止导入时收尾：文件自己的事务回滚（与 mysql 客户端断开时一致），我们的批量事务提交（等同逐条自动提交）；
// 致命错误一律回滚。
func (im *importer) abort(err error) {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var se *stmtFailure
	if errors.As(err, &se) && !se.fatal && !im.inFileTx {
		if im.mysql || im.autoTx {
			_, _ = im.conn.ExecContext(c, "COMMIT")
		}
		return
	}
	_, _ = im.conn.ExecContext(c, "ROLLBACK")
}

type stmtFailure struct {
	e     StmtError
	fatal bool
	err   error
}

func (f *stmtFailure) Error() string {
	return fmt.Sprintf("第 %d 条语句（第 %d 行）出错: %s", f.e.Index, f.e.Line, f.e.Error)
}

func (f *stmtFailure) Unwrap() error { return f.err }

func (im *importer) run() error {
	res := im.res
	for {
		if err := im.ctx.Err(); err != nil {
			return err
		}
		st, err := im.sp.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取 SQL 文件: %w", err)
		}
		text := st.Text
		ts := leadTokens(text, 8, im.mysql)
		if len(ts) == 0 {
			continue // 只有条件注释标记之类
		}
		if t := tableOf(text, ts); t != "" {
			im.table = t
		}
		kw := ts[0].s
		if ts[0].quoted {
			kw = ""
		}

		if im.mysql {
			if t, ok := dbNameToken(ts); ok && len(im.opts.DatabaseMap) > 0 {
				if to, mapped := im.opts.DatabaseMap[identOf(text, t)]; mapped {
					text = text[:t.start] + quoteMySQLIdent(to) + text[t.end:]
				}
			}
		} else {
			if skip, bad := im.sqliteFilter(ts); bad != "" {
				res.Statements++
				if ferr := im.fail(st, res.Statements, errors.New(bad), false); ferr != nil {
					return ferr
				}
				continue
			} else if skip {
				res.Skipped++
				continue
			}
		}

		control := false
		switch kw {
		case "BEGIN":
			control = len(ts) == 1 || ts[1].is("WORK") || ts[1].is("TRANSACTION") || ts[1].is("DEFERRED") || ts[1].is("IMMEDIATE") || ts[1].is("EXCLUSIVE")
		case "START":
			control = len(ts) > 1 && ts[1].is("TRANSACTION")
		case "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
			control = kw != "END" || !im.mysql
		case "PRAGMA", "VACUUM":
			control = !im.mysql
		}
		if !im.mysql && control && im.autoTx {
			if err := im.commit(); err != nil {
				return isCanceled(im.ctx, err)
			}
		}
		if !im.mysql && !control && !im.inFileTx && !im.autoTx {
			if err := im.exec("BEGIN"); err != nil {
				return isCanceled(im.ctx, err)
			}
			im.autoTx = true
		}

		res.Statements++
		if err := im.exec(text); err != nil {
			if cerr := im.ctx.Err(); cerr != nil {
				return cerr
			}
			if ferr := im.fail(st, res.Statements, err, fatalErr(err)); ferr != nil {
				return ferr
			}
		} else if control {
			switch kw {
			case "BEGIN", "START":
				im.inFileTx = true
			case "COMMIT", "END":
				im.inFileTx = false
				im.pending, im.pendingLen = 0, 0
			case "ROLLBACK":
				if len(ts) < 2 || !ts[1].is("TO") {
					im.inFileTx = false
					im.pending, im.pendingLen = 0, 0
				}
			}
		}
		if im.mysql && kw == "SET" && strings.Contains(strings.ToUpper(text), "SQL_MODE") {
			im.syncBackslash()
		}

		if !control {
			im.pending++
			im.pendingLen += len(text)
			if !im.inFileTx && (im.pending >= im.opts.CommitEvery || im.pendingLen >= commitBytes) {
				if err := im.commit(); err != nil {
					return isCanceled(im.ctx, err)
				}
			}
		}
		im.progress(false)
	}
}

// fail 记录一条语句错误；需要停止时返回错误。
func (im *importer) fail(st sqlsplit.Statement, idx int64, err error, fatal bool) error {
	res := im.res
	res.Errors++
	e := StmtError{Index: idx, Line: st.StartLine, Snippet: snippet(st.Text), Error: err.Error()}
	if len(res.ErrorList) < maxErrorList {
		res.ErrorList = append(res.ErrorList, e)
	}
	if fatal || !im.opts.ContinueOnError {
		return &stmtFailure{e: e, fatal: fatal, err: err}
	}
	return nil
}

// sqliteFilter：Navicat 的 SQLite 文件会重建 sqlite_sequence 等内部表（SQLite 不允许），跳过；
// ATTACH、DETACH、VACUUM INTO 一律拒绝（不能借导入读写白名单以外的文件）。
func (im *importer) sqliteFilter(ts []tok) (skip bool, bad string) {
	kw := ts[0].s
	if ts[0].quoted {
		return false, ""
	}
	switch kw {
	case "ATTACH", "DETACH":
		return false, "导入文件里不允许 " + kw + " 语句"
	case "VACUUM":
		for _, t := range ts[1:] {
			if t.is("INTO") {
				return false, "导入文件里不允许 VACUUM INTO"
			}
		}
	case "CREATE", "DROP":
		i := 1
		if i < len(ts) && (ts[i].is("TEMP") || ts[i].is("TEMPORARY")) {
			i++
		}
		if i < len(ts) && ts[i].is("TABLE") {
			i++
			for i < len(ts) && (ts[i].is("IF") || ts[i].is("NOT") || ts[i].is("EXISTS")) {
				i++
			}
			if i < len(ts) {
				name := strings.ToLower(ts[i].s)
				if i+2 < len(ts) && ts[i+1].s == "." {
					name = strings.ToLower(ts[i+2].s)
				}
				if strings.HasPrefix(name, "sqlite_") {
					return true, ""
				}
			}
		}
	}
	return false, ""
}
