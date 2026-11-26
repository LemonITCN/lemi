package sqlsplit

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

type testCase struct {
	Name               string   `json:"name"`
	Dialect            Dialect  `json:"dialect"`
	NoBackslashEscapes bool     `json:"noBackslashEscapes"`
	Input              string   `json:"input"`
	Statements         []string `json:"statements"`
	StartLines         []int    `json:"startLines"`
}

func loadCases(t testing.TB) []testCase {
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cs []testCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func texts(sts []Statement) []string {
	out := []string{}
	for _, s := range sts {
		out = append(out, s.Text)
	}
	return out
}

// streamAll 用一次只给一个字节的 Reader 流式切分，覆盖所有缓冲区边界。
func streamAll(t testing.TB, src string, d Dialect, o Options) []Statement {
	sp := NewSplitterOptions(iotest.OneByteReader(strings.NewReader(src)), d, o)
	var out []Statement
	for {
		st, err := sp.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("流式切分出错: %v", err)
		}
		out = append(out, st)
	}
}

func checkInvariants(t testing.TB, src string, sts []Statement) {
	var prev int64
	for i, s := range sts {
		if s.StartOffset < prev || s.EndOffset < s.StartOffset || s.EndOffset > int64(len(src)) {
			t.Fatalf("第 %d 条偏移不合法: %d..%d（上一条结束 %d，原文 %d 字节）", i, s.StartOffset, s.EndOffset, prev, len(src))
		}
		if got := src[s.StartOffset:s.EndOffset]; got != s.Text {
			t.Fatalf("第 %d 条偏移与文本不一致: 原文[%d:%d]=%q, Text=%q", i, s.StartOffset, s.EndOffset, got, s.Text)
		}
		if want := 1 + strings.Count(src[:s.StartOffset], "\n"); s.StartLine != want {
			t.Fatalf("第 %d 条起始行 %d，应为 %d", i, s.StartLine, want)
		}
		if strings.Trim(s.Text, " \t\r\n\f\v") == "" {
			t.Fatalf("第 %d 条是空语句", i)
		}
		prev = s.EndOffset
	}
}

func TestSharedCases(t *testing.T) {
	cs := loadCases(t)
	if len(cs) < 60 {
		t.Fatalf("共享用例只有 %d 条", len(cs))
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if seen[c.Name] {
			t.Fatalf("用例重名: %s", c.Name)
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			o := Options{NoBackslashEscapes: c.NoBackslashEscapes}
			got := SplitOptions(c.Input, c.Dialect, o)
			if !reflect.DeepEqual(texts(got), c.Statements) {
				t.Fatalf("输入 %q\n得到 %q\n应为 %q", c.Input, texts(got), c.Statements)
			}
			checkInvariants(t, c.Input, got)
			if c.StartLines != nil {
				var lines []int
				for _, s := range got {
					lines = append(lines, s.StartLine)
				}
				if !reflect.DeepEqual(lines, c.StartLines) {
					t.Fatalf("起始行 %v，应为 %v", lines, c.StartLines)
				}
			}
			if st := streamAll(t, c.Input, c.Dialect, o); !reflect.DeepEqual(st, got) {
				t.Fatalf("逐字节流式结果与整段切分不同:\n%#v\n%#v", st, got)
			}
		})
	}
}

func TestTerminated(t *testing.T) {
	got := Split("SELECT 1; SELECT 2", MySQL)
	if len(got) != 2 || !got[0].Terminated || got[1].Terminated {
		t.Fatalf("%#v", got)
	}
}

func TestStatementTooLarge(t *testing.T) {
	src := "SELECT 1; SELECT '" + strings.Repeat("x", 200000)
	sp := NewSplitterOptions(strings.NewReader(src), MySQL, Options{MaxStatementBytes: 1000})
	if _, err := sp.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Next(); !errors.Is(err, ErrStatementTooLarge) {
		t.Fatalf("应报语句过长，得到 %v", err)
	}
}

func TestReadError(t *testing.T) {
	boom := errors.New("boom")
	sp := NewSplitter(io.MultiReader(strings.NewReader("SELECT 1; SELECT"), iotest.ErrReader(boom)), MySQL)
	if st, err := sp.Next(); err != nil || st.Text != "SELECT 1" {
		t.Fatalf("%v %v", st, err)
	}
	if _, err := sp.Next(); !errors.Is(err, boom) {
		t.Fatalf("读取错误应原样返回，得到 %v", err)
	}
}

func TestSetNoBackslashEscapesBetweenStatements(t *testing.T) {
	sp := NewSplitter(strings.NewReader(`SET sql_mode='NO_BACKSLASH_ESCAPES'; SELECT 'a\'; SELECT 2;`), MySQL)
	st, _ := sp.Next()
	if st.Text != `SET sql_mode='NO_BACKSLASH_ESCAPES'` {
		t.Fatal(st.Text)
	}
	sp.SetNoBackslashEscapes(true)
	st, _ = sp.Next()
	if st.Text != `SELECT 'a\'` {
		t.Fatal(st.Text)
	}
}

// 大文件流式切分：内存只与单条语句有关。
func TestStreamingLargeInput(t *testing.T) {
	const n = 200000
	r := &repeatReader{unit: []byte("INSERT INTO `t` VALUES (1, 'a;b\\'c', \"x\");\n-- note\n"), n: n}
	sp := NewSplitter(r, MySQL)
	count := 0
	for {
		st, err := sp.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if st.Text != "INSERT INTO `t` VALUES (1, 'a;b\\'c', \"x\")" {
			t.Fatalf("第 %d 条: %q", count, st.Text)
		}
		if st.StartLine != 2*count+1 {
			t.Fatalf("第 %d 条起始行 %d", count, st.StartLine)
		}
		count++
	}
	if count != n {
		t.Fatalf("切出 %d 条，应为 %d", count, n)
	}
	if cap(sp.buf) > 1<<20 {
		t.Fatalf("缓冲区涨到 %d 字节", cap(sp.buf))
	}
}

type repeatReader struct {
	unit []byte
	n    int
	off  int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	w := 0
	for w < len(p) && r.n > 0 {
		c := copy(p[w:], r.unit[r.off:])
		w += c
		r.off += c
		if r.off == len(r.unit) {
			r.off = 0
			r.n--
		}
	}
	return w, nil
}

func FuzzSplit(f *testing.F) {
	for _, c := range loadCases(f) {
		f.Add(c.Input, c.Dialect == SQLite, c.NoBackslashEscapes)
	}
	f.Fuzz(func(t *testing.T, src string, sqlite, nbe bool) {
		d := MySQL
		if sqlite {
			d = SQLite
		}
		o := Options{NoBackslashEscapes: nbe}
		got := SplitOptions(src, d, o)
		checkInvariants(t, src, got)
		if st := streamAll(t, src, d, o); !reflect.DeepEqual(st, got) {
			t.Fatalf("流式与整段结果不同: %q", src)
		}
	})
}
