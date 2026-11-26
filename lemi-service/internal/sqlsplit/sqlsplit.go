// Package sqlsplit 把 SQL 文本切成一条条语句，MySQL（含 MariaDB）与 SQLite 两种方言。
//
// 控制台用 Split 找光标所在语句，导入器用 Splitter 流式切分任意大的 .sql 文件（内存只与单条语句大小有关）。
// 前端 TypeScript 版本与本包共用 testdata/cases.json 里的用例，改规则时两边一起改。
//
// 规则：
//   - 字符串与标识符：单引号、双引号（MySQL 支持反斜杠转义，NO_BACKSLASH_ESCAPES 时不支持；两种方言都支持引号双写），
//     反引号（只支持双写），SQLite 另有 [方括号标识符]。其中的分隔符、注释符号都不起作用。
//   - 注释：`/* */`；`--`（MySQL 要求后面跟空白、控制字符或到达末尾，SQLite 不要求）；`#`（仅 MySQL）。
//     语句前后的注释与空白不属于语句，语句中间的注释原样保留。只有注释与空白的片段丢弃。
//   - MySQL 条件注释 `/*! ... */`、`/*!40101 ... */`、MariaDB 的 `/*M!100100 ... */` 是代码，原样保留在语句里交给服务器按版本处理；
//     其内部照常识别字符串与注释，内部的分隔符不切断语句。
//   - MySQL 的 DELIMITER 指令（大小写不限，必须出现在语句开头，后跟空格或制表符和新分隔符，作用到行尾）
//     是客户端指令，不产出语句，只改变后续分隔符。Navicat 用 `delimiter ;;` … `;;` … `delimiter ;`，mysqldump 用 `DELIMITER ;;`。
//   - MySQL 不猜 BEGIN ... END：没有用 DELIMITER 改分隔符时，存储过程体里的分号照样切断，和官方 mysql 客户端一致。
//   - SQLite 的 CREATE [TEMP] TRIGGER ... BEGIN ... END; 按 sqlite3_complete 的状态机处理，触发器体内的分号不切断。
//   - 文件开头的 UTF-8 BOM 当作空白。
package sqlsplit

import (
	"errors"
	"io"
	"strings"
)

// Dialect 是 SQL 方言。
type Dialect string

const (
	MySQL  Dialect = "mysql"
	SQLite Dialect = "sqlite"
)

// DefaultMaxStatementBytes 是单条语句的默认上限（与 MySQL max_allowed_packet 的最大值相同）。
const DefaultMaxStatementBytes = 1 << 30

// ErrStatementTooLarge 表示单条语句超过了 Options.MaxStatementBytes（多半是引号没闭合）。
var ErrStatementTooLarge = errors.New("单条语句超过长度上限（可能有未闭合的引号或注释）")

// Options 是切分选项。
type Options struct {
	// NoBackslashEscapes 对应 MySQL 的 sql_mode NO_BACKSLASH_ESCAPES：字符串里的反斜杠是普通字符。SQLite 忽略。
	NoBackslashEscapes bool
	// MaxStatementBytes 单条语句上限，0 = DefaultMaxStatementBytes。
	MaxStatementBytes int
}

// Statement 是切出的一条语句。
type Statement struct {
	// Text 从第一个有效字符到最后一个有效字符的原文，不含分隔符、不含前后的注释与空白。
	Text string
	// StartLine 是 Text 第一个字符所在行（从 1 开始）。
	StartLine int
	// StartOffset、EndOffset 是 Text 在原文中的字节偏移，原文[StartOffset:EndOffset] == Text。
	StartOffset, EndOffset int64
	// Terminated 为真表示语句以分隔符结束；为假表示读到末尾也没遇到分隔符（最后一条没写分号，或引号、注释未闭合）。
	Terminated bool
}

// SQLite 触发器状态机（与 sqlite3_complete 相同）。
const (
	stInvalid = iota
	stStart
	stNormal
	stExplain
	stCreate
	stTrigger
	stSemi
	stEnd
)

const (
	tkSemi = iota
	tkWS
	tkOther
	tkExplain
	tkCreate
	tkTemp
	tkTrigger
	tkEnd
)

var trans = [8][8]uint8{
	/*               SEMI  WS  OTHER EXPLAIN CREATE TEMP TRIGGER END */
	/* INVALID */ {stStart, stInvalid, stNormal, stExplain, stCreate, stNormal, stNormal, stNormal},
	/* START   */ {stStart, stStart, stNormal, stExplain, stCreate, stNormal, stNormal, stNormal},
	/* NORMAL  */ {stStart, stNormal, stNormal, stNormal, stNormal, stNormal, stNormal, stNormal},
	/* EXPLAIN */ {stStart, stExplain, stExplain, stNormal, stCreate, stNormal, stNormal, stNormal},
	/* CREATE  */ {stStart, stCreate, stNormal, stNormal, stNormal, stCreate, stTrigger, stNormal},
	/* TRIGGER */ {stSemi, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger},
	/* SEMI    */ {stSemi, stSemi, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger, stEnd},
	/* END     */ {stStart, stEnd, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger, stTrigger},
}

