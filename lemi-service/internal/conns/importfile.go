package conns

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/xuri/excelize/v2"
)

// TmpDir 导入文件的临时目录（由装配时设置为 <数据目录>/tmp/imports）。
var TmpDir string

type pendingImport struct {
	userID, connID, path, format string
	expires                      time.Time
}

var (
	importsMu sync.Mutex
	imports   = map[string]*pendingImport{}
)

func cleanImports() {
	importsMu.Lock()
	defer importsMu.Unlock()
	for id, p := range imports {
		if time.Now().After(p.expires) {
			_ = os.Remove(p.path)
			delete(imports, id)
		}
	}
}

// rowReader 逐行读取 CSV / TSV / XLSX。
type rowReader interface {
	Next() ([]string, error)
	Close() error
}

type csvReader struct {
	f *os.File
	r *csv.Reader
}

func (c *csvReader) Next() ([]string, error) { return c.r.Read() }
func (c *csvReader) Close() error             { return c.f.Close() }

type xlsxReader struct {
	f    *excelize.File
	rows *excelize.Rows
}

func (x *xlsxReader) Next() ([]string, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return x.rows.Columns()
}
func (x *xlsxReader) Close() error { x.rows.Close(); return x.f.Close() }

func openRows(p *pendingImport, sheet string) (rowReader, []string, error) {
	switch p.format {
	case "csv", "tsv":
		f, err := os.Open(p.path)
		if err != nil {
			return nil, nil, err
		}
		br := bufio.NewReaderSize(f, 1<<20)
		// 去掉 UTF-8 BOM
		if b, _ := br.Peek(3); len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
			_, _ = br.Discard(3)
		}
		r := csv.NewReader(br)
		if p.format == "tsv" {
			r.Comma = '\t'
			r.LazyQuotes = true
		}
		r.FieldsPerRecord = -1
		r.ReuseRecord = false
		return &csvReader{f: f, r: r}, nil, nil
	case "xlsx":
		f, err := excelize.OpenFile(p.path)
		if err != nil {
			return nil, nil, fmt.Errorf("无法读取 Excel 文件：%w", err)
		}
		sheets := f.GetSheetList()
		if sheet == "" && len(sheets) > 0 {
			sheet = sheets[0]
		}
		rows, err := f.Rows(sheet)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return &xlsxReader{f: f, rows: rows}, sheets, nil
	}
	return nil, nil, errors.New("不支持的文件格式")
}

func (s *Service) hImportPreview(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.conn(r, LevelWrite)
	if err != nil {
		return err
	}
	cleanImports()
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return apperr.Bad("请选择要导入的文件")
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	format := map[string]string{".csv": "csv", ".tsv": "tsv", ".txt": "tsv", ".xlsx": "xlsx"}[ext]
	if format == "" {
		return apperr.Bad("只支持 CSV、TSV、XLSX 文件")
	}
	if err := os.MkdirAll(TmpDir, 0o700); err != nil {
		return err
	}
	id := ids.New()
	path := filepath.Join(TmpDir, id+ext)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		os.Remove(path)
		return apperr.Bad("上传失败：%v", err)
	}
	out.Close()
	p := &pendingImport{userID: web.UserOf(r).ID, connID: c.ID, path: path, format: format, expires: time.Now().Add(time.Hour)}
	rr, sheets, err := openRows(p, r.FormValue("sheet"))
	if err != nil {
		os.Remove(path)
		return apperr.Bad("%s", err.Error())
	}
	defer rr.Close()
	var sample [][]string
	for len(sample) < 50 {
		row, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.Remove(path)
			return apperr.Bad("文件格式错误：%v", err)
		}
		for i, v := range row {
			if !utf8.ValidString(v) {
				os.Remove(path)
				return apperr.Bad("文件不是 UTF-8 编码（第 %d 行第 %d 列），请另存为 UTF-8 后再导入", len(sample)+1, i+1)
			}
		}
		sample = append(sample, row)
	}
	importsMu.Lock()
	imports[id] = p
	importsMu.Unlock()
	return web.OK(w, map[string]any{"import_id": id, "format": format, "sheets": sheets, "sample": sample})
}

// ImportRequest 导入执行。
type ImportRequest struct {
	ImportID    string         `json:"import_id"`
	Database    string         `json:"db"`
	Table       string         `json:"table"`
	Sheet       string         `json:"sheet"`
	Header      bool           `json:"header"`
	Mapping     map[string]int `json:"mapping"` // 表列 → 文件列序号（从 0 开始）
	EmptyAsNull bool           `json:"empty_as_null"`
	NullText    string         `json:"null_text"`
	SkipErrors  bool           `json:"skip_errors"`
}

