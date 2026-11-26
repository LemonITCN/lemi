package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 运行状态。
const (
	StatusRunning  = "running"
	StatusSuccess  = "success"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

// Run 是一次备份。
type Run struct {
	ID             string `db:"id" json:"id"`
	WorkspaceID    string `db:"workspace_id" json:"workspace_id"`
	PlanID         string `db:"plan_id" json:"plan_id"`
	PlanName       string `db:"plan_name" json:"plan_name"`
	ConnectionID   string `db:"connection_id" json:"connection_id"`
	ConnectionName string `db:"connection_name" json:"connection_name"`
	Driver         string `db:"driver" json:"driver"`
	Trigger        string `db:"trigger_kind" json:"trigger"`
	Attempt        int    `db:"attempt" json:"attempt"`
	Status         string `db:"status" json:"status"`
	Method         string `db:"method" json:"method"`
	Tool           string `db:"tool" json:"tool"`
	ServerVersion  string `db:"server_version" json:"server_version"`
	Databases      string `db:"databases" json:"-"`
	FileName       string `db:"file_name" json:"file_name"`
	Compression    string `db:"compression" json:"compression"`
	Encrypted      bool   `db:"encrypted" json:"encrypted"`
	Passphrase     string `db:"passphrase" json:"-"`
	Size           int64  `db:"size" json:"size"`
	RawSize        int64  `db:"raw_size" json:"raw_size"`
	SHA256         string `db:"sha256" json:"sha256"`
	Manifest       string `db:"manifest" json:"-"`
	Warnings       string `db:"warnings" json:"-"`
	Error          string `db:"error" json:"error"`
	CreatedBy      string `db:"created_by" json:"created_by"`
	StartedAt      int64  `db:"started_at" json:"started_at"`
	FinishedAt     int64  `db:"finished_at" json:"finished_at"`
	VerifyStatus   string `db:"verify_status" json:"verify_status"`
	VerifyAt       int64  `db:"verify_at" json:"verify_at"`
	VerifyDetail   string `db:"verify_detail" json:"-"`
}

// DatabaseList 备份里的库。
func (r *Run) DatabaseList() []string { return jsonStrings(r.Databases) }

// FileRec 是一个目标上的备份文件。
type FileRec struct {
	RunID    string `db:"run_id" json:"run_id"`
	TargetID string `db:"target_id" json:"target_id"`
	Path     string `db:"path" json:"path"`
	Size     int64  `db:"size" json:"size"`
	Status   string `db:"status" json:"status"`
	Error    string `db:"error" json:"error"`
}

// GetRun 取备份记录。
func (s *Service) GetRun(ctx context.Context, id string) (*Run, error) {
	var r Run
	if err := s.Store.Get(ctx, &r, `SELECT * FROM backup_runs WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("备份记录")
		}
		return nil, err
	}
	return &r, nil
}

// RunFiles 备份在各目标上的文件。
func (s *Service) RunFiles(ctx context.Context, runID string) ([]FileRec, error) {
	out := []FileRec{}
	err := s.Store.Select(ctx, &out, `SELECT * FROM backup_files WHERE run_id = ? ORDER BY target_id`, runID)
	return out, err
}

// RunQuery 是列表条件。
type RunQuery struct {
	WorkspaceID  string
	PlanID       string
	ConnectionID string
	Status       string
	Cursor       string // 上一页最后一条的 ID
	Limit        int
}

// ListRuns 按时间倒序列出备份记录。
func (s *Service) ListRuns(ctx context.Context, q RunQuery) ([]Run, string, error) {
	conds := []string{"workspace_id = ?"}
	args := []any{q.WorkspaceID}
	if q.PlanID != "" {
		conds, args = append(conds, "plan_id = ?"), append(args, q.PlanID)
	}
	if q.ConnectionID != "" {
		conds, args = append(conds, "connection_id = ?"), append(args, q.ConnectionID)
	}
	if q.Status != "" {
		conds, args = append(conds, "status = ?"), append(args, q.Status)
	}
	if q.Cursor != "" {
		conds, args = append(conds, "id < ?"), append(args, q.Cursor)
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	out := []Run{}
	err := s.Store.Select(ctx, &out, `SELECT * FROM backup_runs WHERE `+strings.Join(conds, " AND ")+` ORDER BY id DESC LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) == q.Limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// RunView 给前端的备份记录。
func (s *Service) RunView(ctx context.Context, r *Run, withFiles bool) map[string]any {
	var manifest, verify any
	_ = json.Unmarshal([]byte(r.Manifest), &manifest)
	_ = json.Unmarshal([]byte(r.VerifyDetail), &verify)
	out := map[string]any{
		"id": r.ID, "workspace_id": r.WorkspaceID, "plan_id": r.PlanID, "plan_name": r.PlanName, "connection_id": r.ConnectionID,
		"connection_name": r.ConnectionName, "driver": r.Driver, "trigger": r.Trigger, "attempt": r.Attempt, "status": r.Status,
		"method": r.Method, "tool": r.Tool, "server_version": r.ServerVersion, "databases": r.DatabaseList(), "file_name": r.FileName,
		"compression": r.Compression, "encrypted": r.Encrypted, "size": r.Size, "raw_size": r.RawSize, "sha256": r.SHA256,
		"warnings": jsonStrings(r.Warnings), "error": r.Error, "created_by": r.CreatedBy, "started_at": r.StartedAt, "finished_at": r.FinishedAt,
		"verify_status": r.VerifyStatus, "verify_at": r.VerifyAt, "verify": verify,
	}
	if withFiles {
		out["manifest"] = manifest
		files, _ := s.RunFiles(ctx, r.ID)
		out["files"] = files
	}
	if p := s.progressOf(r.ID); p != nil {
		out["progress"] = p.snapshot()
	}
	return out
}

// deleteRunFiles 删除备份在各目标上的文件与清单，然后删除记录。任一目标删除失败时保留记录（下次重试）。
func (s *Service) deleteRunFiles(ctx context.Context, r *Run) error {
	files, err := s.RunFiles(ctx, r.ID)
	if err != nil {
		return err
	}
	var errs []string
	for _, f := range files {
		if f.Status != "ok" {
			continue
		}
		t, err := s.GetTarget(ctx, f.TargetID)
		if err != nil {
			continue // 目标已删除：文件无从删起，记录照删
		}
		tg, err := s.OpenTarget(ctx, t)
		if err != nil {
			errs = append(errs, t.Name+"："+err.Error())
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		e1 := tg.Delete(dctx, f.Path)
		e2 := tg.Delete(dctx, f.Path+".manifest.json")
		cancel()
		tg.Close()
		if e1 != nil || e2 != nil {
			errs = append(errs, t.Name+"："+errText(errors.Join(e1, e2)))
			continue
		}
		_, _ = s.Store.Exec(ctx, `UPDATE backup_files SET status = 'deleted' WHERE run_id = ? AND target_id = ?`, r.ID, f.TargetID)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	_, err = s.Store.Exec(ctx, `DELETE FROM backup_runs WHERE id = ?`, r.ID)
	return err
}

// DeleteRun 手动删除一份备份（运行中的不能删）。
func (s *Service) DeleteRun(ctx context.Context, r *Run) error {
	if r.Status == StatusRunning {
		return apperr.Conflict("备份正在进行，请先取消")
	}
	if err := s.deleteRunFiles(ctx, r); err != nil {
		return apperr.Wrap("delete_failed", fmt.Errorf("删除备份文件失败：%w", err))
	}
	return nil
}

// okFiles 可读取的文件（preferTarget 非空时它排第一）。
func (s *Service) okFiles(ctx context.Context, r *Run, preferTarget string) ([]FileRec, error) {
	files, err := s.RunFiles(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	var out []FileRec
	for _, f := range files {
		if f.Status != "ok" {
			continue
		}
		if f.TargetID == preferTarget {
			out = append([]FileRec{f}, out...)
		} else {
			out = append(out, f)
		}
	}
	if preferTarget != "" && (len(out) == 0 || out[0].TargetID != preferTarget) {
		return nil, apperr.Bad("该存储目标上没有这份备份")
	}
	if len(out) == 0 {
		return nil, apperr.Bad("这份备份没有可用的文件")
	}
	return out, nil
}

// ---------------- 进度 ----------------

// progress 是一个任务的实时进度（每秒推一次）。
type progress struct {
	Kind         string // backup | restore | verify
	ID           string
	WorkspaceID  string
	ConnectionID string
	PlanID       string
	start        time.Time
	raw          atomic.Int64 // 处理的原始 SQL 字节
	out          atomic.Int64 // 写到目标 / 从目标读的字节
	total        atomic.Int64
	rows         atomic.Int64
	mu           sync.Mutex
	phase        string
	database     string
	table        string
	tablesDone   int
	tablesTotal  int
	statements   int64
	errors       int64
}

func (p *progress) set(fn func(p *progress)) {
	p.mu.Lock()
	fn(p)
	p.mu.Unlock()
}

func (p *progress) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	el := time.Since(p.start).Seconds()
	speed := 0.0
	if el > 0 {
		speed = float64(p.raw.Load()) / el
	}
	return map[string]any{"kind": p.Kind, "id": p.ID, "workspace_id": p.WorkspaceID, "connection_id": p.ConnectionID, "plan_id": p.PlanID,
		"phase": p.phase, "bytes": p.raw.Load(), "written": p.out.Load(), "total": p.total.Load(), "rows": p.rows.Load(),
		"database": p.database, "table": p.table, "tables_done": p.tablesDone, "tables_total": p.tablesTotal,
		"statements": p.statements, "errors": p.errors, "speed": int64(speed), "elapsed_ms": time.Since(p.start).Milliseconds()}
}

// track 登记进度并每秒推送，返回结束函数（结束时再推一次最终状态）。
func (s *Service) track(p *progress) func(final map[string]any) {
	p.start = time.Now()
	s.mu.Lock()
	s.prog[p.ID] = p
	s.mu.Unlock()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.broadcast(p.WorkspaceID, "backup_progress", p.snapshot())
			}
		}
	}()
	return func(final map[string]any) {
		close(stop)
		<-done
		s.mu.Lock()
		delete(s.prog, p.ID)
		s.mu.Unlock()
		snap := p.snapshot()
		for k, v := range final {
			snap[k] = v
		}
		snap["done"] = true
		s.broadcast(p.WorkspaceID, "backup_progress", snap)
	}
}

func (s *Service) progressOf(id string) *progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prog[id]
}

