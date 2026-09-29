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
	"github.com/aceaura/model-surge-relay/backend/migrate"
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
			Defaults:      json.RawMessage(`{"temperature":0.6}`),
			Overrides:     json.RawMessage(`{"max_tokens":8192}`),
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
	pool     *store.Store
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
	collections := collection.NewRepo(st.Pool(), c, upstream)
	userModels := usermodel.NewRepo(st.Pool(), c)
	runStates := runstate.NewRepo(st.Pool())

	api := &httpapi.Server{
		Dispatch: &dispatch.Service{
			UserModels:  userModels,
			Collections: collections,
			RunStates:   runStates,
			Resolver:    upstream,
			Thresholds:  runstate.Thresholds{FailureThreshold: 2, CooldownDuration: time.Hour},
		},
		Health:      stubHealth{st: st, c: c, up: upstream},
		Collections: collections,
		Migrator:    migrate.New(st.Pool(), collections),
		UserModels:  userModels,
		RunStates:   runStates,
		DispatchKey: dispatchKey,
		AdminKey:    adminKey,
	}
	relay := httptest.NewServer(api.Handler())
	t.Cleanup(relay.Close)

	// 缓存后端跨用例共享，清掉可能残留的 usermodel 键。
	if backend != nil {
		_ = backend.Del(ctx, cache.UserModelKey("sonnet"))
	}
	return &env{relay: relay, upstream: up, runState: runStates, pool: st}
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
	return e.request(t, method, path, body, map[string]string{"Authorization": "Bearer " + adminKey})
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

// setup 建好 Collection、两组成员与 user model。
func (e *env) setup(t *testing.T) {
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
	e.mustAdmin(t, http.MethodPost, "/admin/user-models",
		`{"name":"sonnet","collection":"c1","client_key":"sk-client","protocol":"anthropic","enabled":true}`,
		http.StatusCreated)
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
	status, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch", string(payload))
	var out relayv1.DispatchResponse
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("unmarshal dispatch response: %v (%s)", err, body)
		}
	}
	return status, out, body
}

func TestEndToEndDispatchCarriesFullTargetAndProvenance(t *testing.T) {
	e := newEnv(t)
	e.setup(t)

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
	if string(tgt.Defaults) != `{"temperature":0.6}` {
		t.Fatalf("defaults = %s", tgt.Defaults)
	}
	if string(tgt.Overrides) != `{"max_tokens":8192}` {
		t.Fatalf("overrides = %s", tgt.Overrides)
	}
	d := resp.Decision
	if d.Collection != "c1" || d.Group != "primary" || d.GroupType != "fast" {
		t.Fatalf("decision = %+v", d)
	}
	if d.CollectionUpdatedAt.IsZero() {
		t.Fatalf("decision = %+v, want the collection version stamp", d)
	}
	if len(d.Candidates) != 3 || d.Candidates[0].Phase != "standard" {
		t.Fatalf("candidates = %+v, want three standard-phase candidates", d.Candidates)
	}
}

func TestEndToEndCandidateFallForward(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
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
	e.setup(t)
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
	e.setup(t)
	status, resp, body := e.dispatch(t, "req-1", []string{"kimi-1/k3", "kimi-2/k3"})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the only untried candidate", resp.Target.ModelID)
	}
}

// TestEndToEndStrategyChangeTakesEffectImmediately 策略组合改完即生效，
// 覆盖集合缓存失效这条路径（SetStrategy 必须让下一次调度看到新组合）。
func TestEndToEndStrategyChangeTakesEffectImmediately(t *testing.T) {
	e := newEnv(t)
	e.setup(t)

	_, resp, _ := e.dispatch(t, "req-1", nil)
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q", resp.Target.ModelID)
	}

	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["backup","primary"]}`, http.StatusNoContent)

	_, resp, body := e.dispatch(t, "req-2", nil)
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the new chain to apply: %s", resp.Target.ModelID, body)
	}
	if resp.Decision.Group != "backup" {
		t.Fatalf("decision = %+v", resp.Decision)
	}
}

// TestEndToEndGroupExhaustionFailsOverToNextGroup 整组不可用（冷却+已试）
// 自动跳下一组，并在决策里留下 group_skips 痕迹。
func TestEndToEndGroupExhaustionFailsOverToNextGroup(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["primary","backup"]}`, http.StatusNoContent)

	// 阈值为 2，两次异常上报即冷却；再叠一个已试，主组整组不可用。
	for i := 1; i <= 2; i++ {
		e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results",
			fmt.Sprintf(`{"report_id":"rep-cool-%d","model_id":"kimi-1/k3","outcome":"abnormal"}`, i))
	}

	status, resp, body := e.dispatch(t, "req-1", []string{"kimi-2/k3"})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want failover into backup", resp.Target.ModelID)
	}
	if len(resp.Decision.GroupSkips) != 1 || resp.Decision.GroupSkips[0].Group != "primary" {
		t.Fatalf("group skips = %+v, want primary skipped", resp.Decision.GroupSkips)
	}
}

