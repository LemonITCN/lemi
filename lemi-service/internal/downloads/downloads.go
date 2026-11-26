// Package downloads 提供一次性下载链接：导出、备份下载等先登记一个生成函数拿到令牌，浏览器再用 GET 链接原生下载（不经前端内存）。
// 令牌 5 分钟有效、只能用一次、只能由登记它的用户使用。
package downloads

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Job 是一个待下载的内容。
type Job struct {
	UserID      string
	Filename    string
	ContentType string
	// Size 已知大小时填写（-1 未知）
	Size    int64
	Write   func(ctx context.Context, w io.Writer) error
	expires time.Time
}

// Store 令牌表。
type Store struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// New 创建。
func New() *Store { return &Store{jobs: map[string]*Job{}} }

// Add 登记下载，返回令牌。
func (s *Store) Add(j *Job) string {
	tok := ids.RandomToken(40)
	j.expires = time.Now().Add(5 * time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.jobs {
		if time.Now().After(v.expires) {
			delete(s.jobs, k)
		}
	}
	s.jobs[tok] = j
	return tok
}

func (s *Store) take(tok, userID string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[tok]
	if j == nil || j.UserID != userID || time.Now().After(j.expires) {
		return nil
	}
	delete(s.jobs, tok)
	return j
}

// Routes 挂载 GET /downloads/{token}。
func (s *Store) Routes(srv *web.Server, r chi.Router) {
	r.Get("/downloads/{token}", srv.H(func(w http.ResponseWriter, r *http.Request) error {
		j := s.take(chi.URLParam(r, "token"), web.UserOf(r).ID)
		if j == nil {
			return apperr.New(404, "download_expired", "下载链接已失效，请重新导出")
		}
		ct := j.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": sanitize(j.Filename)}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		if j.Size > 0 {
			w.Header().Set("Content-Length", itoa(j.Size))
		}
		w.WriteHeader(200)
		// 写到一半出错时连接直接断开，浏览器会显示下载失败（不能再改状态码）
		if err := j.Write(r.Context(), w); err != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	}))
}

func sanitize(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" {
		return "download"
	}
	return name
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
