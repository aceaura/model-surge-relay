package upstreamclient

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResolvedTarget 是下发面返回的目标全套信息。Headers 含认证头，
// 因此 String() 脱敏而 MarshalJSON 输出原值——响应带凭据、日志不带凭据
// 由类型本身保证，不依赖调用点自觉。
type ResolvedTarget struct {
	ModelID       string            `json:"model_id"`
	Account       string            `json:"account"`
	ProviderID    string            `json:"provider_id"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"base_url"`
	NativeModel   string            `json:"native_model"`
	ContextWindow int               `json:"context_window,omitempty"`
	Headers       map[string]string `json:"headers"`
	Params        json.RawMessage   `json:"params,omitempty"`
}

// sensitiveHeaders 是日志脱敏时需要遮蔽的头（大小写不敏感比较）。
var sensitiveHeaders = map[string]bool{
	"authorization":  true,
	"x-api-key":      true,
	"x-goog-api-key": true,
}

func (t ResolvedTarget) String() string {
	return fmt.Sprintf("ResolvedTarget{ModelID:%q Account:%q Protocol:%q BaseURL:%q NativeModel:%q Headers:%v}",
		t.ModelID, t.Account, t.Protocol, t.BaseURL, t.NativeModel, RedactHeaders(t.Headers))
}

// RedactHeaders 遮蔽敏感头的值，保留键名以便排查配置问题。
func RedactHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if sensitiveHeaders[strings.ToLower(k)] {
			out[k] = mask(v)
			continue
		}
		out[k] = v
	}
	return out
}

// mask 只保留尾部四字符，长度不足则整体遮蔽。
func mask(v string) string {
	if len(v) <= 4 {
		return "****"
	}
	return "****" + v[len(v)-4:]
}
