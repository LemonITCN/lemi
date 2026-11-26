// Package apperr 定义可以直接展示给用户的错误。各模块返回它，HTTP 层按 Status 与 Code 输出；
// 其他错误一律当内部错误记日志，只给用户「服务器内部错误」，避免泄露内部细节。
package apperr

import (
	"errors"
	"fmt"
)

// Error 是面向用户的错误。Code 是稳定的英文标识（前端据此翻译），Message 是中文说明。
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Extra   any    `json:"extra,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// New 创建错误。
func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Bad 请求不正确（400）。
func Bad(format string, a ...any) *Error {
	return &Error{Status: 400, Code: "bad_request", Message: fmt.Sprintf(format, a...)}
}

// NotFound 不存在（404）。
func NotFound(what string) *Error {
	return &Error{Status: 404, Code: "not_found", Message: what + "不存在"}
}

// Forbidden 没有权限（403）。
func Forbidden(msg string) *Error {
	if msg == "" {
		msg = "没有权限"
	}
	return &Error{Status: 403, Code: "forbidden", Message: msg}
}

// Conflict 冲突（409）。
func Conflict(msg string) *Error { return &Error{Status: 409, Code: "conflict", Message: msg} }

// Wrap 把底层错误（例如数据库返回的语法错误）原样作为用户可见的 400 错误。
func Wrap(code string, err error) *Error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return &Error{Status: 400, Code: code, Message: err.Error()}
}

// As 判断是否是面向用户的错误。
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
