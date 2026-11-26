package agent

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
)

// Clock 是可注入的时钟（测试里用假时钟验证退避，不真的等待）。
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RetryPolicy 是模型请求的重试策略（docs/spec.md §16.2）。
type RetryPolicy struct {
	Base          time.Duration // 首次等待 2 秒
	MaxDelay      time.Duration // 单次封顶 10 秒
	MaxAttempts   int           // 最多 10 次
	HeaderCap     time.Duration // retry-after 封顶 15 分钟
	NetworkBudget time.Duration // DNS / 网络不可达单独计预算：最长 1 小时
	Jitter        func() float64
}

// DefaultRetry 默认策略。
var DefaultRetry = RetryPolicy{Base: 2 * time.Second, MaxDelay: 10 * time.Second, MaxAttempts: 10, HeaderCap: 15 * time.Minute, NetworkBudget: time.Hour}

// retryState 记录一次请求的重试进度。
type retryState struct {
	attempts   int       // 普通可重试错误的次数
	netAttempt int       // 网络不可达的次数
	netSince   time.Time // 第一次网络不可达的时间
}

// next 判断是否重试、等多久。
func (p RetryPolicy) next(e *llm.APIError, st *retryState, now time.Time) (time.Duration, bool) {
	if e.Kind == llm.ErrCanceled || e.Kind == llm.ErrContextOverflow {
		return 0, false
	}
	retry := e.Kind.Retryable()
	if e.ShouldRetry != nil {
		retry = *e.ShouldRetry
	}
	if !retry {
		return 0, false
	}
	var n int
	if e.Kind == llm.ErrNetworkDown {
		if st.netSince.IsZero() {
			st.netSince = now
		}
		if now.Sub(st.netSince) >= p.NetworkBudget {
			return 0, false
		}
		st.netAttempt++
		n = st.netAttempt
	} else {
		st.attempts++
		if st.attempts > p.MaxAttempts {
			return 0, false
		}
		n = st.attempts
	}
	if e.RetryAfterMS > 0 {
		d := time.Duration(e.RetryAfterMS) * time.Millisecond
		if d > p.HeaderCap {
			d = p.HeaderCap
		}
		return d, true
	}
	d := p.Base
	for i := 1; i < n && d < p.MaxDelay; i++ {
		d *= 2
	}
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	j := 0.0
	if p.Jitter != nil {
		j = p.Jitter()
	} else {
		j = rand.Float64()
	}
	// 抖动：在 [0.8d, d] 之间，不超过封顶
	d = time.Duration(float64(d) * (0.8 + 0.2*j))
	return d, true
}
