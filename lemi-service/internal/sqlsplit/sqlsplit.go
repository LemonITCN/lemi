// Package sqlsplit 是 SQL 语句切分器。
//
// 占位实现：完整实现（DELIMITER、条件注释、触发器体、流式）由独立分支提供，合并时整体替换本文件。
package sqlsplit

import "strings"

// Dialect 方言。
type Dialect string

const (
	MySQL  Dialect = "mysql"
	SQLite Dialect = "sqlite"
)

// Statement 是切出的一条语句。
type Statement struct {
	Text        string
	StartLine   int
	StartOffset int64
	EndOffset   int64
}

// Split 切分整段 SQL（按分号，跳过字符串与注释）。
func Split(src string, d Dialect) []Statement {
	var out []Statement
	start, line, startLine := 0, 1, 1
	inQ := byte(0)
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			line++
		}
		if inQ != 0 {
			if c == '\\' && d == MySQL && inQ != '`' {
				i++
				continue
			}
			if c == inQ {
				inQ = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"' || c == '`':
			inQ = c
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			line++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				i = len(src)
			} else {
				line += strings.Count(src[i:i+j+4], "\n")
				i += j + 3
			}
		case c == ';':
			if t := strings.TrimSpace(src[start:i]); t != "" {
				out = append(out, Statement{Text: t, StartLine: startLine, StartOffset: int64(start), EndOffset: int64(i)})
			}
			start, startLine = i+1, line
		}
	}
	if t := strings.TrimSpace(src[start:]); t != "" {
		out = append(out, Statement{Text: t, StartLine: startLine, StartOffset: int64(start), EndOffset: int64(len(src))})
	}
	return out
}
