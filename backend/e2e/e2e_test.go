// Package e2e 以真 PG（可选真 Redis）与 stub upstream 跑全链路。
// TEST_PG_DSN 缺失即跳过；TEST_REDIS_ADDR 缺失则以直读模式运行。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/dispatch"
	"github.com/aceaura/model-surge-relay/backend/httpapi"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/policy/examples"
	"github.com/aceaura/model-surge-relay/backend/policy/runtime"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/store"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

const (
	dispatchKey = "dk-e2e"
	adminKey    = "ak-e2e"
)

// stubUpstream 扮演 model-surge-upstream 的下发面。
type stubUpstream struct {
	server *httptest.Server
	// resolveFail 按 model_id 让解析失败，用于回退路径测试。
	resolveFail map[string]bool
	// disabled 让目录把某模型标为未启用。
	disabled     map[string]bool
	resolveCalls atomic.Int64
}

func newStubUpstream(t *testing.T, models []upstreamclient.Listing) *stubUpstream {
	t.Helper()
	s := &stubUpstream{resolveFail: map[string]bool{}, disabled: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		out := make([]upstreamclient.Listing, 0, len(models))
		for _, m := range models {
			if s.disabled[m.ID] {
				m.Enabled = false
			}
			out = append(out, m)
		}
		writeJSON(w, map[string]any{"models": out})
	})
	mux.HandleFunc("POST /v1/resolve", func(w http.ResponseWriter, r *http.Request) {
		s.resolveCalls.Add(1)
		var body struct {
			ModelID string `json:"model_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s.resolveFail[body.ModelID] {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var found *upstreamclient.Listing
		for i := range models {
			if models[i].ID == body.ModelID {
				found = &models[i]
				break
			}
		}
		if found == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, upstreamclient.ResolvedTarget{
			ModelID:       found.ID,
			Account:       found.Account,
			ProviderID:    found.ProviderID,
			Protocol:      found.Protocol,
			BaseURL:       "https://" + found.Account + ".example.invalid/v1",
			NativeModel:   found.NativeModel,
			ContextWindow: found.ContextWindow,
			Headers:       map[string]string{"Authorization": "Bearer sk-live-" + found.Account},
			Params:        json.RawMessage(`{"temperature":0.6}`),
		})
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func catalog() []upstreamclient.Listing {
	return []upstreamclient.Listing{
		{ID: "kimi-1/k3", Account: "kimi-1", ProviderID: "moonshot", Protocol: relayv1.ProtocolAnthropic, NativeModel: "kimi-k3-256k", ContextWindow: 262144, Enabled: true},
		{ID: "kimi-2/k3", Account: "kimi-2", ProviderID: "moonshot", Protocol: relayv1.ProtocolAnthropic, NativeModel: "kimi-k3-256k", ContextWindow: 262144, Enabled: true},
		{ID: "ark-1/ds", Account: "ark-1", ProviderID: "volcengine", Protocol: relayv1.ProtocolChatCompletions, NativeModel: "deepseek-v4-1-flash", ContextWindow: 1048576, Enabled: true},
	}
}

type env struct {
	relay    *httptest.Server
	upstream *stubUpstream
	runState *runstate.Repo
	cache    *cache.Cache
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE user_models, policies, group_members, groups, collections,
		          target_runtime, result_reports RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var backend cache.Backend
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		redis := cache.NewRedisBackend(addr, os.Getenv("TEST_REDIS_PASSWORD"), 0)
		t.Cleanup(func() { _ = redis.Close() })
		// 每个用例独立键空间会更干净，但缓存键是全局常量；
		// 用例间清空整库以免互相污染。
		if !redis.Ready(ctx) {
			t.Fatalf("TEST_REDIS_ADDR set but redis is unreachable")
		}
		for _, key := range []string{"c1", "c2"} {
			_ = redis.Del(ctx, cache.CollectionKey(key))
		}
		backend = redis
	}
	c := cache.New(backend, time.Minute)

	up := newStubUpstream(t, catalog())
	upstream := upstreamclient.New(up.server.URL, "delivery-key")
	engine := policy.NewEngine(
		policy.NewRegistry(runtime.NewLua(), runtime.NewJavaScript(), runtime.NewTypeScript()),
		time.Second,
	)
	collections := collection.NewRepo(st.Pool(), c, upstream)
	policies := policy.NewRepo(st.Pool(), c, engine)
	userModels := usermodel.NewRepo(st.Pool(), c)
	runStates := runstate.NewRepo(st.Pool())

	api := &httpapi.Server{
		Dispatch: &dispatch.Service{
			UserModels:  userModels,
			Collections: collections,
			Policies:    policies,
			Engine:      engine,
			RunStates:   runStates,
			Resolver:    upstream,
			Thresholds:  runstate.Thresholds{FailureThreshold: 2, CooldownDuration: time.Hour},
		},
		Health:      stubHealth{st: st, c: c, up: upstream},
		Collections: collections,
		Policies:    policies,
		Engine:      engine,
		UserModels:  userModels,
		RunStates:   runStates,
		DispatchKey: dispatchKey,
		AdminKey:    adminKey,
	}
	relay := httptest.NewServer(api.Handler())
	t.Cleanup(relay.Close)

	// 缓存后端跨用例共享，清掉可能残留的 usermodel / policy 键。
	if backend != nil {
		for _, key := range []string{cache.UserModelKey("sonnet"), cache.PolicyKey("dynamic")} {
			_ = backend.Del(ctx, key)
		}
	}
	return &env{relay: relay, upstream: up, runState: runStates, cache: c}
}

type stubHealth struct {
	st *store.Store
	c  *cache.Cache
	up *upstreamclient.Client
}

func (h stubHealth) PingDatabase(ctx context.Context) error { return h.st.Ping(ctx) }
func (h stubHealth) CacheReady(ctx context.Context) bool    { return h.c.Ready(ctx) }
func (h stubHealth) UpstreamReady(ctx context.Context) bool { return h.up.Ready(ctx) }

func (e *env) request(t *testing.T, method, path, body string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.relay.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.relay.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func (e *env) admin(t *testing.T, method, path, body string) (int, string) {
	return e.request(t, method, path, body, map[string]string{"X-Admin-Key": adminKey})
}

func (e *env) internal(t *testing.T, method, path, body string) (int, string) {
	return e.request(t, method, path, body, map[string]string{"Authorization": "Bearer " + dispatchKey})
}

// mustAdmin 断言管理面调用成功，把建库噪声压到一行。
func (e *env) mustAdmin(t *testing.T, method, path, body string, want int) string {
	t.Helper()
	status, out := e.admin(t, method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d (want %d): %s", method, path, status, want, out)
	}
	return out
}

// setup 建好 Collection、两组成员与 user model；policySource 为空表示不绑定策略。
func (e *env) setup(t *testing.T, policySource string) {
	t.Helper()
	e.mustAdmin(t, http.MethodPost, "/admin/collections", `{"name":"c1"}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"primary","type":"fast","position":0}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"backup","type":"cheap","position":1}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/groups/primary/members",
		`{"members":["kimi-1/k3","kimi-2/k3"]}`, http.StatusNoContent)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/groups/backup/members",
		`{"members":["ark-1/ds"]}`, http.StatusNoContent)

	body := `{"name":"sonnet","collection":"c1","client_key":"sk-client","protocol":"anthropic","enabled":true}`
	if policySource != "" {
		payload, err := json.Marshal(map[string]string{
			"name": "dynamic", "language": "lua", "source": policySource,
		})
		if err != nil {
			t.Fatalf("marshal policy: %v", err)
		}
		e.mustAdmin(t, http.MethodPost, "/admin/policies", string(payload), http.StatusCreated)
		body = `{"name":"sonnet","collection":"c1","policy":"dynamic","client_key":"sk-client","protocol":"anthropic","enabled":true}`
	}
	e.mustAdmin(t, http.MethodPost, "/admin/user-models", body, http.StatusCreated)
}

