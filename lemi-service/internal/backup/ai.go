package backup

import (
	"context"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
)

// 智能体与 MCP 的只读工具 list_backups / backup_status / health_status（实现 tools.BackupInfo）。
// connIDs 已由调用方按用户与智能体权限过滤，这里只再限定工作空间。

// HealthStatus 是健康检查服务提供的当前状态（health.Service 实现它）。
type HealthStatus interface {
	Status(ctx context.Context, connID string) (status string, lastCheckAt int64, latencyMS int64)
}

func (s *Service) fmtTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(s.Location).Format("2006-01-02 15:04:05 MST")
}

func inClause(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Service) connNames(ctx context.Context, wsID string, connIDs []string) map[string]string {
	out := map[string]string{}
	for _, id := range connIDs {
		if c, err := s.Conns.Get(ctx, id); err == nil && c.WorkspaceID == wsID {
			out[id] = c.Name
		}
	}
	return out
}

// AIListBackups 最近的备份记录。
func (s *Service) AIListBackups(ctx context.Context, u *accounts.User, wsID string, connIDs []string, limit int) (any, error) {
	if len(connIDs) == 0 {
		return map[string]any{"backups": []any{}}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	args := []any{wsID}
	for _, id := range connIDs {
		args = append(args, id)
	}
	var runs []Run
	if err := s.Store.Select(ctx, &runs, `SELECT * FROM backup_runs WHERE workspace_id = ? AND connection_id IN (`+inClause(len(connIDs))+`)
		ORDER BY started_at DESC LIMIT ?`, append(args, limit)...); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range runs {
		item := map[string]any{"id": r.ID, "connection_id": r.ConnectionID, "connection": r.ConnectionName, "plan": r.PlanName, "trigger": r.Trigger,
			"status": r.Status, "method": r.Method, "tool": r.Tool, "databases": r.DatabaseList(), "file": r.FileName,
			"size": humanBytes(r.Size), "size_bytes": r.Size, "encrypted": r.Encrypted, "started_at": s.fmtTime(r.StartedAt), "finished_at": s.fmtTime(r.FinishedAt)}
		if r.VerifyStatus != "" {
			item["verify"] = r.VerifyStatus
		}
		if r.Error != "" {
			item["error"] = r.Error
		}
		out = append(out, item)
	}
	return map[string]any{"backups": out}, nil
}

// AIBackupStatus 备份计划与最近一次结果。
func (s *Service) AIBackupStatus(ctx context.Context, u *accounts.User, wsID string, connIDs []string) (any, error) {
	names := s.connNames(ctx, wsID, connIDs)
	plansOut := []map[string]any{}
	for _, id := range connIDs {
		if _, ok := names[id]; !ok {
			continue
		}
		plans, err := s.ListPlans(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		for i := range plans {
			p := &plans[i]
			var tnames []string
			for _, tid := range p.TargetIDs() {
				tnames = append(tnames, s.fileTargetName(ctx, tid))
			}
			item := map[string]any{"plan": p.Name, "connection_id": id, "connection": names[id], "enabled": p.Enabled, "method": p.Method,
				"databases": p.DatabaseList(), "schedule": p.Spec().Describe(), "next_run": s.fmtTime(p.NextRunAt), "encrypted": p.Encrypt,
				"compression": p.Compression, "targets": tnames, "retention": p.RetentionRule(), "verify_after_backup": p.VerifyRule().AfterBackup}
			if p.RetryAt > 0 {
				item["retry_at"] = s.fmtTime(p.RetryAt)
				item["retry_attempt"] = p.RetryAttempt
			}
			if busy := s.Busy(id); busy != "" {
				item["running"] = true
			}
			var last Run
			if err := s.Store.Get(ctx, &last, `SELECT * FROM backup_runs WHERE plan_id = ? ORDER BY started_at DESC LIMIT 1`, p.ID); err == nil {
				lr := map[string]any{"status": last.Status, "started_at": s.fmtTime(last.StartedAt), "size": humanBytes(last.Size)}
				if last.Error != "" {
					lr["error"] = last.Error
				}
				if last.VerifyStatus != "" {
					lr["verify"] = last.VerifyStatus
				}
				item["last_run"] = lr
			}
			var ok Run
			if err := s.Store.Get(ctx, &ok, `SELECT * FROM backup_runs WHERE plan_id = ? AND status = 'success' ORDER BY started_at DESC LIMIT 1`, p.ID); err == nil {
				item["last_success"] = s.fmtTime(ok.StartedAt)
			}
			plansOut = append(plansOut, item)
		}
	}
	return map[string]any{"plans": plansOut}, nil
}

// AIHealthStatus 健康检查状态（当前状态、最近延迟、24 小时可用率、最近的错误）。
func (s *Service) AIHealthStatus(ctx context.Context, u *accounts.User, wsID string, connIDs []string) (any, error) {
	names := s.connNames(ctx, wsID, connIDs)
	out := []map[string]any{}
	since := s.now().Add(-24 * time.Hour).UnixMilli()
	for _, id := range connIDs {
		name, ok := names[id]
		if !ok {
			continue
		}
		item := map[string]any{"connection_id": id, "connection": name, "status": "unknown"}
		if s.Health != nil {
			st, at, lat := s.Health.Status(ctx, id)
			item["status"], item["last_check"], item["latency_ms"] = st, s.fmtTime(at), lat
		}
		var row struct {
			Checks int64 `db:"checks"`
			OK     int64 `db:"ok"`
		}
		if err := s.Store.Get(ctx, &row, `SELECT COUNT(*) AS checks, COALESCE(SUM(ok), 0) AS ok FROM health_checks WHERE connection_id = ? AND at >= ?`, id, since); err == nil && row.Checks > 0 {
			item["uptime_24h"] = float64(row.OK*10000/row.Checks) / 100
			item["checks_24h"] = row.Checks
		}
		var lastErr struct {
			At    int64  `db:"at"`
			Error string `db:"error"`
		}
		if err := s.Store.Get(ctx, &lastErr, `SELECT at, error FROM health_checks WHERE connection_id = ? AND ok = 0 ORDER BY at DESC LIMIT 1`, id); err == nil {
			item["last_error"] = lastErr.Error
			item["last_error_at"] = s.fmtTime(lastErr.At)
		}
		out = append(out, item)
	}
	return map[string]any{"connections": out}, nil
}