// ---------------- 并发控制 ----------------

// acquire 占用连接（同一连接同一时刻只跑一个任务），返回释放函数；被占用时返回 false。
func (s *Service) acquire(connID, taskID string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.busy[connID]; busy {
		return nil, false
	}
	s.busy[connID] = taskID
	return func() {
		s.mu.Lock()
		if s.busy[connID] == taskID {
			delete(s.busy, connID)
		}
		s.mu.Unlock()
	}, true
}

// Busy 连接上正在运行的任务 ID。
func (s *Service) Busy(connID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[connID]
}

// Cancel 取消运行中的任务（备份、还原、校验）。
func (s *Service) Cancel(taskID string) bool {
	s.mu.Lock()
	c := s.cancels[taskID]
	s.mu.Unlock()
	if c != nil {
		c()
		return true
	}
	return false
}

func (s *Service) setCancel(taskID string, c context.CancelFunc) {
	s.mu.Lock()
	if c == nil {
		delete(s.cancels, taskID)
	} else {
		s.cancels[taskID] = c
	}
	s.mu.Unlock()
}

// recoverInterrupted 启动时把残留的「运行中」记录标为失败（服务重启中断）。
func (s *Service) recoverInterrupted(ctx context.Context) {
	now := store.Now()
	msg := "服务重启，任务中断"
	_, _ = s.Store.Exec(ctx, `UPDATE backup_runs SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running'`, msg, now)
	_, _ = s.Store.Exec(ctx, `UPDATE backup_runs SET verify_status = 'failed', verify_detail = ? WHERE verify_status = 'running'`, toJSON(map[string]any{"error": msg}), now)
	_, _ = s.Store.Exec(ctx, `UPDATE backup_restores SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running'`, msg, now)
}

// fileTargetName 取目标名（已删除时用 ID）。
func (s *Service) fileTargetName(ctx context.Context, id string) string {
	if t, err := s.GetTarget(ctx, id); err == nil {
		return t.Name
	}
	return id
}