func (e *env) dispatch(t *testing.T, requestID string, tried []string) (int, relayv1.DispatchResponse, string) {
	t.Helper()
	payload, err := json.Marshal(relayv1.DispatchRequest{
		Model:           "sonnet",
		InboundProtocol: relayv1.ProtocolAnthropic,
		ClientKey:       "sk-client",
		RequestID:       requestID,
		TriedIDs:        tried,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/dispatch", string(payload))
	var out relayv1.DispatchResponse
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("unmarshal dispatch response: %v (%s)", err, body)
		}
	}
	return status, out, body
}

func presetSource(t *testing.T) string {
	t.Helper()
	src, err := examples.Source(examples.Preset)
	if err != nil {
		t.Fatalf("example source: %v", err)
	}
	return src
}

func TestEndToEndDispatchCarriesFullTargetAndProvenance(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))

	status, resp, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	tgt := resp.Target
	if tgt.ModelID != "kimi-1/k3" || tgt.Account != "kimi-1" || tgt.ProviderID != "moonshot" ||
		tgt.Protocol != relayv1.ProtocolAnthropic || tgt.NativeModel != "kimi-k3-256k" ||
		tgt.ContextWindow != 262144 || tgt.BaseURL == "" {
		t.Fatalf("target = %+v, want the full upstream description", tgt)
	}
	if tgt.Headers["Authorization"] != "Bearer sk-live-kimi-1" {
		t.Fatalf("headers = %v, credentials must pass through", tgt.Headers)
	}
	if string(tgt.Params) != `{"temperature":0.6}` {
		t.Fatalf("params = %s", tgt.Params)
	}
	d := resp.Decision
	if d.Policy != "dynamic" || d.PolicyVersion != 1 || d.Collection != "c1" ||
		d.Group != "primary" || d.GroupType != "fast" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestEndToEndFallbackPathWithoutPolicy(t *testing.T) {
	e := newEnv(t)
	e.setup(t, "")
	status, resp, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q, want first member of first group", resp.Target.ModelID)
	}
	if resp.Decision.Policy != "" {
		t.Fatalf("decision = %+v, want no policy provenance", resp.Decision)
	}
}

