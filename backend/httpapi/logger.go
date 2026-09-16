package httpapi

import (
	"log/slog"

	"github.com/aceaura/model-surge-relay/backend/dispatch"
)

// SlogLogger 把调度结果写成结构化日志。
//
// 这里只消费 dispatch.LogEntry，而该结构里没有任何凭据、认证头或客户端密钥字段，
// 所以"日志不含凭据"是类型层面的保证，不依赖本函数写得小心。
type SlogLogger struct {
	Log *slog.Logger
}

func (l SlogLogger) Dispatched(e dispatch.LogEntry) {
	logger := l.Log
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"request_id", e.RequestID,
		"user_model", e.UserModel,
		"candidates", e.Candidates,
		"skipped", skipReasons(e),
		"duration_ms", e.Duration.Milliseconds(),
	}
	if e.Policy != "" {
		attrs = append(attrs, "policy", e.Policy, "policy_version", e.PolicyVersion)
	}
	if e.Error != "" {
		attrs = append(attrs, "error", e.Error)
		logger.Error("dispatch failed", attrs...)
		return
	}
	attrs = append(attrs, "selected", e.Selected)
	logger.Info("dispatched", attrs...)
}

// skipReasons 渲染为 "model_id:reason" 列表，保持单行可读。
func skipReasons(e dispatch.LogEntry) []string {
	out := make([]string, 0, len(e.Skipped))
	for _, s := range e.Skipped {
		out = append(out, s.ModelID+":"+s.Reason)
	}
	return out
}
