package sched

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	base := time.Date(2026, 1, 31, 10, 30, 0, 0, loc)
	cases := []struct {
		s    Spec
		want string
	}{
		{Spec{Kind: "hourly", Minute: 15, Timezone: "Asia/Shanghai"}, "2026-01-31 11:15"},
		{Spec{Kind: "daily", Time: "04:00", Timezone: "Asia/Shanghai"}, "2026-02-01 04:00"},
		{Spec{Kind: "daily", Time: "10:31", Timezone: "Asia/Shanghai"}, "2026-01-31 10:31"},
		{Spec{Kind: "weekly", Time: "09:00", Weekdays: []int{1}, Timezone: "Asia/Shanghai"}, "2026-02-02 09:00"},
		{Spec{Kind: "monthly", Day: 31, Time: "02:00", Timezone: "Asia/Shanghai"}, "2026-02-28 02:00"},
		{Spec{Kind: "cron", Cron: "*/20 * * * *", Timezone: "Asia/Shanghai"}, "2026-01-31 10:40"},
		{Spec{Kind: "cron", Cron: "0 3 * * 1-5", Timezone: "Asia/Shanghai"}, "2026-02-02 03:00"},
		{Spec{Kind: "cron", Cron: "0 0 1 */3 *", Timezone: "Asia/Shanghai"}, "2026-04-01 00:00"},
		{Spec{Kind: "cron", Cron: "30 8 * * sun", Timezone: "Asia/Shanghai"}, "2026-02-01 08:30"},
	}
	for _, c := range cases {
		if err := c.s.Validate(); err != nil {
			t.Fatalf("%+v：%v", c.s, err)
		}
		got := c.s.Next(base).Format("2006-01-02 15:04")
		if got != c.want {
			t.Errorf("%+v：得到 %s，应为 %s", c.s, got, c.want)
		}
	}
	if (&Spec{Kind: "cron", Cron: "0 0 30 2 *"}).Validate() == nil {
		t.Error("2 月 30 日永远不会触发，应报错")
	}
	if (&Spec{Kind: "cron", Cron: "61 * * * *"}).Validate() == nil {
		t.Error("分钟越界应报错")
	}
}
