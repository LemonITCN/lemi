package backup

import (
	"bytes"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
)

// dumpObserver 旁观 mysqldump 的输出流（不改动数据），统计每张表的行数，写进备份清单供还原校验比对。
//
// mysqldump 的输出格式固定：每条 INSERT 独占一行（字符串里的换行被转义成 \n，所以值里不会出现裸换行），
// 形如 `INSERT INTO `t` VALUES (..),(..);`，表里有生成列、不可见列或用了 --complete-insert 时带列名：
// `INSERT INTO `t` (`a`, `b`) VALUES (..);`；`USE `db`;` 切换库；`CREATE TABLE `t` (` 开始一张表。
// 计数时按引号与反斜杠转义跟踪字符串，数顶层的左括号。整个过程是逐字节的状态机，内存与数据量无关。
type dumpObserver struct {
	mode  int    // 0 收集行首；1 跳到行尾；2 在 INSERT 里数行；3 跳过列名列表，等 VALUES
	name  string // mode 3：INSERT 的表名
	inBT  bool   // mode 3：在反引号里
	win   []byte // mode 3：最近读到的几个字节（找 "VALUES "）
	line  []byte
	inQ   byte
	esc   bool
	depth int
	db    string
	tab   *sqldump.TableManifest
	dbs   []*sqldump.DBManifest
	// onTable 每开始一张表调用一次
	onTable func(db, table string)
	// onRows 每数到一批行调用（参数是新增的行数）
	onRows func(n int64)
	pend   int64
}

const obsLineMax = 1024

func (o *dumpObserver) Write(p []byte) (int, error) {
	for _, b := range p {
		switch o.mode {
		case 0:
			if b == '\n' {
				o.classify(true)
				o.line = o.line[:0]
				continue
			}
			if len(o.line) == 0 && b != 'U' && b != 'C' && b != 'I' {
				o.mode = 1
				continue
			}
			o.line = append(o.line, b)
			// 表名后面的空格：接下来是 VALUES 或列名列表
			if b == ' ' && bytes.HasPrefix(o.line, []byte(insertPrefix)) {
				if name, ok := backtickName(string(o.line[len(insertPrefix):])); ok && len(o.line) == len(insertPrefix)+len(quoteBT(name))+2 {
					o.name, o.inBT, o.win = name, false, o.win[:0]
					o.mode = 3
					o.line = o.line[:0]
					continue
				}
			}
			if len(o.line) >= obsLineMax {
				o.classify(false)
				o.line = o.line[:0]
				o.mode = 1
			}
		case 1:
			if b == '\n' {
				o.mode = 0
			}
		case 3:
			if b == '\n' {
				o.mode = 0
				o.line = o.line[:0]
				continue
			}
			if b == '`' {
				o.inBT = !o.inBT // 名字里的 `` 是两次切换，结果不变
				continue
			}
			if o.inBT {
				continue
			}
			o.win = append(o.win, b)
			if len(o.win) > len(valuesWord) {
				o.win = o.win[len(o.win)-len(valuesWord):]
			}
			if string(o.win) == valuesWord {
				o.startInsert(o.name)
			}
		case 2:
			if o.esc {
				o.esc = false
				continue
			}
			if o.inQ != 0 {
				if b == '\\' {
					o.esc = true
				} else if b == o.inQ {
					o.inQ = 0
				}
				continue
			}
			switch b {
			case '\'', '"':
				o.inQ = b
			case '(':
				if o.depth == 0 {
					o.pend++
				}
				o.depth++
			case ')':
				if o.depth > 0 {
					o.depth--
				}
			case '\n':
				o.flushRows()
				o.mode, o.depth, o.inQ, o.esc = 0, 0, 0, false
				o.line = o.line[:0]
			}
		}
	}
	if o.pend > 0 && o.pend >= 1000 {
		o.flushRows()
	}
	return len(p), nil
}

func (o *dumpObserver) flushRows() {
	if o.pend == 0 {
		return
	}
	if o.tab != nil {
		o.tab.Rows += o.pend
	}
	if o.onRows != nil {
		o.onRows(o.pend)
	}
	o.pend = 0
}

// backtickName 解析行首某前缀之后的反引号标识符（“ 转义为一个反引号）。
func backtickName(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			if i+1 < len(s) && s[i+1] == '`' {
				b.WriteByte('`')
				i++
				continue
			}
			return b.String(), true
		}
		b.WriteByte(s[i])
	}
	return "", false
}

func (o *dumpObserver) curDB() *sqldump.DBManifest {
	for _, d := range o.dbs {
		if d.Name == o.db {
			return d
		}
	}
	d := &sqldump.DBManifest{Name: o.db}
	o.dbs = append(o.dbs, d)
	return d
}

func (o *dumpObserver) table(name string) *sqldump.TableManifest {
	d := o.curDB()
	for i := range d.Tables {
		if d.Tables[i].Name == name {
			return &d.Tables[i]
		}
	}
	d.Tables = append(d.Tables, sqldump.TableManifest{Name: name})
	return &d.Tables[len(d.Tables)-1]
}

func (o *dumpObserver) classify(complete bool) {
	s := string(o.line)
	switch {
	case strings.HasPrefix(s, "USE `"):
		if n, ok := backtickName(s[len("USE `"):]); ok {
			o.db = n
			o.curDB()
		}
	case strings.HasPrefix(s, "CREATE TABLE `"):
		if n, ok := backtickName(s[len("CREATE TABLE `"):]); ok {
			o.table(n)
			if o.onTable != nil {
				o.onTable(o.db, n)
			}
		}
	}
	_ = complete
}

const (
	insertPrefix = "INSERT INTO `"
	valuesWord   = "VALUES "
)

// quoteBT 反引号里的写法（` 写成 “），不含两侧的反引号。
func quoteBT(name string) string { return strings.ReplaceAll(name, "`", "``") }

func (o *dumpObserver) startInsert(name string) {
	// 切表时把指针换过去（Rows 累加到对应的表上）
	o.flushRows()
	// table() 可能让切片扩容，指针每次重新取
	o.tab = nil
	t := o.table(name)
	o.tab = t
	o.mode, o.depth, o.inQ, o.esc = 2, 0, 0, false
	o.line = o.line[:0]
}

// manifest 返回统计结果。
func (o *dumpObserver) manifest() []sqldump.DBManifest {
	o.flushRows()
	out := make([]sqldump.DBManifest, 0, len(o.dbs))
	for _, d := range o.dbs {
		if d.Name == "" && len(d.Tables) == 0 {
			continue
		}
		out = append(out, *d)
	}
	return out
}
