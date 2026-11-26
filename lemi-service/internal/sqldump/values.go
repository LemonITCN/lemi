package sqldump

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// rowHasher 是顺序无关的多重集哈希：每行规范化编码取 SHA-256 前 8 字节，求和 mod 2^64。
type rowHasher struct {
	sum  uint64
	rows int64
	buf  []byte
}

func (h *rowHasher) reset() { h.buf = h.buf[:0] }

// 每个值编码为 标记字节 + uvarint(长度) + 内容，NULL 只有标记。
func (h *rowHasher) null() { h.buf = append(h.buf, 'n') }

func (h *rowHasher) val(tag byte, b []byte) {
	h.buf = append(h.buf, tag)
	h.buf = binary.AppendUvarint(h.buf, uint64(len(b)))
	h.buf = append(h.buf, b...)
}

func (h *rowHasher) valStr(tag byte, s string) {
	h.buf = append(h.buf, tag)
	h.buf = binary.AppendUvarint(h.buf, uint64(len(s)))
	h.buf = append(h.buf, s...)
}

func (h *rowHasher) endRow() {
	s := sha256.Sum256(h.buf)
	h.sum += binary.BigEndian.Uint64(s[:8])
	h.rows++
	h.buf = h.buf[:0]
}

func (h *rowHasher) String() string { return fmt.Sprintf("%s%016x", hashPrefix, h.sum) }

// ---------- 浮点数 ----------

// appendFloat32Literal 写出能被 MySQL 精确还原成 f 的十进制文本。
// MySQL 先把文本解析成 double、检查不超过 FLT_MAX，再转成 float，所以最短 float32 表示要过两道检查，
// 不行就退回 f 的精确 double 表示（一定能还原）。
func appendFloat32Literal(b []byte, f float32) []byte {
	if f == 0 && math.Signbit(float64(f)) {
		return append(b, "-0e0"...)
	}
	s := strconv.FormatFloat(float64(f), 'g', -1, 32)
	if d, err := strconv.ParseFloat(s, 64); err != nil || float32(d) != f || math.Abs(d) > math.MaxFloat32 {
		s = strconv.FormatFloat(float64(f), 'g', -1, 64)
	}
	return append(b, s...)
}

// appendFloat64Literal 写出 MySQL 能精确还原的 double 文本（最短往返表示）。
func appendFloat64Literal(b []byte, f float64) []byte {
	if f == 0 && math.Signbit(f) {
		return append(b, "-0e0"...)
	}
	return strconv.AppendFloat(b, f, 'g', -1, 64)
}

// appendSQLiteReal 写出 SQLite REAL 字面量：保证带小数点或指数（否则会被当成 INTEGER），无穷写成 9e999。
func appendSQLiteReal(b []byte, f float64) []byte {
	switch {
	case math.IsInf(f, 1):
		return append(b, "9e999"...)
	case math.IsInf(f, -1):
		return append(b, "-9e999"...)
	case math.IsNaN(f):
		return append(b, "NULL"...)
	}
	start := len(b)
	b = strconv.AppendFloat(b, f, 'g', -1, 64)
	for _, c := range b[start:] {
		if c == '.' || c == 'e' || c == 'E' {
			return b
		}
	}
	return append(b, ".0"...)
}

// ---------- 字符串 ----------

// appendMySQLString 写出 MySQL 单引号字符串，按 MySQL 规则转义 \0 \' \" \b \n \r \t \Z \\。
func appendMySQLString(b []byte, s []byte) []byte {
	b = append(b, '\'')
	start := 0
	for i, c := range s {
		var esc byte
		switch c {
		case 0:
			esc = '0'
		case '\'':
			esc = '\''
		case '"':
			esc = '"'
		case '\b':
			esc = 'b'
		case '\n':
			esc = 'n'
		case '\r':
			esc = 'r'
		case '\t':
			esc = 't'
		case 0x1a:
			esc = 'Z'
		case '\\':
			esc = '\\'
		default:
			continue
		}
		b = append(b, s[start:i]...)
		b = append(b, '\\', esc)
		start = i + 1
	}
	b = append(b, s[start:]...)
	return append(b, '\'')
}

// quoteMySQLIdent 用反引号括起标识符，内部反引号双写。
func quoteMySQLIdent(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '`')
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			b = append(b, '`')
		}
		b = append(b, s[i])
	}
	return string(append(b, '`'))
}

// quoteSQLiteIdent 用双引号括起标识符，内部双引号双写。
func quoteSQLiteIdent(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			b = append(b, '"')
		}
		b = append(b, s[i])
	}
	return string(append(b, '"'))
}

// appendSQLiteString 写出 SQLite 字符串字面量。含 \0（SQL 文本在 \0 处截断）、\r（防换行符被传输工具改写）
// 或非法 UTF-8 时写成 CAST(X'..' AS TEXT)，保证逐字节一致。
func appendSQLiteString(b []byte, s string) []byte {
	safe := utf8.ValidString(s)
	for i := 0; safe && i < len(s); i++ {
		if s[i] == 0 || s[i] == '\r' {
			safe = false
		}
	}
	if !safe {
		b = append(b, "CAST(X'"...)
		b = hexUpper(b, []byte(s))
		return append(b, "' AS TEXT)"...)
	}
	b = append(b, '\'')
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b = append(b, s[start:i+1]...)
			b = append(b, '\'')
			start = i + 1
		}
	}
	b = append(b, s[start:]...)
	return append(b, '\'')
}

// quoteSQLiteString 用于把名字放进 SQL 字符串（例如 sqlite_sequence 的 name）。
func quoteSQLiteString(s string) string { return string(appendSQLiteString(nil, s)) }

// appendBits 把大端字节写成 b'0101'（不带前导 0）。
func appendBits(b []byte, v []byte) []byte {
	b = append(b, "b'"...)
	started := false
	for _, c := range v {
		for i := 7; i >= 0; i-- {
			bit := c >> uint(i) & 1
			if bit == 1 {
				started = true
			}
			if started {
				b = append(b, '0'+bit)
			}
		}
	}
	if !started {
		b = append(b, '0')
	}
	return append(b, '\'')
}