func (s *Service) hImportRun(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, lvl, err := s.conn(r, LevelWrite)
	if err != nil {
		return err
	}
	var req ImportRequest
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	importsMu.Lock()
	p := imports[req.ImportID]
	importsMu.Unlock()
	u := web.UserOf(r)
	if p == nil || p.userID != u.ID || p.connID != c.ID {
		return apperr.New(404, "import_expired", "导入文件已过期，请重新上传")
	}
	if len(req.Mapping) == 0 {
		return apperr.Bad("请至少映射一列")
	}
	sc, err := s.SQL(r.Context(), c)
	if err != nil {
		return err
	}
	t, err := sc.Table(r.Context(), req.Database, req.Table)
	if err != nil {
		return apperr.Wrap("meta_failed", err)
	}
	colOK := map[string]bool{}
	for _, col := range t.Columns {
		if col.Generated == "" {
			colOK[col.Name] = true
		}
	}
	var cols []string
	var idx []int
	for name, i := range req.Mapping {
		if !colOK[name] {
			return apperr.Bad("列 %s 不存在或是生成列", name)
		}
		if i < 0 {
			continue
		}
		cols = append(cols, name)
		idx = append(idx, i)
	}
	rr, _, err := openRows(p, req.Sheet)
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	defer rr.Close()
	cs, err := s.OpenConsole(r.Context(), u, c, lvl, req.Database, audit.SrcUI, false, 0)
	if err != nil {
		return err
	}
	defer s.CloseConsole(cs.ID)
	qcols := make([]string, len(cols))
	for i, n := range cols {
		qcols[i] = sc.QuoteIdent(n)
	}
	rowMarks := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	prefix := "INSERT INTO " + qualified(sc, req.Database, req.Table) + " (" + strings.Join(qcols, ", ") + ") VALUES "
	batch := 500
	if sc.Dialect() == "sqlite" && len(cols)*batch > 30000 {
		batch = 30000 / len(cols)
	}
	var inserted, failed, lineNo int64
	var errs []string
	var buf [][]any
	flush := func(ctx context.Context) error {
		if len(buf) == 0 {
			return nil
		}
		marks := make([]string, len(buf))
		var args []any
		for i, row := range buf {
			marks[i] = rowMarks
			args = append(args, row...)
		}
		_, err := cs.S.Exec(ctx, prefix+strings.Join(marks, ", "), args, 0)
		if err != nil && req.SkipErrors {
			// 批量失败时逐行重试，跳过出错的行
			for _, row := range buf {
				if _, e := cs.S.Exec(ctx, prefix+rowMarks, row, 0); e != nil {
					failed++
					if len(errs) < 50 {
						errs = append(errs, e.Error())
					}
				} else {
					inserted++
				}
			}
			buf = buf[:0]
			return nil
		}
		if err != nil {
			return err
		}
		inserted += int64(len(buf))
		buf = buf[:0]
		return nil
	}
	ctx := r.Context()
	start := time.Now()
	if _, err := cs.S.Exec(ctx, "BEGIN", nil, 0); err != nil {
		return apperr.Wrap("tx_failed", err)
	}
	var runErr error
	for {
		row, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			runErr = fmt.Errorf("读取文件第 %d 行失败：%w", lineNo+1, err)
			break
		}
		lineNo++
		if lineNo == 1 && req.Header {
			continue
		}
		vals := make([]any, len(idx))
		for i, fi := range idx {
			var v any
			if fi < len(row) {
				sv := row[fi]
				switch {
				case req.NullText != "" && sv == req.NullText:
					v = nil
				case req.EmptyAsNull && sv == "":
					v = nil
				default:
					v = sv
				}
			}
			vals[i] = v
		}
		buf = append(buf, vals)
		if len(buf) >= batch {
			if err := flush(ctx); err != nil {
				runErr = fmt.Errorf("第 %d 行附近插入失败：%w", lineNo, err)
				break
			}
		}
		if ctx.Err() != nil {
			runErr = ctx.Err()
			break
		}
	}
	if runErr == nil {
		if err := flush(ctx); err != nil {
			runErr = fmt.Errorf("插入失败：%w", err)
		}
	}
	if runErr != nil {
		_, _ = cs.S.Exec(context.WithoutCancel(ctx), "ROLLBACK", nil, 0)
	} else if _, err := cs.S.Exec(ctx, "COMMIT", nil, 0); err != nil {
		runErr = fmt.Errorf("提交失败：%w", err)
	}
	srv.AuditR(r, audit.Entry{Action: "data.import", WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, Target: req.Database + "." + req.Table, OK: runErr == nil, Error: errText(runErr),
		DurationMS: time.Since(start).Milliseconds()}, map[string]any{"inserted": inserted, "failed": failed})
	if runErr != nil {
		return apperr.Bad("导入失败，已全部撤销：%v", runErr)
	}
	importsMu.Lock()
	delete(imports, req.ImportID)
	importsMu.Unlock()
	_ = os.Remove(p.path)
	return web.OK(w, map[string]any{"inserted": inserted, "failed": failed, "errors": errs, "duration_ms": time.Since(start).Milliseconds()})
}
