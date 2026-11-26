package driver

import (
	"database/sql"
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 单元格传给前端时的上限（§8.5）。
const (
	MaxCellBytes = 64 << 10
)

// Column 是结果集的列描述。
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Nullable *bool  `json:"nullable,omitempty"`
	Table    string `json:"table,omitempty"`
	Database string `json:"db,omitempty"`
	Origin   string `json:"origin,omitempty"` // 原始列名（结果可编辑时）
	PK       bool   `json:"pk,omitempty"`
}

// BinValue 二进制值。
type BinValue struct {
	B64 string `json:"$bin"`
	Len int    `json:"len"`
}

// TruncValue 被截断的长文本。
type TruncValue struct {
	Text string `json:"$trunc"`
	Len  int    `json:"len"`
}

// binaryKinds 一定按二进制传的种类。
func isBinaryKind(kind string) bool { return kind == "binary" || kind == "geometry" }

// EncodeCell 把扫描得到的值编码为前端格式：数值一律字符串（防精度丢失），二进制 $bin，超长文本 $trunc。
func EncodeCell(v any, kind string, limit int) any {
	if limit <= 0 {
		limit = MaxCellBytes
	}
	switch x := v.(type) {
	case nil:
		return nil
	case sql.RawBytes:
		return encodeBytes([]byte(x), kind, limit)
	case []byte:
		return encodeBytes(x, kind, limit)
	case string:
		return encodeText(x, limit)
	case int64:
		if kind == "bool" {
			return strconv.FormatInt(x, 10)
		}
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return FormatFloat(x)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		// SQLite 驱动会把声明为 DATE/DATETIME 的文本解析成时间：日期列按原样只显示日期，避免写回时改变数据格式
		if kind == "date" && x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		if x.Nanosecond() == 0 {
			return x.Format("2006-01-02 15:04:05")
		}
		return strings.TrimRight(x.Format("2006-01-02 15:04:05.000000"), "0")
	}
	return encodeText(strconv.Quote("?"), limit)
}

// FormatFloat 往返精确的最短表示（NaN/Inf 原样写成文字）。
func FormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func encodeBytes(b []byte, kind string, limit int) any {
	if isBinaryKind(kind) || (kind != "string" && kind != "text" && kind != "json" && !utf8.Valid(b)) {
		n := len(b)
		if n > limit {
			b = b[:limit]
		}
		return BinValue{B64: base64.StdEncoding.EncodeToString(b), Len: n}
	}
	if len(b) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		return TruncValue{Text: string(b[:cut]), Len: len(b)}
	}
	return string(b)
}

func encodeText(s string, limit int) any {
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return TruncValue{Text: s[:cut], Len: len(s)}
	}
	return s
}

// CellText 把编码后的单元格转成纯文本（给智能体看）。
func CellText(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return x
	case BinValue:
		return "0x(" + strconv.Itoa(x.Len) + " 字节二进制)"
	case TruncValue:
		return x.Text + "…（共 " + strconv.Itoa(x.Len) + " 字节，已截断）"
	}
	return ""
}
