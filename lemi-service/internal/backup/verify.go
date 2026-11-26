package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver/mysql"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqldump"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// Mismatch 是校验发现的一处不一致。
type Mismatch struct {
	Database     string `json:"database"`
	Table        string `json:"table"`
	ExpectedRows int64  `json:"expected_rows"`
	GotRows      int64  `json:"got_rows"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	GotHash      string `json:"got_hash,omitempty"`
	Error        string `json:"error,omitempty"`
}

// VerifyResult 是还原校验结果（存进备份记录）。
type VerifyResult struct {
	OK             bool       `json:"ok"`
	Method         string     `json:"method"` // 用什么还原的：mysql 客户端 / 自研导入器 / 文件
	Connection     string     `json:"connection,omitempty"`
	TempDatabases  []string   `json:"temp_databases,omitempty"`
	TablesChecked  int        `json:"tables_checked"`
	RowsChecked    int64      `json:"rows_checked"`
	HashChecked    bool       `json:"hash_checked"` // 是否比对了内容哈希（自研导出器与文件快照有）
	Integrity      string     `json:"integrity,omitempty"`
	Mismatches     []Mismatch `json:"mismatches,omitempty"`
	Error          string     `json:"error,omitempty"`
	DurationMS     int64      `json:"duration_ms"`
	TriggeredBy    string     `json:"triggered_by,omitempty"`
	TempDBDropped  bool       `json:"temp_db_dropped,omitempty"`
	TempDropErrors []string   `json:"temp_drop_errors,omitempty"`
}

func (r *Run) manifestData() (*BackupManifest, error) {
	var m BackupManifest
	if err := json.Unmarshal([]byte(r.Manifest), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// StartVerify 开始还原校验（异步）。MySQL 备份还原到空间的校验连接上的临时库，SQLite 在数据目录的临时文件里还原。
func (s *Service) StartVerify(ctx context.Context, r *Run, userID string) (string, error) {
	if r.Status != StatusSuccess {
		return "", apperr.Bad("只能校验成功的备份")
	}
	if r.VerifyStatus == "running" {
		return "", apperr.Conflict("这份备份正在校验")
	}
	var vc *conns.Connection
	lockKey := "verify-file:" + r.ID
	if r.Driver == "mysql" {
		id := s.GetWSSettings(ctx, r.WorkspaceID).VerifyConnectionID
		if id == "" {
			return "", apperr.Bad("还没有设置校验连接：请在备份设置里指定一个用于还原校验的 MySQL 连接")
		}
		c, err := s.Conns.Get(ctx, id)
		if err != nil || c.WorkspaceID != r.WorkspaceID || c.Driver != "mysql" {
			return "", apperr.Bad("校验连接不存在或不是 MySQL 连接")
		}
		vc, lockKey = c, c.ID
	}
	taskID := ids.New()
	release, ok := s.acquire(lockKey, taskID)
	if !ok {
		return "", apperr.Conflict("校验连接正在执行其他任务，请稍后再试")
	}
	if _, err := s.Store.Exec(ctx, `UPDATE backup_runs SET verify_status = 'running' WHERE id = ?`, r.ID); err != nil {
		release()
		return "", err
	}
	rctx, cancel := context.WithTimeout(s.stopCtx, RunTimeout)
	s.setCancel(taskID, cancel)
	prog := &progress{Kind: "verify", ID: taskID, WorkspaceID: r.WorkspaceID, ConnectionID: r.ConnectionID, PlanID: r.PlanID, phase: "queued"}
	prog.total.Store(r.Size)
	finish := s.track(prog)
	s.begin()
	go func() {
		defer s.end()
		defer release()
		defer cancel()
		defer s.setCancel(taskID, nil)
		start := time.Now()
		res := &VerifyResult{TriggeredBy: userID}
		var err error
		select {
		case s.sem <- struct{}{}:
			prog.set(func(p *progress) { p.phase = "running" })
			if vc != nil {
				err = s.verifyMySQL(rctx, r, vc, res, prog)
			} else {
				err = s.verifySQLite(rctx, r, res, prog)
			}
			<-s.sem
		case <-rctx.Done():
			err = rctx.Err()
		}
		if err == nil && len(res.Mismatches) > 0 {
			err = fmt.Errorf("%d 处不一致", len(res.Mismatches))
		}
		res.OK = err == nil
		if err != nil && res.Error == "" {
			res.Error = err.Error()
		}
		res.DurationMS = time.Since(start).Milliseconds()
		s.finishVerify(r, res, finish)
	}()
	return taskID, nil
}

func (s *Service) finishVerify(r *Run, res *VerifyResult, finish func(map[string]any)) {
	ctx := context.Background()
	st := "ok"
	if !res.OK {
		st = "failed"
	}
	_, _ = s.Store.Exec(ctx, `UPDATE backup_runs SET verify_status = ?, verify_at = ?, verify_detail = ? WHERE id = ?`, st, store.Now(), toJSON(res), r.ID)
	finish(map[string]any{"status": st, "error": res.Error, "run_id": r.ID})
	s.Audit.Record(audit.Entry{UserID: res.TriggeredBy, WorkspaceID: r.WorkspaceID, ConnectionID: r.ConnectionID, Source: audit.SrcSystem, Action: "backup.verify",
		Target: r.FileName, OK: res.OK, Error: res.Error, DurationMS: res.DurationMS}, map[string]any{"run_id": r.ID, "tables": res.TablesChecked, "mismatches": len(res.Mismatches)})
	if !res.OK {
		if p, err := s.GetPlan(ctx, r.PlanID); err == nil {
			s.notifyWS(r.WorkspaceID, jsonStrings(p.NotifyFailure), notify.Event{Kind: notify.EvVerifyFailed, Level: "error",
				Title:   "备份校验失败：" + r.ConnectionName,
				Message: fmt.Sprintf("备份 %s 的还原校验失败：%s", r.FileName, res.Error), Link: "/w/" + r.WorkspaceID + "/backups?run=" + r.ID})
		}
	}
}

// verifyMySQL 还原到校验连接上的临时库，逐表比对。
func (s *Service) verifyMySQL(ctx context.Context, r *Run, vc *conns.Connection, res *VerifyResult, prog *progress) error {
	man, err := r.manifestData()
	if err != nil {
		return errors.New("备份清单损坏")
	}
	res.Connection = vc.Name
	tag := "lemi_verify_" + strings.ToLower(ids.RandomToken(8))
	m := map[string]string{}
	dbs := r.DatabaseList()
	for i, db := range dbs {
		name := tag
		if len(dbs) > 1 {
			name = fmt.Sprintf("%s_%d", tag, i+1)
		}
		m[db] = name
		res.TempDatabases = append(res.TempDatabases, name)
	}
	sc, err := s.Conns.SQL(ctx, vc)
	if err != nil {
		return err
	}
	// 结束时删除临时库（即使 ctx 已取消）
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res.TempDBDropped = true
		for _, name := range res.TempDatabases {
			if _, err := sc.DB().ExecContext(dctx, "DROP DATABASE IF EXISTS "+mysql.QuoteIdent(name)); err != nil {
				res.TempDBDropped = false
				res.TempDropErrors = append(res.TempDropErrors, name+"："+err.Error())
			}
		}
	}()
	src, _, err := s.openDecoded(ctx, r, "", func(n int64) { prog.out.Add(n) })
	if err != nil {
		return err
	}
	defer src.Close()
	counted := &countReader{src, func(n int64) { prog.raw.Add(n) }}
	useClient := false
	if r.Method == MethodMysqldump && s.Tools != nil {
		if ok, _ := s.Tools.Available(); ok {
			useClient = true
		}
	}
	if useClient {
		info, err := sc.Server(ctx)
		if err != nil {
			return err
		}
		tool, _, err := s.Tools.Resolve(ctx, info.Product, info.Version, true, nil)
		if err != nil {
			return err
		}
		cc, closeFn, err := s.clientConn(ctx, vc)
		if err != nil {
			return err
		}
		defer closeFn()
		res.Method = "mysql " + tool.Version
		if _, err := tool.Restore(ctx, cc, "", s.tmpDir("client"), newDBRenamer(counted, m)); err != nil {
			return fmt.Errorf("还原到临时库失败：%w", err)
		}
	} else {
		res.Method = sqldump.ToolName
		conn, err := sc.DB().Conn(ctx)
		if err != nil {
			return err
		}
		_, err = importInto(ctx, conn, sqldump.MySQL, counted, m, prog)
		conn.Close()
		if err != nil {
			return fmt.Errorf("还原到临时库失败：%w", err)
		}
	}
	prog.set(func(p *progress) { p.phase = "comparing" })
	conn, err := sc.DB().Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, dbm := range man.Databases {
		tmp, ok := m[dbm.Name]
		if !ok {
			continue
		}
		// 临时库里实际的表
		got := map[string]bool{}
		rows, err := conn.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'`, tmp)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				got[n] = true
			}
		}
		rows.Close()
		for _, t := range dbm.Tables {
			if err := ctx.Err(); err != nil {
				return err
			}
			res.TablesChecked++
			prog.set(func(p *progress) { p.database, p.table = dbm.Name, t.Name })
			if !got[t.Name] {
				res.Mismatches = append(res.Mismatches, Mismatch{Database: dbm.Name, Table: t.Name, ExpectedRows: t.Rows, Error: "还原后缺少这张表"})
				continue
			}
			mm := Mismatch{Database: dbm.Name, Table: t.Name, ExpectedRows: t.Rows, ExpectedHash: t.Hash}
			if t.Hash != "" {
				res.HashChecked = true
				n, h, err := sqldump.TableChecksum(ctx, conn, sqldump.MySQL, tmp, t.Name)
				if err != nil {
					mm.Error = err.Error()
					res.Mismatches = append(res.Mismatches, mm)
					continue
				}
				mm.GotRows, mm.GotHash = n, h
			} else {
				if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+mysql.QuoteIdent(tmp)+"."+mysql.QuoteIdent(t.Name)).Scan(&mm.GotRows); err != nil {
					mm.Error = err.Error()
					res.Mismatches = append(res.Mismatches, mm)
					continue
				}
			}
			res.RowsChecked += mm.GotRows
			if mm.GotRows != t.Rows || (t.Hash != "" && mm.GotHash != t.Hash) {
				res.Mismatches = append(res.Mismatches, mm)
			}
			delete(got, t.Name)
		}
		for n := range got {
			res.Mismatches = append(res.Mismatches, Mismatch{Database: dbm.Name, Table: n, Error: "还原后多出了清单里没有的表"})
		}
	}
	return nil
}

