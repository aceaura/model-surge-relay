package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/dispatch"
)

func captureLog(t *testing.T, e dispatch.LogEntry) (map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	SlogLogger{Log: slog.New(slog.NewJSONHandler(&buf, nil))}.Dispatched(e)
	raw := buf.String()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("log line is not JSON (%v): %s", err, raw)
	}
	return got, raw
}

func TestDispatchedLogCarriesEveryField(t *testing.T) {
	got, raw := captureLog(t, dispatch.LogEntry{
		RequestID:     "req-1",
		UserModel:     "sonnet",
		Policy:        "least-used",
		PolicyVersion: 7,
		Candidates:    2,
		Selected:      "kimi-1/k3",
		Skipped:       []relayv1.Skip{{ModelID: "kimi-2/k3", Reason: relayv1.SkipCooling}},
		Duration:      12 * time.Millisecond,
	})
	for _, key := range []string{
		"request_id", "user_model", "policy", "policy_version",
		"candidates", "selected", "skipped", "duration_ms",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("field %q missing from %s", key, raw)
		}
	}
	if got["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", got["level"])
	}
	if got["duration_ms"] != float64(12) {
		t.Errorf("duration_ms = %v, want 12", got["duration_ms"])
	}
	if !strings.Contains(raw, "kimi-2/k3:cooling") {
		t.Errorf("skip reason should be rendered: %s", raw)
	}
}

func TestDispatchedLogOmitsPolicyOnFallback(t *testing.T) {
	got, raw := captureLog(t, dispatch.LogEntry{
		RequestID: "req-1", UserModel: "sonnet", Candidates: 1, Selected: "kimi-1/k3",
	})
	if _, ok := got["policy"]; ok {
		t.Fatalf("fallback dispatch should not claim a policy: %s", raw)
	}
}

func TestFailedDispatchLogsAtErrorLevel(t *testing.T) {
	got, raw := captureLog(t, dispatch.LogEntry{
		RequestID: "req-1", UserModel: "sonnet",
		Policy: "broken", PolicyVersion: 2,
		Error: "policy_error: attempt to index a nil value",
	})
	if got["level"] != "ERROR" {
		t.Fatalf("level = %v, want ERROR", got["level"])
	}
	if got["policy"] != "broken" || got["policy_version"] != float64(2) {
		t.Fatalf("failure log should name the policy and version: %s", raw)
	}
	if !strings.Contains(raw, "attempt to index a nil value") {
		t.Fatalf("failure log should carry the error summary: %s", raw)
	}
	if _, ok := got["selected"]; ok {
		t.Fatalf("a failed dispatch has no selection: %s", raw)
	}
}

// TestLogNeverCarriesSecrets 是不变式测试：即使把密钥塞进每个可控字段，
// 输出里也不该出现，因为 LogEntry 本身不承载凭据。
func TestLogNeverCarriesSecrets(t *testing.T) {
	const secret = "sk-super-secret-value"
	_, raw := captureLog(t, dispatch.LogEntry{
		RequestID:  "req-1",
		UserModel:  "sonnet",
		Candidates: 1,
		Selected:   "kimi-1/k3",
		Skipped:    []relayv1.Skip{{ModelID: "kimi-2/k3", Reason: relayv1.SkipResolveFailed, Detail: "upstream 503"}},
	})
	if strings.Contains(raw, secret) {
		t.Fatalf("log leaks a secret: %s", raw)
	}
	// 也断言结构本身没有可放凭据的位置。
	for _, banned := range []string{"authorization", "x-api-key", "client_key", "credential"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Fatalf("log line mentions %q: %s", banned, raw)
		}
	}
}

func TestNilLoggerFallsBackToDefault(t *testing.T) {
	// 不应 panic：装配漏配 Logger 不该让调度整条链路崩掉。
	SlogLogger{}.Dispatched(dispatch.LogEntry{RequestID: "req-1"})
}