const minRead = 32 << 10

// Splitter 流式切分语句。不是并发安全的。
type Splitter struct {
	r       io.Reader
	dialect Dialect
	opt     Options

	buf  []byte
	pos  int   // buf 中下一个未处理字节
	base int64 // buf[0] 的绝对偏移
	line int   // pos 所在行
	eof  bool
	err  error

	delim  string
	inCond bool  // 在 MySQL 条件注释内
	trig   uint8 // SQLite 触发器状态

	stmtStart int64 // 当前语句第一个有效字节的绝对偏移，-1 = 还没开始
	stmtLine  int
	lastSig   int64 // 当前语句最后一个有效字节之后的绝对偏移
}

// NewSplitter 创建流式切分器，Next 依次返回语句，结束返回 io.EOF。
func NewSplitter(r io.Reader, d Dialect) *Splitter {
	return NewSplitterOptions(r, d, Options{})
}

// NewSplitterOptions 同 NewSplitter，带选项。
func NewSplitterOptions(r io.Reader, d Dialect, o Options) *Splitter {
	if o.MaxStatementBytes <= 0 {
		o.MaxStatementBytes = DefaultMaxStatementBytes
	}
	if d != SQLite {
		d = MySQL
	}
	return &Splitter{r: r, dialect: d, opt: o, line: 1, delim: ";", trig: stInvalid, stmtStart: -1}
}

// SetNoBackslashEscapes 在两条语句之间切换反斜杠转义（导入器执行了改 sql_mode 的语句后调用）。
func (s *Splitter) SetNoBackslashEscapes(v bool) { s.opt.NoBackslashEscapes = v }

// Delimiter 返回当前分隔符（MySQL 可被 DELIMITER 指令改变）。
func (s *Splitter) Delimiter() string { return s.delim }

// Offset 返回已经处理到的绝对字节偏移。
func (s *Splitter) Offset() int64 { return s.base + int64(s.pos) }

// Split 一次切分整段文本（控制台用）。偏移是 src 的字节偏移。
func Split(src string, d Dialect) []Statement {
	return SplitOptions(src, d, Options{})
}

// SplitOptions 同 Split，带选项。
func SplitOptions(src string, d Dialect, o Options) []Statement {
	if o.MaxStatementBytes <= 0 {
		o.MaxStatementBytes = len(src) + 1
	}
	sp := NewSplitterOptions(strings.NewReader(src), d, o)
	var out []Statement
	for {
		st, err := sp.Next()
		if err != nil {
			return out
		}
		out = append(out, st)
	}
}

// fill 再读一块数据；丢弃已经不需要的前缀。到末尾返回 false。
func (s *Splitter) fill() bool {
	if s.eof {
		return false
	}
	keep := s.pos
	if s.stmtStart >= 0 {
		if k := int(s.stmtStart - s.base); k < keep {
			keep = k
		}
	}
	if keep > 0 && (keep >= len(s.buf)/2 || cap(s.buf)-len(s.buf) < minRead) {
		n := copy(s.buf, s.buf[keep:])
		s.buf = s.buf[:n]
		s.base += int64(keep)
		s.pos -= keep
	}
	if cap(s.buf)-len(s.buf) < minRead {
		if s.stmtStart >= 0 && len(s.buf) > s.opt.MaxStatementBytes+64 {
			s.err = ErrStatementTooLarge
			s.eof = true
			return false
		}
		nb := make([]byte, len(s.buf), 2*cap(s.buf)+minRead)
		copy(nb, s.buf)
		s.buf = nb
	}
	for {
		n, err := s.r.Read(s.buf[len(s.buf):cap(s.buf)])
		s.buf = s.buf[:len(s.buf)+n]
		if err != nil {
			s.eof = true
			if !errors.Is(err, io.EOF) {
				s.err = err
			}
			return n > 0
		}
		if n > 0 {
			return true
		}
	}
}

// ensure 保证 pos 之后至少有 n 个字节可读；到末尾也凑不够返回 false。
func (s *Splitter) ensure(n int) bool {
	for len(s.buf)-s.pos < n {
		if !s.fill() && len(s.buf)-s.pos < n {
			return false
		}
	}
	return true
}

