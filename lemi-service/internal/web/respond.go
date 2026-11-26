package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// HandlerFunc 返回错误的处理函数。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// H 把返回错误的处理函数包装成 http.HandlerFunc。
func (s *Server) H(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.WriteError(w, r, err)
		}
	}
}

// WriteError 输出错误。只有 apperr.Error 与已知错误会把原因给用户，其他一律记日志、回「服务器内部错误」。
func (s *Server) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apperr.Error
	var locked *auth.ErrLocked
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &locked):
		ae = &apperr.Error{Status: 429, Code: "too_many_attempts", Message: err.Error(), Extra: map[string]any{"retry_after": int(locked.RetryAfter.Seconds()) + 1}}
	case errors.Is(err, auth.ErrBadCredentials):
		ae = apperr.New(401, "bad_credentials", err.Error())
	case errors.Is(err, auth.ErrBadCode):
		ae = apperr.New(401, "bad_code", err.Error())
	case errors.Is(err, auth.ErrDisabled):
		ae = apperr.New(403, "account_disabled", err.Error())
	case errors.Is(err, auth.ErrSessionInvalid):
		ae = apperr.New(401, "session_invalid", err.Error())
	case errors.Is(err, store.ErrNotFound):
		ae = apperr.NotFound("记录")
	case errors.Is(err, accounts.ErrForbidden):
		ae = apperr.Forbidden("")
	case errors.Is(err, accounts.ErrNotMember):
		ae = apperr.NotFound("工作空间")
	case errors.Is(err, accounts.ErrLastOwner), errors.Is(err, accounts.ErrNameInvalid):
		ae = apperr.Bad("%s", err.Error())
	case errors.As(err, &maxErr):
		ae = apperr.New(413, "too_large", "请求内容太大")
	default:
		s.Log.Error("请求处理失败", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = apperr.New(500, "internal", "服务器内部错误")
	}
	if ae.Status == 0 {
		ae.Status = 400
	}
	WriteJSON(w, ae.Status, map[string]any{"error": ae})
}

// WriteJSON 输出 JSON。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

// OK 输出 200 JSON。
func OK(w http.ResponseWriter, v any) error {
	WriteJSON(w, 200, v)
	return nil
}

// ReadJSON 解析请求体 JSON（拒绝未知类型的语法错误，返回用户可读的错误）。
func ReadJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apperr.New(413, "too_large", "请求内容太大")
		}
		if errors.Is(err, io.EOF) {
			return apperr.Bad("请求内容为空")
		}
		return apperr.Bad("请求格式不正确：%v", err)
	}
	return nil
}

// QueryInt 读取整数查询参数。
func QueryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// QueryInt64 读取 64 位整数查询参数。
func QueryInt64(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}
