// Package runstate 持有每个上游目标的运行态：冷却、连续失败计数与累计用量。
//
// 这层之所以落在 relay 自己的库里，是因为 model-surge-upstream 是静态配置中心，
// 不承载健康态。键为 model_id 而非 (collection, model_id)：目标健康是全局属性。
package runstate

import (
	"fmt"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
)

// Outcome 是调用方上报的结果类别。
type Outcome string

const (
	// OutcomeNormal 请求成功：累加用量、清零失败计数、解除冷却。
	OutcomeNormal Outcome = "normal"
	// OutcomeAbnormal 目标异常：递增失败计数。
	OutcomeAbnormal Outcome = "abnormal"
	// OutcomeRetrying 调用方换目标重试：同样计入失败。
	OutcomeRetrying Outcome = "retrying"
	// OutcomeInvalidModel 目标模型不可用：同样计入失败。
	OutcomeInvalidModel Outcome = "invalid_model"
	// OutcomeContextExceeded 上下文超限：请求本身太大，与目标健康无关，零变更。
	OutcomeContextExceeded Outcome = "context_exceeded"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeNormal, OutcomeAbnormal, OutcomeRetrying, OutcomeInvalidModel, OutcomeContextExceeded:
		return true
	default:
		return false
	}
}

// countsAsFailure 报告该 outcome 是否递增失败计数。
func (o Outcome) countsAsFailure() bool {
	switch o {
	case OutcomeAbnormal, OutcomeRetrying, OutcomeInvalidModel:
		return true
	default:
		return false
	}
}

type Usage struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	RequestCount    int64 `json:"request_count"`
}

type State struct {
	ModelID             string    `json:"model_id"`
	Cooling             bool      `json:"cooling"`
	CoolingUntil        time.Time `json:"cooling_until,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Usage               Usage     `json:"usage"`
	UpdatedAt           time.Time `json:"updated_at,omitzero"`
}

// ResultReport 是一次结果上报。ReportID 是幂等键。
type ResultReport struct {
	ReportID  string  `json:"report_id"`
	RequestID string  `json:"request_id"`
	ModelID   string  `json:"model_id"`
	Outcome   Outcome `json:"outcome"`
	Usage     Usage   `json:"usage,omitempty"`
}

func (rep ResultReport) validate() error {
	if rep.ReportID == "" {
		return apperr.Field(apperr.InvalidRequest, "report_id", "report id is required")
	}
	if rep.ModelID == "" {
		return apperr.Field(apperr.InvalidRequest, "model_id", "model id is required")
	}
	if !rep.Outcome.valid() {
		return apperr.Field(apperr.InvalidRequest, "outcome",
			fmt.Sprintf("unknown outcome %q", rep.Outcome))
	}
	return nil
}

// Thresholds 是冷却触发参数。
type Thresholds struct {
	// FailureThreshold 连续失败达到该值即置入冷却。<=0 表示从不冷却。
	FailureThreshold int
	// CooldownDuration 冷却时长。
	CooldownDuration time.Duration
}