// TestEndToEndOverflowRoutesToCompactPool 超长请求走压缩托管：
// 压缩组候选在前（compact 阶段），原组候选在后（resume 阶段）。
func TestEndToEndOverflowRoutesToCompactPool(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["primary"],"overflow":{"enabled":true,"threshold_tokens":1000,"compact_groups":["backup"]}}`,
		http.StatusNoContent)

	payload, err := json.Marshal(relayv1.DispatchRequest{
		Model: "sonnet", InboundProtocol: relayv1.ProtocolAnthropic,
		ClientKey: "sk-client", RequestID: "req-over", EstTokens: 5000,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch", string(payload))
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var resp relayv1.DispatchResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the compact pool under overflow", resp.Target.ModelID)
	}
	want := []relayv1.PhasedCandidate{
		{ModelID: "ark-1/ds", Phase: "compact"},
		{ModelID: "kimi-1/k3", Phase: "resume"},
		{ModelID: "kimi-2/k3", Phase: "resume"},
	}
	if fmt.Sprint(resp.Decision.Candidates) != fmt.Sprint(want) {
		t.Fatalf("candidates = %v, want %v", resp.Decision.Candidates, want)
	}
}

func TestEndToEndStrategyValidationRejectsUnknownGroup(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	status, body := e.admin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["ghost"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "ghost") {
		t.Fatalf("body %s should name the unknown group", body)
	}
}

// TestEndToEndDeleteGroupReferencedByStrategyIsConflict 被组合引用的组
// 不允许静默删除，409 必须指出引用位置。
func TestEndToEndDeleteGroupReferencedByStrategyIsConflict(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["primary","backup"]}`, http.StatusNoContent)

	status, body := e.admin(t, http.MethodDelete, "/admin/collections/c1/groups/backup", "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, body)
	}
	if !strings.Contains(body, "priority_chain") {
		t.Fatalf("body %s should name the referencing strategy section", body)
	}
}

// TestEndToEndMemberChangeInvalidatesCache 断言改成员后下一次调度立即可见。
func TestEndToEndMemberChangeInvalidatesCache(t *testing.T) {
	e := newEnv(t)
	e.setup(t)

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
	e.setup(t)

	// 阈值为 2，两次异常上报即冷却。
	for i := 1; i <= 2; i++ {
		payload := fmt.Sprintf(
			`{"report_id":"rep-%d","request_id":"req-1","model_id":"kimi-1/k3","outcome":"abnormal"}`, i)
		status, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results", payload)
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
	e.setup(t)
	const payload = `{"report_id":"rep-dup","request_id":"req-1","model_id":"kimi-1/k3","outcome":"abnormal"}`

	_, first := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results", payload)
	if !strings.Contains(first, `"applied":true`) {
		t.Fatalf("first report = %s", first)
	}
	_, second := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results", payload)
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
	e.setup(t)
	for i := 1; i <= 2; i++ {
		e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results",
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
	e.setup(t)
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
	e.setup(t)
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

func TestEndToEndStrategyDryRunDoesNotTouchRuntime(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	before := e.upstream.resolveCalls.Load()

	status, body := e.admin(t, http.MethodPost, "/admin/collections/c1/strategy/dry-run",
		`{"est_tokens":10,"tried_ids":["kimi-1/k3"]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if !strings.Contains(body, "kimi-2/k3") {
		t.Fatalf("body %s should contain the composed sequence", body)
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