func TestEndToEndCandidateFallForward(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	e.upstream.resolveFail["kimi-1/k3"] = true

	status, resp, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the second candidate", resp.Target.ModelID)
	}
	var sawFailure bool
	for _, s := range resp.Decision.Skipped {
		if s.ModelID == "kimi-1/k3" && s.Reason == relayv1.SkipResolveFailed {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("skipped = %+v, want a resolve_failed entry", resp.Decision.Skipped)
	}
}

func TestEndToEndAllCandidatesFail(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	for _, id := range []string{"kimi-1/k3", "kimi-2/k3", "ark-1/ds"} {
		e.upstream.resolveFail[id] = true
	}
	status, _, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", status, body)
	}
	var env relayv1.ErrorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.Error.Retryable {
		t.Fatalf("error = %+v, want retryable", env.Error)
	}
	for _, want := range []string{"kimi-1/k3", "kimi-2/k3", "ark-1/ds"} {
		if !strings.Contains(env.Error.Message, want) {
			t.Fatalf("message %q should summarize %s", env.Error.Message, want)
		}
	}
}

func TestEndToEndTriedIDsAreExcluded(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	status, resp, body := e.dispatch(t, "req-1", []string{"kimi-1/k3", "kimi-2/k3"})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the only untried candidate", resp.Target.ModelID)
	}
}

// TestEndToEndPolicyVersionBumpTakesEffect 断言更新源码后的下一次调度用新逻辑，
// 覆盖编译缓存按版本失效与策略缓存键失效两条路径。
func TestEndToEndPolicyVersionBumpTakesEffect(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))

	_, resp, _ := e.dispatch(t, "req-1", nil)
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q", resp.Target.ModelID)
	}

	payload, err := json.Marshal(map[string]string{
		"language": "lua",
		"source":   `return { candidates = { "ark-1/ds" }, note = "v2 forces backup" }`,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e.mustAdmin(t, http.MethodPut, "/admin/policies/dynamic", string(payload), http.StatusOK)

	_, resp, body := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the new logic to apply: %s", resp.Target.ModelID, body)
	}
	if resp.Decision.PolicyVersion != 2 || resp.Decision.Note != "v2 forces backup" {
		t.Fatalf("decision = %+v", resp.Decision)
	}
}

// TestEndToEndMemberChangeInvalidatesCache 断言改成员后下一次调度立即可见。
func TestEndToEndMemberChangeInvalidatesCache(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))

	_, resp, _ := e.dispatch(t, "req-1", nil)
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q", resp.Target.ModelID)
	}

	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/groups/primary/members",
		`{"members":["kimi-2/k3"]}`, http.StatusNoContent)

	_, resp, body := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the edited membership to be visible: %s", resp.Target.ModelID, body)
	}
}

