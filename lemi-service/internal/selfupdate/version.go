package selfupdate

import (
	"strconv"
	"strings"
)

// Compare 比较两个版本号，a < b 返回 -1，相等返回 0，a > b 返回 1。
// 格式：主.次.修订[-预发布][+构建信息]，前面可带 v；数字段可多可少（缺的按 0）。
// 带预发布标记的版本比同号正式版旧（0.2.0-dev < 0.2.0），预发布标记按点分段比较（数字段按数值）。
// 无法解析的版本一律视为最旧，避免把乱写的标签当成新版本。
func Compare(a, b string) int {
	an, ap, aok := parseVersion(a)
	bn, bp, bok := parseVersion(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := 0; i < len(an) || i < len(bn); i++ {
		x, y := at(an, i), at(bn, i)
		if x != y {
			return sign(x - y)
		}
	}
	switch {
	case len(ap) == 0 && len(bp) == 0:
		return 0
	case len(ap) == 0:
		return 1
	case len(bp) == 0:
		return -1
	}
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if c := comparePre(ap[i], bp[i]); c != 0 {
			return c
		}
	}
	return sign(len(ap) - len(bp))
}

// ValidVersion 版本号能否解析。
func ValidVersion(v string) bool {
	_, _, ok := parseVersion(v)
	return ok
}

func parseVersion(s string) (nums []int, pre []string, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		if s[i+1:] == "" {
			return nil, nil, false
		}
		pre = strings.Split(s[i+1:], ".")
	}
	if core == "" {
		return nil, nil, false
	}
	for _, p := range strings.Split(core, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 9 {
			return nil, nil, false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}

func comparePre(a, b string) int {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return sign(x - y)
	case errA == nil:
		return -1 // 数字段比字母段旧（与 semver 一致）
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
