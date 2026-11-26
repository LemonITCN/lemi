// Package sched 解析计划表达式并计算下一次运行时间：每小时 / 每天 / 每周 / 每月 / cron（5 段），支持时区。
// 备份计划与定时智能体任务共用。
package sched

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec 是一个计划。
type Spec struct {
	Kind     string `json:"kind"`               // hourly | daily | weekly | monthly | cron | manual
	Minute   int    `json:"minute,omitempty"`   // hourly：每小时第几分
	Time     string `json:"time,omitempty"`     // daily/weekly/monthly：HH:MM
	Weekdays []int  `json:"weekdays,omitempty"` // weekly：0=周日 … 6=周六
	Day      int    `json:"day,omitempty"`      // monthly：1–31（当月没有这一天时取当月最后一天）
	Cron     string `json:"cron,omitempty"`     // cron：分 时 日 月 周
	Timezone string `json:"timezone,omitempty"` // IANA 时区，空 = 服务器时区
}

// Parse 从 JSON 解析并校验。
func Parse(raw string) (*Spec, error) {
	var s Spec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, errors.New("计划格式不正确")
	}
	return &s, s.Validate()
}

func parseHM(s string) (int, int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, errors.New("时间格式应为 HH:MM")
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, errors.New("时间格式应为 HH:MM")
	}
	return hh, mm, nil
}

// Location 计划使用的时区。
func (s *Spec) Location() *time.Location {
	if s.Timezone != "" {
		if loc, err := time.LoadLocation(s.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// Validate 校验。
func (s *Spec) Validate() error {
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("未知的时区 %q", s.Timezone)
		}
	}
	switch s.Kind {
	case "manual":
		return nil
	case "hourly":
		if s.Minute < 0 || s.Minute > 59 {
			return errors.New("分钟应在 0–59")
		}
	case "daily":
		_, _, err := parseHM(s.Time)
		return err
	case "weekly":
		if _, _, err := parseHM(s.Time); err != nil {
			return err
		}
		if len(s.Weekdays) == 0 {
			return errors.New("请至少选择一天")
		}
		for _, d := range s.Weekdays {
			if d < 0 || d > 6 {
				return errors.New("星期取值应在 0–6")
			}
		}
	case "monthly":
		if _, _, err := parseHM(s.Time); err != nil {
			return err
		}
		if s.Day < 1 || s.Day > 31 {
			return errors.New("日期应在 1–31")
		}
	case "cron":
		c, err := parseCron(s.Cron)
		if err != nil {
			return err
		}
		if c.next(time.Now().In(s.Location())).IsZero() {
			return errors.New("这个 cron 表达式永远不会触发")
		}
	default:
		return errors.New("未知的计划类型")
	}
	return nil
}

// Next 返回 after 之后的下一次运行时间（manual 返回零值）。
func (s *Spec) Next(after time.Time) time.Time {
	loc := s.Location()
	t := after.In(loc)
	switch s.Kind {
	case "hourly":
		n := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), s.Minute, 0, 0, loc)
		if !n.After(t) {
			n = n.Add(time.Hour)
		}
		return n
	case "daily":
		h, m, _ := parseHM(s.Time)
		n := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, loc)
		if !n.After(t) {
			n = time.Date(t.Year(), t.Month(), t.Day()+1, h, m, 0, 0, loc)
		}
		return n
	case "weekly":
		h, m, _ := parseHM(s.Time)
		days := map[int]bool{}
		for _, d := range s.Weekdays {
			days[d] = true
		}
		for i := 0; i < 8; i++ {
			d := time.Date(t.Year(), t.Month(), t.Day()+i, h, m, 0, 0, loc)
			if days[int(d.Weekday())] && d.After(t) {
				return d
			}
		}
	case "monthly":
		h, m, _ := parseHM(s.Time)
		for i := 0; i < 13; i++ {
			first := time.Date(t.Year(), t.Month()+time.Month(i), 1, h, m, 0, 0, loc)
			last := first.AddDate(0, 1, -1).Day()
			day := s.Day
			if day > last {
				day = last
			}
			d := time.Date(first.Year(), first.Month(), day, h, m, 0, 0, loc)
			if d.After(t) {
				return d
			}
		}
	case "cron":
		c, err := parseCron(s.Cron)
		if err == nil {
			return c.next(t)
		}
	}
	return time.Time{}
}

// Describe 中文描述（界面与通知用）。
func (s *Spec) Describe() string {
	wd := []string{"日", "一", "二", "三", "四", "五", "六"}
	switch s.Kind {
	case "manual":
		return "手动"
	case "hourly":
		return fmt.Sprintf("每小时第 %d 分", s.Minute)
	case "daily":
		return "每天 " + s.Time
	case "weekly":
		var ds []string
		for _, d := range s.Weekdays {
			ds = append(ds, "周"+wd[d])
		}
		return "每" + strings.Join(ds, "、") + " " + s.Time
	case "monthly":
		return fmt.Sprintf("每月 %d 日 %s", s.Day, s.Time)
	case "cron":
		return "cron：" + s.Cron
	}
	return ""
}

// ---------- cron ----------

type cron struct {
	min, hour, dom, mon, dow uint64
	domStar, dowStar         bool
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dowNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

func parseField(f string, lo, hi int, names map[string]int) (uint64, bool, error) {
	var bits uint64
	star := f == "*" || f == "?"
	for _, part := range strings.Split(f, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("步长不正确：%s", part)
			}
			step, part = n, part[:i]
		}
		a, b := lo, hi
		if part != "*" && part != "?" {
			x, y, isRange := strings.Cut(part, "-")
			v1, err := atoiName(x, names)
			if err != nil {
				return 0, false, err
			}
			a, b = v1, v1
			if isRange {
				v2, err := atoiName(y, names)
				if err != nil {
					return 0, false, err
				}
				b = v2
			} else if step > 1 {
				b = hi
			}
		}
		if hi == 7 && b == 7 { // 周日既可写 0 也可写 7
			b = 6
			bits |= 1
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("取值超出范围：%s", part)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, star, nil
}

func atoiName(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("无法识别 %q", s)
	}
	return n, nil
}

func parseCron(expr string) (*cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, errors.New("cron 表达式需要 5 段：分 时 日 月 周")
	}
	c := &cron{}
	var err error
	if c.min, _, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, err
	}
	if c.hour, _, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, err
	}
	if c.dom, c.domStar, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, err
	}
	if c.mon, _, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, err
	}
	if c.dow, c.dowStar, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cron) dayMatch(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow // 标准 cron：两者都限定时满足其一即可
}

// next 逐分钟推进（按月、日、时跳跃），最多找 5 年。
func (c *cron) next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.mon&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !c.dayMatch(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if c.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if c.min&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
