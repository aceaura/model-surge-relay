// Package relayv1 定义 model-surge-stream 与本服务之间的 HTTP JSON 契约。
package relayv1

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
)

// 三组路由前缀。健康检查独立于两面且免鉴权：探活不该需要密钥。
const (
	HealthPath       = "/healthz"
	DispatchBasePath = "/v1"
	AdminBasePath    = "/admin"
)

// 入站协议取值，对齐 model-surge-upstream 命名。
const (
	ProtocolAnthropic       = "anthropic"
	ProtocolChatCompletions = "chat_completions"
	ProtocolResponses       = "responses"
	ProtocolGemini          = "gemini"
)

type DispatchRequest struct {
	Model           string   `json:"model"`
	InboundProtocol string   `json:"inbound_protocol"`
	ClientKey       string   `json:"client_key"`
	RequestID       string   `json:"request_id"`
	TriedIDs        []string `json:"tried_ids,omitempty"`
	EstTokens       int      `json:"est_tokens,omitempty"`
}

// Target 是选中 upstream model 的全套信息。Headers 含认证头：
// MarshalJSON 输出原值供调用方直接发请求，String() 脱敏供日志。
//
// Defaults 与 Overrides 是两层未合并的参数：前者缺失才填、后者强制压盖。
// 本服务只搬运不解释，由数据面在编码出上游请求体之后逐层作用上去。
type Target struct {
	ModelID       string            `json:"model_id"`
	Account       string            `json:"account"`
	ProviderID    string            `json:"provider_id"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"base_url"`
	NativeModel   string            `json:"native_model"`
	ContextWindow int               `json:"context_window,omitempty"`
	Headers       map[string]string `json:"headers"`
	Defaults      json.RawMessage   `json:"defaults,omitempty"`
	Overrides     json.RawMessage   `json:"overrides,omitempty"`
}

func TargetFromResolved(t upstreamclient.ResolvedTarget) Target {
	return Target{
		ModelID:       t.ModelID,
		Account:       t.Account,
		ProviderID:    t.ProviderID,
		Protocol:      t.Protocol,
		BaseURL:       t.BaseURL,
		NativeModel:   t.NativeModel,
		ContextWindow: t.ContextWindow,
		Headers:       t.Headers,
		Defaults:      t.Defaults,
		Overrides:     t.Overrides,
	}
}

func (t Target) String() string {
	return fmt.Sprintf("Target{ModelID:%q Account:%q Protocol:%q BaseURL:%q NativeModel:%q Headers:%v}",
		t.ModelID, t.Account, t.Protocol, t.BaseURL, t.NativeModel,
		upstreamclient.RedactHeaders(t.Headers))
}

// 丢弃原因。
const (
	SkipOutOfCollection = "out_of_collection"
	SkipAlreadyTried    = "already_tried"
	SkipDisabled        = "disabled"
	SkipCooling         = "cooling"
	SkipUnknownModel    = "unknown_model"
	SkipResolveFailed   = "resolve_failed"
)

type Skip struct {
	ModelID string `json:"model_id"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail,omitempty"`
}

// Decision 是决策溯源。Policy 为空表示走了兜底顺序。
type Decision struct {
	Policy        string   `json:"policy,omitempty"`
	PolicyVersion int      `json:"policy_version,omitempty"`
	Collection    string   `json:"collection"`
	Group         string   `json:"group"`
	GroupType     string   `json:"group_type"`
	Note          string   `json:"note,omitempty"`
	Candidates    []string `json:"candidates"`
	Skipped       []Skip   `json:"skipped,omitempty"`
}

type DispatchResponse struct {
	RequestID string   `json:"request_id"`
	Target    Target   `json:"target"`
	Decision  Decision `json:"decision"`
}

// String 脱敏整个响应，供日志直接打印。
func (r DispatchResponse) String() string {
	return fmt.Sprintf("DispatchResponse{RequestID:%q Target:%s Decision:%+v}",
		r.RequestID, r.Target, r.Decision)
}

type Usage struct {
	InputTokens     int64 `json:"input_tokens,omitempty"`
	OutputTokens    int64 `json:"output_tokens,omitempty"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
}

type ResultReport struct {
	ReportID  string `json:"report_id"`
	RequestID string `json:"request_id"`
	ModelID   string `json:"model_id"`
	Outcome   string `json:"outcome"`
	Usage     Usage  `json:"usage,omitempty"`
	// RetryAfter 是上游明示的该目标最早可重试时刻（数据面从限流响应头或
	// google.rpc.RetryInfo 解出）。
	//
	// 零值（键不出现）表示上游没说，回落到失败计数启发式。非零时直接冷却
	// 到该时刻，不要求失败计数达阈值：上游的明示比启发式可靠。
	//
	// omitzero 是必需的：旧版本数据面不带这个键，不能因此拒收上报。
	RetryAfter time.Time `json:"retry_after,omitzero"`
}

type ReportResponse struct {
	Applied bool `json:"applied"`
}

type UserModelSummary struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
	Policy     string `json:"policy,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type ModelsResponse struct {
	Models []UserModelSummary `json:"models"`
}

// HealthResponse 的 Ready 恒等于 Database：PG 是权威存储，
// 缓存与上游不可用只降级，不构成摘流理由。
type HealthResponse struct {
	Ready    bool `json:"ready"`
	Database bool `json:"database"`
	Cache    bool `json:"cache"`
	Upstream bool `json:"upstream"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Field     string `json:"field,omitempty"`
}

type ErrorEnvelope struct {
	Error Error `json:"error"`
}