// verifySQLite 在数据目录的临时文件里还原并做 integrity_check 与逐表比对。
func (s *Service) verifySQLite(ctx context.Context, r *Run, res *VerifyResult, prog *progress) error {
	man, err := r.manifestData()
	if err != nil {
		return errors.New("备份清单损坏")
	}
	dir := s.tmpDir("verify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ids.New()+".db")
	defer func() {
		for _, sfx := range []string{"", "-wal", "-shm", "-journal"} {
			os.Remove(tmp + sfx)
		}
	}()
	src, _, err := s.openDecoded(ctx, r, "", func(n int64) { prog.out.Add(n) })
	if err != nil {
		return err
	}
	defer src.Close()
	counted := &countReader{src, func(n int64) { prog.raw.Add(n) }}
	if r.Method == MethodSQLiteFile {
		res.Method = "file"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, ctxReader{ctx, counted})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	} else {
		res.Method = sqldump.ToolName
		db, err := openSQLite(tmp, false)
		if err != nil {
			return err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			return err
		}
		_, err = importInto(ctx, conn, sqldump.SQLite, counted, nil, prog)
		conn.Close()
		db.Close()
		if err != nil {
			return fmt.Errorf("还原到临时文件失败：%w", err)
		}
	}
	prog.set(func(p *progress) { p.phase = "comparing" })
	ic, err := integrityCheck(ctx, tmp)
	if err != nil {
		return err
	}
	res.Integrity = ic
	if ic != "ok" {
		return fmt.Errorf("integrity_check 未通过：%s", ic)
	}
	got, err := sqliteManifest(ctx, tmp)
	if err != nil {
		return err
	}
	gotMap := map[string]sqldump.TableManifest{}
	for _, t := range got {
		gotMap[t.Name] = t
	}
	for _, dbm := range man.Databases {
		for _, t := range dbm.Tables {
			res.TablesChecked++
			g, ok := gotMap[t.Name]
			if !ok {
				res.Mismatches = append(res.Mismatches, Mismatch{Database: "main", Table: t.Name, ExpectedRows: t.Rows, Error: "还原后缺少这张表"})
				continue
			}
			delete(gotMap, t.Name)
			res.RowsChecked += g.Rows
			if t.Hash != "" {
				res.HashChecked = true
			}
			if g.Rows != t.Rows || (t.Hash != "" && g.Hash != t.Hash) {
				res.Mismatches = append(res.Mismatches, Mismatch{Database: "main", Table: t.Name, ExpectedRows: t.Rows, GotRows: g.Rows, ExpectedHash: t.Hash, GotHash: g.Hash})
			}
		}
	}
	for n := range gotMap {
		res.Mismatches = append(res.Mismatches, Mismatch{Database: "main", Table: n, Error: "还原后多出了清单里没有的表"})
	}
	return nil
}
