package upstreamclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-relay/backend/apperr"
)

const deliveryKey = "delivery-secret"

func newStubUpstream(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+deliveryKey {
			t.Errorf("Authorization = %q, want bearer delivery key", got)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, deliveryKey)
}

func TestModelsParsesCatalog(t *testing.T) {
	c := newStubUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[
			{"id":"kimi-1/k3","account":"kimi-1","provider_id":"kimi","protocol":"anthropic","native_model":"k3","context_window":262144,"enabled":true},
			{"id":"ark-1/ds","account":"ark-1","provider_id":"ark","protocol":"chat_completions","native_model":"ds","enabled":false}]}`))
	})

	got, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "kimi-1/k3" || got[0].ContextWindow != 262144 || !got[0].Enabled {
		t.Errorf("first listing = %+v", got[0])
	}
	if got[1].Enabled {
		t.Error("second listing must stay disabled")
	}
}

func TestResolveReturnsFullTarget(t *testing.T) {
	c := newStubUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resolve" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model_id"] != "kimi-1/k3" {
			t.Errorf("model_id = %q", body["model_id"])
		}
		_, _ = w.Write([]byte(`{"model_id":"kimi-1/k3","account":"kimi-1","provider_id":"kimi",
			"protocol":"anthropic","base_url":"https://api.example.test/coding","native_model":"k3",
			"context_window":262144,"headers":{"x-api-key":"super-secret-1234","anthropic-version":"2023-06-01"},
			"defaults":{"temperature":0.6},"overrides":{"max_tokens":8192}}`))
	})

	got, err := c.Resolve(context.Background(), "kimi-1/k3")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BaseURL != "https://api.example.test/coding" || got.NativeModel != "k3" {
		t.Errorf("target = %+v", got)
	}
	if got.Headers["x-api-key"] != "super-secret-1234" {
		t.Error("credential header must reach the caller verbatim")
	}
	if string(got.Defaults) != `{"temperature":0.6}` {
		t.Errorf("defaults = %s", got.Defaults)
	}
	if string(got.Overrides) != `{"max_tokens":8192}` {
		t.Errorf("overrides = %s", got.Overrides)
	}
}

func TestResolveMapsUpstreamFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   apperr.Code
	}{
		{"server error", http.StatusInternalServerError, apperr.TargetUnavailable},
		{"unknown model", http.StatusNotFound, apperr.NotFound},
		{"unauthorized delivery key", http.StatusUnauthorized, apperr.TargetUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newStubUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			_, err := c.Resolve(context.Background(), "kimi-1/k3")
			got := apperr.From(err)
			if got == nil || got.Code != tc.want {
				t.Fatalf("code = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestUnreachableUpstreamIsRetryable(t *testing.T) {
	c := New("http://127.0.0.1:1", deliveryKey)
	_, err := c.Resolve(context.Background(), "kimi-1/k3")
	got := apperr.From(err)
	if got.Code != apperr.TargetUnavailable || !got.Retryable {
		t.Fatalf("err = %+v, want retryable target_unavailable", got)
	}
	if c.Ready(context.Background()) {
		t.Error("Ready must be false when upstream is unreachable")
	}
}

func TestReadyReflectsUpstream(t *testing.T) {
	c := newStubUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	})
	if !c.Ready(context.Background()) {
		t.Error("Ready must be true when the catalog endpoint answers")
	}
}

func TestStringRedactsCredentialsWhileJSONKeepsThem(t *testing.T) {
	target := ResolvedTarget{
		ModelID:     "kimi-1/k3",
		Protocol:    "anthropic",
		BaseURL:     "https://api.example.test",
		NativeModel: "k3",
		Headers: map[string]string{
			"Authorization":     "Bearer top-secret-token",
			"x-api-key":         "sk-super-secret",
			"X-Goog-Api-Key":    "goog-secret-key",
			"anthropic-version": "2023-06-01",
		},
	}

	rendered := target.String()
	for _, secret := range []string{"top-secret-token", "sk-super-secret", "goog-secret-key"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("String() leaked %q: %s", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "anthropic-version") {
		t.Error("String() must keep non-sensitive headers readable")
	}

	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"top-secret-token", "sk-super-secret", "goog-secret-key"} {
		if !strings.Contains(string(raw), secret) {
			t.Errorf("MarshalJSON must keep %q verbatim: %s", secret, raw)
		}
	}
}

func TestRedactHeadersIsCaseInsensitive(t *testing.T) {
	got := RedactHeaders(map[string]string{
		"AUTHORIZATION": "Bearer abcdefgh",
		"X-Api-Key":     "sk-abcdefgh",
	})
	for k, v := range got {
		if strings.Contains(v, "abcdefgh") && !strings.HasPrefix(v, "****") {
			t.Errorf("header %s not redacted: %s", k, v)
		}
	}
}

func TestMaskShortValues(t *testing.T) {
	if got := mask("abc"); got != "****" {
		t.Errorf("mask(short) = %q, want ****", got)
	}
	if got := mask("abcdefgh"); got != "****efgh" {
		t.Errorf("mask(long) = %q, want ****efgh", got)
	}
}