// at 返回 pos+i 处的字节；没有返回 0,false。
func (s *Splitter) at(i int) (byte, bool) {
	if !s.ensure(i + 1) {
		return 0, false
	}
	return s.buf[s.pos+i], true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isIdent(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

// mark 在当前语句还没开始时把 start（buf 下标）记为语句开头。必须在扫描记号之前调用，
// 这样读入更多数据时不会把语句开头丢掉。
func (s *Splitter) mark(start, line int) {
	if s.stmtStart < 0 {
		s.stmtStart = s.base + int64(start)
		s.stmtLine = line
	}
}

// sig 记录当前语句最后一个有效字节之后的位置。
func (s *Splitter) sig() { s.lastSig = s.base + int64(s.pos) }

func (s *Splitter) emit(terminated bool) Statement {
	a := int(s.stmtStart - s.base)
	b := int(s.lastSig - s.base)
	st := Statement{
		Text:        string(s.buf[a:b]),
		StartLine:   s.stmtLine,
		StartOffset: s.stmtStart,
		EndOffset:   s.lastSig,
		Terminated:  terminated,
	}
	s.stmtStart = -1
	return st
}

func (s *Splitter) feed(tk int) {
	if s.dialect == SQLite {
		s.trig = trans[s.trig][tk]
	}
}

// Next 返回下一条语句；没有更多语句时返回 io.EOF；读取出错返回该错误。
func (s *Splitter) Next() (Statement, error) {
	if s.err != nil {
		return Statement{}, s.err
	}
	mysql := s.dialect == MySQL
	for {
		if !s.ensure(1) {
			if s.err != nil {
				return Statement{}, s.err
			}
			if s.stmtStart >= 0 {
				return s.emit(false), nil
			}
			return Statement{}, io.EOF
		}
		c := s.buf[s.pos]

		// 文件开头的 BOM
		if c == 0xEF && s.base+int64(s.pos) == 0 && s.ensure(3) && s.buf[s.pos+1] == 0xBB && s.buf[s.pos+2] == 0xBF {
			s.pos += 3
			continue
		}

		if isSpace(c) {
			if c == '\n' {
				s.line++
			}
			s.pos++
			s.feed(tkWS)
			continue
		}

		if s.inCond {
			if c == '*' {
				if n, ok := s.at(1); ok && n == '/' {
					s.mark(s.pos, s.line)
					s.pos += 2
					s.inCond = false
					s.sig()
					continue
				}
			}
		} else if mysql {
			if s.matchDelim() {
				s.pos += len(s.delim)
				if s.stmtStart >= 0 {
					return s.emit(true), nil
				}
				continue
			}
			if s.stmtStart < 0 && (c == 'd' || c == 'D') && s.delimiterDirective() {
				continue
			}
		} else if c == ';' {
			s.feed(tkSemi)
			if s.trig == stStart {
				s.pos++
				if s.stmtStart >= 0 {
					return s.emit(true), nil
				}
				continue
			}
			s.mark(s.pos, s.line)
			s.pos++
			s.sig()
			continue
		}

		// 注释
		if c == '#' && mysql {
			s.skipLine()
			s.feed(tkWS)
			continue
		}
		if c == '-' {
			if n, ok := s.at(1); ok && n == '-' {
				n2, ok2 := s.at(2)
				if !mysql || !ok2 || n2 <= ' ' || n2 == 0x7f {
					s.skipLine()
					s.feed(tkWS)
					continue
				}
			}
		}
		if c == '/' {
			if n, ok := s.at(1); ok && n == '*' {
				if mysql && !s.inCond {
					n2, _ := s.at(2)
					n3, _ := s.at(3)
					if n2 == '!' || (n2 == 'M' && n3 == '!') {
						s.mark(s.pos, s.line)
						if n2 == '!' {
							s.pos += 3
						} else {
							s.pos += 4
						}
						s.inCond = true
						s.sig()
						continue
					}
				}
				s.skipBlockComment()
				s.feed(tkWS)
				continue
			}
		}

		s.mark(s.pos, s.line)
		switch {
		case c == '\'' || c == '"':
			s.scanQuoted(c, mysql && !s.opt.NoBackslashEscapes)
			s.feed(tkOther)
		case c == '`':
			s.scanQuoted(c, false)
			s.feed(tkOther)
		case c == '[' && !mysql:
			s.scanUntil(']')
			s.feed(tkOther)
		case !mysql && isIdent(c):
			s.feed(s.scanWord())
		default:
			s.pos++
			s.feed(tkOther)
		}
		s.sig()
	}
}

// matchDelim 判断 pos 处是不是当前分隔符。
func (s *Splitter) matchDelim() bool {
	d := s.delim
	if s.buf[s.pos] != d[0] {
		return false
	}
	if len(d) == 1 {
		return true
	}
	if !s.ensure(len(d)) {
		return false
	}
	return string(s.buf[s.pos:s.pos+len(d)]) == d
}

// delimiterDirective 识别并执行 DELIMITER 指令。是指令返回 true（已消费到行尾）。
func (s *Splitter) delimiterDirective() bool {
	const kw = "delimiter"
	if !s.ensure(len(kw) + 1) {
		return false
	}
	if !strings.EqualFold(string(s.buf[s.pos:s.pos+len(kw)]), kw) {
		return false
	}
	if sep := s.buf[s.pos+len(kw)]; sep != ' ' && sep != '\t' {
		return false
	}
	// 读到行尾（最多看 4 KiB）
	end := len(kw)
	for end < 4096 {
		if !s.ensure(end + 1) {
			end = len(s.buf) - s.pos
			break
		}
		if c := s.buf[s.pos+end]; c == '\n' || c == '\r' {
			break
		}
		end++
	}
	rest := string(s.buf[s.pos+len(kw) : s.pos+end])
	rest = strings.TrimLeft(rest, " \t")
	arg := ""
	if rest != "" {
		if q := rest[0]; q == '\'' || q == '"' || q == '`' {
			if j := strings.IndexByte(rest[1:], q); j >= 0 {
				arg = rest[1 : 1+j]
			}
		} else {
			j := strings.IndexAny(rest, " \t")
			if j < 0 {
				j = len(rest)
			}
			arg = rest[:j]
		}
	}
	if arg == "" || strings.ContainsRune(arg, '\\') {
		return false
	}
	s.delim = arg
	s.pos += end
	return true
}

// skipLine 跳过行注释，停在换行符上（换行符按空白处理）。
func (s *Splitter) skipLine() {
	for {
		if s.pos >= len(s.buf) && !s.ensure(1) {
			return
		}
		b := s.buf
		i := s.pos
		for i < len(b) && b[i] != '\n' {
			i++
		}
		s.pos = i
		if i < len(b) {
			return
		}
	}
}

// skipBlockComment 跳过 /* ... */（未闭合则到末尾）。
func (s *Splitter) skipBlockComment() {
	s.pos += 2
	for {
		if s.pos >= len(s.buf) && !s.ensure(1) {
			return
		}
		b := s.buf
		i := s.pos
		for i < len(b) && b[i] != '*' {
			if b[i] == '\n' {
				s.line++
			}
			i++
		}
		s.pos = i
		if i >= len(b) {
			continue
		}
		s.pos++ // '*'
		if n, ok := s.at(0); ok && n == '/' {
			s.pos++
			return
		}
	}
}

// scanQuoted 扫描引号内容（含两端引号），支持双写，backslash 为真时支持反斜杠转义。未闭合则到末尾。
func (s *Splitter) scanQuoted(q byte, backslash bool) {
	s.pos++
	for {
		if s.pos >= len(s.buf) && !s.ensure(1) {
			return
		}
		b := s.buf
		i := s.pos
		for i < len(b) {
			c := b[i]
			if c == q || (backslash && c == '\\') {
				break
			}
			if c == '\n' {
				s.line++
			}
			i++
		}
		s.pos = i
		if i >= len(b) {
			continue
		}
		if b[i] == '\\' {
			s.pos++
			if n, ok := s.at(0); ok {
				if n == '\n' {
					s.line++
				}
				s.pos++
			}
			continue
		}
		s.pos++ // 引号
		if n, ok := s.at(0); ok && n == q {
			s.pos++
			continue
		}
		return
	}
}

// scanUntil 扫描到结束字符（含），用于 SQLite 的 [标识符]。
func (s *Splitter) scanUntil(end byte) {
	s.pos++
	for {
		if s.pos >= len(s.buf) && !s.ensure(1) {
			return
		}
		c := s.buf[s.pos]
		if c == '\n' {
			s.line++
		}
		s.pos++
		if c == end {
			return
		}
	}
}

// scanWord 扫描一个标识符或数字（SQLite），返回触发器状态机用的记号类型。
func (s *Splitter) scanWord() int {
	var w [9]byte
	n := 0
	for {
		if s.pos >= len(s.buf) && !s.ensure(1) {
			break
		}
		c := s.buf[s.pos]
		if !isIdent(c) {
			break
		}
		if n < len(w) {
			w[n] = c | 0x20
		}
		n++
		s.pos++
	}
	if n > len(w) {
		return tkOther
	}
	switch string(w[:n]) {
	case "create":
		return tkCreate
	case "temp", "temporary":
		return tkTemp
	case "trigger":
		return tkTrigger
	case "explain":
		return tkExplain
	case "end":
		return tkEnd
	}
	return tkOther
}
