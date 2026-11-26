package health

import (
	"context"
	"sort"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
)

// Bucket 是时间线上的一格（一小时或一天）。延迟只统计成功的检查（失败的检查耗时通常是超时，没有意义）。
type Bucket struct {
	Start        int64    `json:"start"` // Unix 毫秒（含）
	End          int64    `json:"end"`   // Unix 毫秒（不含）
	Checks       int      `json:"checks"`
	Failures     int      `json:"failures"`
	Uptime       *float64 `json:"uptime"`         // 成功次数 / 检查次数（0–1）；没有检查时为 null
	AvgLatencyMS *int64   `json:"avg_latency_ms"` // 没有成功的检查时为 null
	P95LatencyMS *int64   `json:"p95_latency_ms"`
	MaxLatencyMS *int64   `json:"max_latency_ms"`
	Status       string   `json:"status"` // none（没有检查）| up（全部成功）| down（全部失败）| partial
}

// Timeline 是一个连接在一段时间里的可用率与延迟。
type Timeline struct {
	Range    string   `json:"range"` // day | 7d | 30d
	Step     string   `json:"step"`  // hour | day
	TZ       string   `json:"tz"`
	Buckets  []Bucket `json:"buckets"`
	Summary  Bucket   `json:"summary"`
	Interval int      `json:"interval_sec"`
}

type checkPoint struct {
	At        int64 `db:"at"`
	OK        bool  `db:"ok"`
	LatencyMS int64 `db:"latency_ms"`
}

// timelineBounds 范围的格子边界：day = 今天 0 点起 24 个小时；7d / 30d = 含今天在内的 7 / 30 个自然日。
func timelineBounds(rng string, now time.Time, loc *time.Location) ([]time.Time, string, error) {
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	var out []time.Time
	switch rng {
	case "", "day":
		for h := 0; h <= 24; h++ {
			out = append(out, time.Date(today.Year(), today.Month(), today.Day(), h, 0, 0, 0, loc))
		}
		return out, "hour", nil
	case "7d", "30d":
		n := 7
		if rng == "30d" {
			n = 30
		}
		for d := -(n - 1); d <= 1; d++ {
			out = append(out, time.Date(today.Year(), today.Month(), today.Day()+d, 0, 0, 0, 0, loc))
		}
		return out, "day", nil
	}
	return nil, "", apperr.Bad("range 只能是 day、7d 或 30d")
}

// aggregate 把按时间排序的检查记录分到格子里，并算出整段汇总。
func aggregate(points []checkPoint, bounds []time.Time) ([]Bucket, Bucket) {
	buckets := make([]Bucket, 0, len(bounds)-1)
	var allLat []int64
	var sum Bucket
	if len(bounds) >= 2 {
		sum.Start, sum.End = bounds[0].UnixMilli(), bounds[len(bounds)-1].UnixMilli()
	}
	i := 0
	for b := 0; b+1 < len(bounds); b++ {
		start, end := bounds[b].UnixMilli(), bounds[b+1].UnixMilli()
		bk := Bucket{Start: start, End: end}
		var lat []int64
		for i < len(points) && points[i].At < start {
			i++
		}
		for ; i < len(points) && points[i].At < end; i++ {
			p := points[i]
			bk.Checks++
			if p.OK {
				lat = append(lat, p.LatencyMS)
			} else {
				bk.Failures++
			}
		}
		fill(&bk, lat)
		allLat = append(allLat, lat...)
		sum.Checks += bk.Checks
		sum.Failures += bk.Failures
		buckets = append(buckets, bk)
	}
	fill(&sum, allLat)
	return buckets, sum
}

func fill(b *Bucket, lat []int64) {
	switch {
	case b.Checks == 0:
		b.Status = "none"
	case b.Failures == 0:
		b.Status = "up"
	case b.Failures == b.Checks:
		b.Status = "down"
	default:
		b.Status = "partial"
	}
	if b.Checks > 0 {
		u := float64(b.Checks-b.Failures) / float64(b.Checks)
		b.Uptime = &u
	}
	if len(lat) == 0 {
		return
	}
	sorted := append([]int64(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total int64
	for _, v := range sorted {
		total += v
	}
	avg := total / int64(len(sorted))
	idx := (len(sorted)*95+99)/100 - 1 // ceil(0.95n) - 1
	if idx < 0 {
		idx = 0
	}
	p95, mx := sorted[idx], sorted[len(sorted)-1]
	b.AvgLatencyMS, b.P95LatencyMS, b.MaxLatencyMS = &avg, &p95, &mx
}

// Timeline 连接的时间线。loc 为空时用服务的时区。
func (s *Service) Timeline(ctx context.Context, connID, rng string, loc *time.Location) (*Timeline, error) {
	if loc == nil {
		loc = s.loc
	}
	bounds, step, err := timelineBounds(rng, s.now(), loc)
	if err != nil {
		return nil, err
	}
	if rng == "" {
		rng = "day"
	}
	var points []checkPoint
	if err := s.st.Select(ctx, &points, `SELECT at, ok, latency_ms FROM health_checks WHERE connection_id = ? AND at >= ? AND at < ? ORDER BY at`,
		connID, bounds[0].UnixMilli(), bounds[len(bounds)-1].UnixMilli()); err != nil {
		return nil, err
	}
	buckets, sum := aggregate(points, bounds)
	cfg, _, _ := s.Config(ctx, connID)
	return &Timeline{Range: rng, Step: step, TZ: loc.String(), Buckets: buckets, Summary: sum, Interval: cfg.IntervalSec}, nil
}

// CheckRecord 是一条原始检查记录。
type CheckRecord struct {
	ID        int64  `db:"id" json:"id"`
	At        int64  `db:"at" json:"at"`
	OK        bool   `db:"ok" json:"ok"`
	LatencyMS int64  `db:"latency_ms" json:"latency_ms"`
	Error     string `db:"error" json:"error"`
	Status    string `db:"status" json:"status"`
	Manual    bool   `db:"manual" json:"manual"`
}

// Checks 原始检查记录（按时间倒序）。cursor 为上一页最后一条的 ID；onlyFailed 只看失败的。
func (s *Service) Checks(ctx context.Context, connID string, cursor int64, limit int, onlyFailed bool) ([]CheckRecord, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, at, ok, latency_ms, error, status, manual FROM health_checks WHERE connection_id = ?`
	args := []any{connID}
	if cursor > 0 {
		q += ` AND id < ?`
		args = append(args, cursor)
	}
	if onlyFailed {
		q += ` AND ok = 0`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	out := []CheckRecord{}
	if err := s.st.Select(ctx, &out, q, args...); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
