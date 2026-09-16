// Package apperr 定义服务的错误码与错误类型。
// 错误码同时决定 HTTP 状态与可重试性，避免调用点各自判断。
package apperr

import "net/http"

type Code string

const (
	Unauthorized      Code = "unauthorized"
	NotFound          Code = "not_found"
	InvalidRequest    Code = "invalid_request"
	Conflict          Code = "conflict"
	Disabled          Code = "disabled"
	TargetUnavailable Code = "target_unavailable"
	PolicyError       Code = "policy_error"
	PolicyTimeout     Code = "policy_timeout"
	Internal          Code = "internal_error"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	// Retryable 由 Code 决定，序列化出去供调用方判断是否换目标重试。
	Retryable bool   `json:"retryable"`
	Field     string `json:"field,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable(code)}
}

func Field(code Code, field, message string) *Error {
	e := New(code, message)
	e.Field = field
	return e
}

// retryable 只有"换个目标或稍后重试可能成功"的失败为真。
// PolicyError 是脚本 bug，重试必然再失败，故为假。
func retryable(code Code) bool {
	switch code {
	case TargetUnavailable, PolicyTimeout, Internal:
		return true
	default:
		return false
	}
}

func Status(code Code) int {
	switch code {
	case Unauthorized:
		return http.StatusUnauthorized
	case NotFound:
		return http.StatusNotFound
	case InvalidRequest:
		return http.StatusBadRequest
	case Conflict:
		return http.StatusConflict
	case Disabled:
		return http.StatusForbidden
	case TargetUnavailable, PolicyTimeout:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// From 提取 *Error；非本包错误统一归为 internal_error，
// 避免底层错误细节泄露到响应里。
func From(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return New(Internal, err.Error())
}
