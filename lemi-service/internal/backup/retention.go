package backup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
)

// Retention 是保留规则（docs/spec.md §12.1）：
//   - count：保留最近 Count 份；
//   - days：保留 Days 天之内的；
//   - gfs：时、日、周、月、年各保留几份（每个时间段保留该段里最新的一份），取并集；
//   - 空：全部保留。
//
// 只按成功的备份计数；新备份 RetentionGrace（1 小时）之内一律不删。
type Retention struct {
	Mode    string `json:"mode"`
	Count   int    `json:"count,omitempty"`
	Days    int    `json:"days,omitempty"`
	Hourly  int    `json:"hourly,omitempty"`
	Daily   int    `json:"daily,omitempty"`
	Weekly  int    `json:"weekly,omitempty"`
	Monthly int    `json:"monthly,omitempty"`
	Yearly  int    `json:"yearly,omitempty"`
}

// Validate 校验规则。
func (r *Retention) Validate() error {
	inRange := func(n, max int) bool { return n >= 0 && n <= max }
	switch r.Mode {
	case "", "none":
		r.Mode = ""
	case "count":
		if r.Count < 1 || r.Count > 10000 {
			return apperr.Bad("保留份数应在 1–10000")
		}
	case "days":
		if r.Days < 1 || r.Days > 36500 {
			return apperr.Bad("保留天数应在 1–36500")
		}
	case "gfs":
		if !inRange(r.Hourly, 1000) || !inRange(r.Daily, 1000) || !inRange(r.Weekly, 1000) || !inRange(r.Monthly, 1000) || !inRange(r.Yearly, 100) {
			return apperr.Bad("GFS 各档份数超出范围")
		}
		if r.Hourly+r.Daily+r.Weekly+r.Monthly+r.Yearly == 0 {
			return apperr.Bad("GFS 至少要有一档保留份数大于 0")
		}
	default:
		return apperr.Bad("未知的保留方式")
	}
	return nil
}

// Item 是参与保留计算的一份成功备份。
type Item struct {
	ID string
	At time.Time
}

// Select 返回要删除的备份 ID。items 是同一计划的成功备份（顺序不限）。
func (r Retention) Select(items []Item, now time.Time, loc *time.Location) []string {
	if r.Mode == "" || len(items) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}
	list := append([]Item(nil), items...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].At.After(list[j].At) }) // 新的在前
	keep := map[string]bool{}
	switch r.Mode {
	case "count":
		for i, it := range list {
			if i < r.Count {
				keep[it.ID] = true
			}
		}
	case "days":
		cut := now.Add(-time.Duration(r.Days) * 24 * time.Hour)
		for _, it := range list {
			if !it.At.Before(cut) {
				keep[it.ID] = true
			}
		}
	case "gfs":
		bucket := func(n int, key func(t time.Time) string) {
			if n <= 0 {
				return
			}
			seen := map[string]bool{}
			for _, it := range list {
				k := key(it.At.In(loc))
				if seen[k] {
					continue
				}
				if len(seen) >= n {
					break
				}
				seen[k] = true
				keep[it.ID] = true // 该时间段里最新的一份
			}
		}
		bucket(r.Hourly, func(t time.Time) string { return t.Format("2006-01-02T15") })
		bucket(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
		bucket(r.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
		bucket(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
		bucket(r.Yearly, func(t time.Time) string { return t.Format("2006") })
	}
	var del []string
	for _, it := range list {
		if keep[it.ID] || now.Sub(it.At) < RetentionGrace {
			continue
		}
		del = append(del, it.ID)
	}
	return del
}

// applyRetention 按计划的保留规则删除过期备份（目标上的文件与记录）。
func (s *Service) applyRetention(ctx context.Context, p *Plan) {
	rule := p.RetentionRule()
	if rule.Mode == "" {
		return
	}
	var runs []Run
	if err := s.Store.Select(ctx, &runs, `SELECT * FROM backup_runs WHERE plan_id = ? AND status = 'success'`, p.ID); err != nil {
		s.Log.Warn("读取备份记录失败", "plan", p.ID, "err", err)
		return
	}
	items := make([]Item, 0, len(runs))
	for _, r := range runs {
		items = append(items, Item{ID: r.ID, At: time.UnixMilli(r.StartedAt)})
	}
	for _, id := range rule.Select(items, s.now(), s.Location) {
		r, err := s.GetRun(ctx, id)
		if err != nil {
			continue
		}
		if err := s.deleteRunFiles(ctx, r); err != nil {
			s.Log.Warn("按保留策略删除备份失败，下次再试", "run", r.ID, "err", err)
			continue
		}
		s.Audit.Record(audit.Entry{WorkspaceID: r.WorkspaceID, ConnectionID: r.ConnectionID, Source: audit.SrcSystem, Action: "backup.retention_delete", Target: r.FileName, OK: true},
			map[string]any{"run_id": r.ID, "plan_id": p.ID, "rule": rule})
	}
}
