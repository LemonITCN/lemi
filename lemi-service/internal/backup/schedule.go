package backup

import (
	"context"
	"time"
)

// Run 后台调度：启动时把残留的「运行中」标为失败，然后每 30 秒检查到期的计划、失败重试与定时校验。
func (s *Service) Run(ctx context.Context) {
	s.recoverInterrupted(ctx)
	t := time.NewTicker(SchedTick)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick 执行一轮调度（测试可直接调用）。
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	nowMS := now.UnixMilli()
	var plans []Plan
	if err := s.Store.Select(ctx, &plans, `SELECT * FROM backup_plans WHERE enabled = 1 AND (
		(next_run_at > 0 AND next_run_at <= ?) OR (retry_at > 0 AND retry_at <= ?) OR (next_verify_at > 0 AND next_verify_at <= ?))
		ORDER BY next_run_at`, nowMS, nowMS, nowMS); err != nil {
		s.Log.Warn("读取备份计划失败", "err", err)
		return
	}
	for i := range plans {
		p := &plans[i]
		if ctx.Err() != nil {
			return
		}
		backupDue := p.NextRunAt > 0 && p.NextRunAt <= nowMS
		retryDue := p.RetryAt > 0 && p.RetryAt <= nowMS
		if backupDue || retryDue {
			// 连接正忙或全局并发已满：保持到期状态，下一轮再试（同一计划不会堆积多次）
			if s.Busy(p.ConnectionID) != "" || len(s.sem) >= cap(s.sem) {
				continue
			}
			trigger, attempt := "schedule", 0
			if retryDue && !backupDue {
				trigger, attempt = "retry", p.RetryAttempt
			}
			if backupDue {
				// 新的一轮计划开始，之前的重试作废
				_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET next_run_at = ?, retry_at = 0, retry_attempt = 0 WHERE id = ?`, nextAt(p.Spec(), now), p.ID)
			} else {
				_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET retry_at = 0 WHERE id = ?`, p.ID)
			}
			if _, err := s.StartBackup(ctx, p, trigger, attempt, ""); err != nil {
				s.Log.Warn("启动计划备份失败", "plan", p.ID, "err", err)
			}
		}
		if p.NextVerifyAt > 0 && p.NextVerifyAt <= nowMS {
			vr := p.VerifyRule()
			_, _ = s.Store.Exec(ctx, `UPDATE backup_plans SET next_verify_at = ? WHERE id = ?`, nextAt(vr.Schedule, now), p.ID)
			var last Run
			if err := s.Store.Get(ctx, &last, `SELECT * FROM backup_runs WHERE plan_id = ? AND status = 'success' ORDER BY started_at DESC LIMIT 1`, p.ID); err == nil {
				if _, err := s.StartVerify(ctx, &last, ""); err != nil {
					s.Log.Warn("定时校验没有启动", "plan", p.ID, "err", err)
				}
			}
		}
	}
}
