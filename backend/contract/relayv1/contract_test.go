package relayv1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
)

func fullTarget() Target {
	return Target{
		ModelID:       "kimi-1/k3",
		Account:       "kimi-1",
		ProviderID:    "moonshot",
		Protocol:      ProtocolAnthropic,
		BaseURL:       "https://api.moonshot.cn/coding",
		NativeModel:   "kimi-k3-256k",
		ContextWindow: 262144,
		Headers: map[string]string{
			"x-api-key":         "sk-secret-value-1234",
			"anthropic-version": "2023-06-01",
		},
		Defaults:  json.RawMessage(`{"temperature":0.6}`),
		Overrides: json.RawMessage(`{"max_tokens":8192}`),
	}
}

func TestTargetSerializesEveryUpstreamField(t *testing.T) {
	raw, err := json.Marshal(fullTarget())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"model_id", "account", "provider_id", "protocol",
		"base_url", "native_model", "context_window", "headers", "defaults", "overrides",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("field %q missing from %s", key, raw)
		}
	}
}

func TestTargetMarshalKeepsCredentialsVerbatim(t *testing.T) {
	raw, err := json.Marshal(fullTarget())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// 调用方要拿这个头去发请求，序列化必须保留原值。
	if !strings.Contains(string(raw), "sk-secret-value-1234") {
		t.Fatalf("marshalled target must carry the real credential: %s", raw)
	}
}

func TestTargetStringRedactsSensitiveHeaders(t *testing.T) {
	s := fullTarget().String()
	if strings.Contains(s, "sk-secret-value-1234") {
		t.Fatalf("String() leaks the credential: %s", s)
	}
	if !strings.Contains(s, "x-api-key") {
		t.Fatalf("String() should keep header names for diagnosis: %s", s)
	}
	if !strings.Contains(s, "2023-06-01") {
		t.Fatalf("String() should keep non-sensitive header values: %s", s)
	}
}

func TestTargetStringRedactsAcrossHeaderCasings(t *testing.T) {
	cases := []string{"Authorization", "authorization", "X-Api-Key", "x-goog-api-key"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			tgt := Target{Headers: map[string]string{name: "secret-token-abcd"}}
			if strings.Contains(tgt.String(), "secret-token-abcd") {
				t.Fatalf("String() leaks %s: %s", name, tgt)
			}
		})
	}
}

func TestTargetFromResolvedCopiesEveryField(t *testing.T) {
	src := upstreamclient.ResolvedTarget{
		ModelID:       "ark-1/ds",
		Account:       "ark-1",
		ProviderID:    "volcengine",
		Protocol:      ProtocolChatCompletions,
		BaseURL:       "https://ark.example.invalid/v3",
		NativeModel:   "deepseek-v4-1-flash",
		ContextWindow: 1048576,
		Headers:       map[string]string{"Authorization": "Bearer sk-ark"},
		Defaults:      json.RawMessage(`{"top_p":0.9}`),
		Overrides:     json.RawMessage(`{"max_tokens":4096}`),
	}
	got := TargetFromResolved(src)
	if got.ModelID != src.ModelID || got.Account != src.Account ||
		got.ProviderID != src.ProviderID || got.Protocol != src.Protocol ||
		got.BaseURL != src.BaseURL || got.NativeModel != src.NativeModel ||
		got.ContextWindow != src.ContextWindow ||
		got.Headers["Authorization"] != src.Headers["Authorization"] ||
		string(got.Defaults) != string(src.Defaults) ||
		string(got.Overrides) != string(src.Overrides) {
		t.Fatalf("conversion dropped a field: %+v vs %+v", got, src)
	}
}

func TestDecisionCarriesFullProvenance(t *testing.T) {
	d := Decision{
		Policy:        "least-used",
		PolicyVersion: 7,
		Collection:    "c1",
		Group:         "backup",
		GroupType:     "cheap",
		Note:          "picked lowest usage",
		Candidates:    []string{"ark-1/ds", "ark-2/ds"},
		Skipped: []Skip{
			{ModelID: "kimi-1/k3", Reason: SkipCooling},
			{ModelID: "kimi-2/k3", Reason: SkipResolveFailed, Detail: "upstream 503"},
		},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"policy", "policy_version", "collection", "group",
		"group_type", "note", "candidates", "skipped",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("field %q missing from %s", key, raw)
		}
	}
}

func TestDecisionOmitsPolicyProvenanceOnFallback(t *testing.T) {
	raw, err := json.Marshal(Decision{Collection: "c1", Group: "g", GroupType: "t", Candidates: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "policy") {
		t.Fatalf("fallback decision should not claim a policy: %s", raw)
	}
}

func TestDispatchResponseStringRedactsCredentials(t *testing.T) {
	resp := DispatchResponse{
		RequestID: "req-1",
		Target:    fullTarget(),
		Decision:  Decision{Collection: "c1", Candidates: []string{"kimi-1/k3"}},
	}
	if strings.Contains(resp.String(), "sk-secret-value-1234") {
		t.Fatalf("String() leaks the credential: %s", resp)
	}
	if !strings.Contains(resp.String(), "req-1") {
		t.Fatalf("String() should keep the request id: %s", resp)
	}
}

func TestDispatchRequestRoundTrip(t *testing.T) {
	want := DispatchRequest{
		Model:           "sonnet",
		InboundProtocol: ProtocolAnthropic,
		ClientKey:       "sk-client",
		RequestID:       "req-1",
		TriedIDs:        []string{"kimi-1/k3"},
		EstTokens:       4096,
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DispatchRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Model != want.Model || got.InboundProtocol != want.InboundProtocol ||
		got.ClientKey != want.ClientKey || got.RequestID != want.RequestID ||
		got.EstTokens != want.EstTokens || len(got.TriedIDs) != 1 {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestResultReportRoundTrip(t *testing.T) {
	want := ResultReport{
		ReportID: "rep-1", RequestID: "req-1", ModelID: "kimi-1/k3", Outcome: "normal",
		Usage: Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ResultReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestProtocolNamesMatchUpstreamVocabulary(t *testing.T) {
	want := []string{"anthropic", "chat_completions", "responses", "gemini"}
	got := []string{ProtocolAnthropic, ProtocolChatCompletions, ProtocolResponses, ProtocolGemini}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("protocol[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	raw, err := json.Marshal(ErrorEnvelope{Error: Error{
		Code: "target_unavailable", Message: "no eligible target", Retryable: true,
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"error":{"code":"target_unavailable","message":"no eligible target","retryable":true}}`
	if string(raw) != want {
		t.Fatalf("envelope = %s, want %s", raw, want)
	}
}