// TestEndToEndCoolingExcludesTargetOnNextDispatch 覆盖上报 → 冷却 → 排除全链路。
func TestEndToEndCoolingExcludesTargetOnNextDispatch(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))

	// 阈值为 2，两次异常上报即冷却。
	for i := 1; i <= 2; i++ {
		payload := fmt.Sprintf(
			`{"report_id":"rep-%d","request_id":"req-1","model_id":"kimi-1/k3","outcome":"abnormal"}`, i)
		status, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/results", payload)
		if status != http.StatusOK {
			t.Fatalf("report %d = %d: %s", i, status, body)
		}
		if !strings.Contains(body, `"applied":true`) {
			t.Fatalf("report %d body = %s", i, body)
		}
	}

	_, resp, body := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, cooling target must be excluded: %s", resp.Target.ModelID, body)
	}
	var sawCooling bool
	for _, s := range resp.Decision.Skipped {
		if s.ModelID == "kimi-1/k3" && s.Reason == relayv1.SkipCooling {
			sawCooling = true
		}
	}
	if !sawCooling {
		t.Fatalf("skipped = %+v, want a cooling entry", resp.Decision.Skipped)
	}
}

func TestEndToEndDuplicateReportIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	const payload = `{"report_id":"rep-dup","request_id":"req-1","model_id":"kimi-1/k3","outcome":"abnormal"}`

	_, first := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/results", payload)
	if !strings.Contains(first, `"applied":true`) {
		t.Fatalf("first report = %s", first)
	}
	_, second := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/results", payload)
	if !strings.Contains(second, `"applied":false`) {
		t.Fatalf("replayed report = %s, want applied=false", second)
	}

	state, err := e.runState.Get(context.Background(), "kimi-1/k3")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("failures = %d, want 1 (replay must not double count)", state.ConsecutiveFailures)
	}
}

func TestEndToEndRuntimeResetRestoresTarget(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	for i := 1; i <= 2; i++ {
		e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/results",
			fmt.Sprintf(`{"report_id":"r-%d","model_id":"kimi-1/k3","outcome":"abnormal"}`, i))
	}
	_, resp, _ := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID == "kimi-1/k3" {
		t.Fatal("cooling target should have been excluded")
	}

	e.mustAdmin(t, http.MethodDelete, "/admin/runtime/kimi-1/k3", "", http.StatusNoContent)

	_, resp, body := e.dispatch(t, "req-3", nil)
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q, reset should bring it back: %s", resp.Target.ModelID, body)
	}
}

func TestEndToEndUnknownMemberReferenceRejected(t *testing.T) {
	e := newEnv(t)
	e.setup(t, "")
	status, body := e.admin(t, http.MethodPut, "/admin/collections/c1/groups/primary/members",
		`{"members":["ghost/model"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "ghost/model") {
		t.Fatalf("body %s should name the unknown reference", body)
	}
}

func TestEndToEndDisabledCatalogModelIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	// 先跑一次预热缓存，再让目录把首选标为停用。
	e.dispatch(t, "req-warm", nil)
	e.upstream.disabled["kimi-1/k3"] = true
	// 目录属性来自快照缓存，改成员触发失效以便新状态可见。
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/groups/primary/members",
		`{"members":["kimi-1/k3","kimi-2/k3"]}`, http.StatusNoContent)

	_, resp, body := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, disabled model must be skipped: %s", resp.Target.ModelID, body)
	}
	var sawDisabled bool
	for _, s := range resp.Decision.Skipped {
		if s.ModelID == "kimi-1/k3" && s.Reason == relayv1.SkipDisabled {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatalf("skipped = %+v, want a disabled entry", resp.Decision.Skipped)
	}
}

func TestEndToEndEmptyPolicyResultSkipsUpstream(t *testing.T) {
	e := newEnv(t)
	e.setup(t, `return { candidates = {}, note = "deliberately empty" }`)
	before := e.upstream.resolveCalls.Load()

	status, _, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", status, body)
	}
	if e.upstream.resolveCalls.Load() != before {
		t.Fatal("an empty candidate list must not reach upstream resolve")
	}
}

func TestEndToEndPolicyRuntimeErrorIsNotRetryable(t *testing.T) {
	e := newEnv(t)
	e.setup(t, `local x = nil; return x.boom`)
	status, _, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", status, body)
	}
	var env relayv1.ErrorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.Code != "policy_error" || env.Error.Retryable {
		t.Fatalf("error = %+v, want non-retryable policy_error", env.Error)
	}
}

func TestEndToEndPolicyWithUncompilableSourceRejectedAtSave(t *testing.T) {
	e := newEnv(t)
	e.setup(t, "")
	status, body := e.admin(t, http.MethodPost, "/admin/policies",
		`{"name":"broken","language":"lua","source":"return ((("}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "compile error") {
		t.Fatalf("body %s should explain the compile failure", body)
	}
}

func TestEndToEndUnsupportedLanguageRejected(t *testing.T) {
	e := newEnv(t)
	e.setup(t, "")
	status, body := e.admin(t, http.MethodPost, "/admin/policies",
		`{"name":"py","language":"python","source":"pass"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", status, body)
	}
	for _, want := range []string{"lua", "javascript", "typescript"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s should list %q as supported", body, want)
		}
	}
}

func TestEndToEndDeletePolicyBoundToUserModelIsConflict(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	status, body := e.admin(t, http.MethodDelete, "/admin/policies/dynamic", "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, body)
	}
	if !strings.Contains(body, "sonnet") {
		t.Fatalf("body %s should name the referencing user model", body)
	}
}

func TestEndToEndDryRunDoesNotTouchRuntime(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	before := e.upstream.resolveCalls.Load()

	status, body := e.admin(t, http.MethodPost, "/admin/policies/dynamic/dry-run",
		`{"collection":"c1","request":{"user_model":"sonnet","est_tokens":10},
		  "runtime":{"kimi-1/k3":{"cooling":true}}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if !strings.Contains(body, "kimi-1/k3") {
		t.Fatalf("body %s should contain the decision", body)
	}
	if e.upstream.resolveCalls.Load() != before {
		t.Fatal("dry-run must not resolve targets")
	}
	state, err := e.runState.Get(context.Background(), "kimi-1/k3")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Cooling || state.ConsecutiveFailures != 0 {
		t.Fatalf("dry-run mutated runtime state: %+v", state)
	}
}

func TestEndToEndDispatchAuthFailures(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unknown model", `{"model":"ghost","client_key":"sk-client","request_id":"r"}`, http.StatusNotFound},
		{"wrong key", `{"model":"sonnet","client_key":"nope","request_id":"r"}`, http.StatusUnauthorized},
		{"wrong protocol", `{"model":"sonnet","inbound_protocol":"gemini","client_key":"sk-client","request_id":"r"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/dispatch", tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, body)
			}
		})
	}
}

func TestEndToEndDisabledUserModelRejected(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	e.mustAdmin(t, http.MethodPut, "/admin/user-models/sonnet",
		`{"collection":"c1","policy":"dynamic","client_key":"sk-client","protocol":"anthropic","enabled":false}`,
		http.StatusOK)

	status, _, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", status, body)
	}
}

func TestEndToEndHealthReportsComponents(t *testing.T) {
	e := newEnv(t)
	status, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/health", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ok" || got.Database != "ok" || got.Upstream != "ok" {
		t.Fatalf("health = %+v", got)
	}
	wantCache := "disabled"
	if os.Getenv("TEST_REDIS_ADDR") != "" {
		wantCache = "ok"
	}
	if got.Cache != wantCache {
		t.Fatalf("cache = %q, want %q", got.Cache, wantCache)
	}
}

func TestEndToEndModelsListing(t *testing.T) {
	e := newEnv(t)
	e.setup(t, presetSource(t))
	status, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/models", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var got relayv1.ModelsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "sonnet" || got.Models[0].Policy != "dynamic" {
		t.Fatalf("models = %+v", got.Models)
	}
	if strings.Contains(body, "sk-client") {
		t.Fatalf("listing leaks the client key: %s", body)
	}
}

func TestEndToEndKeyIsolation(t *testing.T) {
	e := newEnv(t)
	status, _ := e.request(t, http.MethodGet, "/admin/collections", "",
		map[string]string{"Authorization": "Bearer " + dispatchKey})
	if status != http.StatusUnauthorized {
		t.Fatalf("dispatch key on admin = %d, want 401", status)
	}
	status, _ = e.request(t, http.MethodGet, relayv1.InternalBasePath+"/models", "",
		map[string]string{"X-Admin-Key": adminKey})
	if status != http.StatusUnauthorized {
		t.Fatalf("admin key on internal = %d, want 401", status)
	}
}
