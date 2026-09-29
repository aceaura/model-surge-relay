package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/dispatch"
	"github.com/aceaura/model-surge-relay/backend/migrate"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/strategy"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

const (
	dispatchKey = "dk-secret"
	adminKey    = "ak-secret"
)

type stubHealth struct {
	dbErr    error
	cache    bool
	upstream bool
}

func (h stubHealth) PingDatabase(context.Context) error { return h.dbErr }
func (h stubHealth) CacheReady(context.Context) bool    { return h.cache }
func (h stubHealth) UpstreamReady(context.Context) bool { return h.upstream }

type stubCollections struct {
	snap     collection.Snapshot
	created  collection.Collection
	groups   []collection.Group
	members  []string
	strategy strategy.Strategy
	err      error
}

func (c *stubCollections) Create(_ context.Context, name, note string) (collection.Collection, error) {
	if c.err != nil {
		return collection.Collection{}, c.err
	}
	c.created = collection.Collection{Name: name, Note: note}
	return c.created, nil
}

func (c *stubCollections) Get(_ context.Context, name string) (collection.Collection, error) {
	if c.err != nil {
		return collection.Collection{}, c.err
	}
	return collection.Collection{Name: name, Strategy: c.strategy}, nil
}

func (c *stubCollections) List(context.Context) ([]collection.Collection, error) {
	return []collection.Collection{{Name: "c1"}}, c.err
}

func (c *stubCollections) UpdateNote(context.Context, string, string) error { return c.err }
func (c *stubCollections) Delete(context.Context, string) error             { return c.err }

func (c *stubCollections) CreateGroup(_ context.Context, g collection.Group) (collection.Group, error) {
	if c.err != nil {
		return collection.Group{}, c.err
	}
	c.groups = append(c.groups, g)
	return g, nil
}

func (c *stubCollections) UpdateGroup(context.Context, collection.Group) error { return c.err }
func (c *stubCollections) DeleteGroup(context.Context, string, string) error   { return c.err }
func (c *stubCollections) Groups(context.Context, string) ([]collection.Group, error) {
	return c.groups, c.err
}

func (c *stubCollections) ReplaceMembers(_ context.Context, _, _ string, ids []string) error {
	if c.err != nil {
		return c.err
	}
	c.members = ids
	return nil
}

func (c *stubCollections) Snapshot(context.Context, string) (collection.Snapshot, error) {
	return c.snap, c.err
}

func (c *stubCollections) SetStrategy(_ context.Context, _ string, st strategy.Strategy) error {
	if c.err != nil {
		return c.err
	}
	c.strategy = st
	return nil
}

type stubMigrator struct {
	report migrate.Report
	err    error
	calls  int
}

func (m *stubMigrator) Migrate(context.Context) (migrate.Report, error) {
	m.calls++
	return m.report, m.err
}

type stubUserModels struct {
	model usermodel.UserModel
	err   error
}

func (u *stubUserModels) Create(_ context.Context, m usermodel.UserModel) (usermodel.UserModel, error) {
	if u.err != nil {
		return usermodel.UserModel{}, u.err
	}
	u.model = m
	return m, nil
}

func (u *stubUserModels) Update(_ context.Context, m usermodel.UserModel) (usermodel.UserModel, error) {
	if u.err != nil {
		return usermodel.UserModel{}, u.err
	}
	u.model = m
	return m, nil
}

func (u *stubUserModels) Get(context.Context, string) (usermodel.UserModel, error) {
	return u.model, u.err
}

func (u *stubUserModels) List(context.Context) ([]usermodel.UserModel, error) {
	return []usermodel.UserModel{u.model}, u.err
}

func (u *stubUserModels) Delete(context.Context, string) error { return u.err }

func (u *stubUserModels) Authenticate(_ context.Context, name, _, key string) (usermodel.UserModel, error) {
	if u.err != nil {
		return usermodel.UserModel{}, u.err
	}
	if key != u.model.ClientKey {
		return usermodel.UserModel{}, apperr.New(apperr.Unauthorized, "client key mismatch")
	}
	m := u.model
	m.Name = name
	m.ClientKey = ""
	return m, nil
}

type stubRunStates struct {
	states  []runstate.State
	err     error
	resetID string
}

func (r *stubRunStates) List(context.Context) ([]runstate.State, error) { return r.states, r.err }

func (r *stubRunStates) Reset(_ context.Context, modelID string) error {
	if r.err != nil {
		return r.err
	}
	r.resetID = modelID
	return nil
}

func (r *stubRunStates) Load(_ context.Context, ids []string) (map[string]runstate.State, error) {
	out := map[string]runstate.State{}
	for _, id := range ids {
		out[id] = runstate.State{ModelID: id}
	}
	return out, nil
}

func (r *stubRunStates) ApplyReport(context.Context, runstate.ResultReport, runstate.Thresholds) (bool, error) {
	return true, r.err
}

type stubResolver struct{}

func (stubResolver) Resolve(_ context.Context, modelID string) (upstreamclient.ResolvedTarget, error) {
	return upstreamclient.ResolvedTarget{
		ModelID:     modelID,
		Account:     strings.Split(modelID, "/")[0],
		Protocol:    relayv1.ProtocolAnthropic,
		BaseURL:     "https://example.invalid",
		NativeModel: "k3",
		Headers:     map[string]string{"Authorization": "Bearer sk-upstream"},
	}, nil
}

type env struct {
	server      *httptest.Server
	collections *stubCollections
	migrator    *stubMigrator
	users       *stubUserModels
	runStates   *stubRunStates
	health      *stubHealth
}

func newEnv(t *testing.T) *env {
	t.Helper()
	snap := collection.Snapshot{Name: "c1", Groups: []collection.GroupSnapshot{{
		Name: "primary", Type: "fast",
		Members: []collection.Member{{ModelID: "kimi-1/k3", Enabled: true, Known: true}},
	}}}
	e := &env{
		collections: &stubCollections{snap: snap},
		migrator: &stubMigrator{report: migrate.Report{
			Mapped:    []migrate.Mapped{{Collection: "c1", Policy: "preset"}},
			Conflicts: []migrate.Conflict{},
			Unmapped:  []migrate.Unmapped{},
		}},
		users:     &stubUserModels{model: usermodel.UserModel{Name: "sonnet", Collection: "c1", ClientKey: "sk-client", Enabled: true}},
		runStates: &stubRunStates{},
		health:    &stubHealth{cache: true, upstream: true},
	}
	srv := &Server{
		Dispatch: &dispatch.Service{
			UserModels:  e.users,
			Collections: e.collections,
			RunStates:   e.runStates,
			Resolver:    stubResolver{},
			Thresholds:  runstate.Thresholds{FailureThreshold: 3, CooldownDuration: time.Minute},
		},
		Health:      e.health,
		Collections: e.collections,
		Migrator:    e.migrator,
		UserModels:  e.users,
		RunStates:   e.runStates,
		DispatchKey: dispatchKey,
		AdminKey:    adminKey,
	}
	e.server = httptest.NewServer(srv.Handler())
	t.Cleanup(e.server.Close)
	return e
}

type call struct {
	method string
	path   string
	body   string
	header map[string]string
}

func (e *env) do(t *testing.T, c call) (*http.Response, string) {
	t.Helper()
	var body *strings.Reader
	if c.body == "" {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(c.body)
	}
	req, err := http.NewRequest(c.method, e.server.URL+c.path, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(raw)
}

func (e *env) internal(t *testing.T, method, path, body string) (*http.Response, string) {
	return e.do(t, call{method, path, body, map[string]string{"Authorization": "Bearer " + dispatchKey}})
}

func (e *env) admin(t *testing.T, method, path, body string) (*http.Response, string) {
	return e.do(t, call{method, path, body, map[string]string{"Authorization": "Bearer " + adminKey}})
}

func TestDispatchKeyCannotReachAdmin(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, call{http.MethodGet, "/admin/collections", "",
		map[string]string{"Authorization": "Bearer " + dispatchKey}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if strings.Contains(body, dispatchKey) || strings.Contains(body, adminKey) {
		t.Fatalf("401 body leaks configuration: %s", body)
	}
}

func TestAdminKeyCannotReachDispatch(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, call{http.MethodGet, relayv1.DispatchBasePath + "/models", "",
		map[string]string{"Authorization": "Bearer " + adminKey}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMissingKeyIsUnauthorized(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{relayv1.DispatchBasePath + "/models", "/admin/collections"} {
		resp, _ := e.do(t, call{http.MethodGet, path, "", nil})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestEmptyConfiguredKeyDeniesEveryone(t *testing.T) {
	srv := &Server{DispatchKey: "", AdminKey: "", Health: stubHealth{}}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, path := range []string{relayv1.DispatchBasePath + "/models", relayv1.AdminBasePath + "/collections"} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer ")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401 (an unset key must not open the door)", path, resp.StatusCode)
		}
	}
}

// Bearer 前缀大小写不敏感、其后允许空白，与 model-surge-upstream 的约定一致。
func TestBearerPrefixIsCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	for _, header := range []string{"bearer " + dispatchKey, "BEARER " + dispatchKey, "Bearer   " + dispatchKey} {
		resp, _ := e.do(t, call{http.MethodGet, relayv1.DispatchBasePath + "/models", "",
			map[string]string{"Authorization": header}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("header %q status = %d, want 200", header, resp.StatusCode)
		}
	}
}

// 健康检查免鉴权：探针与编排不持有密钥，带密钥反而是配置错误的信号。
func TestHealthNeedsNoKey(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, call{http.MethodGet, relayv1.HealthPath, "", nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 without any key", resp.StatusCode)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Ready || !got.Database || !got.Cache || !got.Upstream {
		t.Fatalf("health = %+v", got)
	}
}

func TestHealthUnreadyWhenDatabaseDown(t *testing.T) {
	e := newEnv(t)
	e.health.dbErr = errors.New("connection refused")
	resp, body := e.do(t, call{http.MethodGet, relayv1.HealthPath, "", nil})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Ready || got.Database {
		t.Fatalf("health = %+v", got)
	}
}

func TestHealthDegradedCacheStaysReady(t *testing.T) {
	e := newEnv(t)
	e.health.cache = false
	e.health.upstream = false
	resp, body := e.do(t, call{http.MethodGet, relayv1.HealthPath, "", nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (only PG gates readiness)", resp.StatusCode)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Ready || got.Cache || got.Upstream {
		t.Fatalf("health = %+v", got)
	}
}

func TestModelsEndpoint(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodGet, relayv1.DispatchBasePath+"/models", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got relayv1.ModelsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "sonnet" {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestDispatchEndpointReturnsTargetAndProvenance(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch",
		`{"model":"sonnet","inbound_protocol":"anthropic","client_key":"sk-client","request_id":"req-1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var got relayv1.DispatchResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Target.ModelID != "kimi-1/k3" || got.Target.Headers["Authorization"] != "Bearer sk-upstream" {
		t.Fatalf("target = %+v", got.Target)
	}
	if got.Decision.Collection != "c1" || got.Decision.Group != "primary" {
		t.Fatalf("decision = %+v", got.Decision)
	}
	if len(got.Decision.Candidates) != 1 || got.Decision.Candidates[0].Phase != "standard" {
		t.Fatalf("candidates = %+v, want one standard candidate", got.Decision.Candidates)
	}
}

func TestDispatchEndpointMapsErrorStatus(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch",
		`{"model":"sonnet","client_key":"wrong","request_id":"req-1"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	var env relayv1.ErrorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.Code != string(apperr.Unauthorized) {
		t.Fatalf("code = %s", env.Error.Code)
	}
}

func TestMalformedBodyIsInvalidRequest(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch", `{not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestResultsEndpointIsIdempotentShape(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/results",
		`{"report_id":"rep-1","request_id":"req-1","model_id":"kimi-1/k3","outcome":"normal"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"applied":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestAdminCollectionRoutes(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", http.MethodGet, "/admin/collections", "", http.StatusOK},
		{"create", http.MethodPost, "/admin/collections", `{"name":"c2","note":"n"}`, http.StatusCreated},
		{"get", http.MethodGet, "/admin/collections/c1", "", http.StatusOK},
		{"update", http.MethodPut, "/admin/collections/c1", `{"note":"edited"}`, http.StatusNoContent},
		{"delete", http.MethodDelete, "/admin/collections/c1", "", http.StatusNoContent},
		{"snapshot", http.MethodGet, "/admin/collections/c1/snapshot", "", http.StatusOK},
		{"list groups", http.MethodGet, "/admin/collections/c1/groups", "", http.StatusOK},
		{"create group", http.MethodPost, "/admin/collections/c1/groups", `{"name":"g","type":"fast"}`, http.StatusCreated},
		{"update group", http.MethodPut, "/admin/collections/c1/groups/g", `{"type":"cheap"}`, http.StatusNoContent},
		{"delete group", http.MethodDelete, "/admin/collections/c1/groups/g", "", http.StatusNoContent},
		{"replace members", http.MethodPut, "/admin/collections/c1/groups/g/members", `{"members":["kimi-1/k3"]}`, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.admin(t, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d, body %s", resp.StatusCode, tc.want, body)
			}
		})
	}
	if fmt.Sprint(e.collections.members) != "[kimi-1/k3]" {
		t.Fatalf("members = %v, order must reach the repo", e.collections.members)
	}
}

func TestAdminStrategyRoutes(t *testing.T) {
	e := newEnv(t)
	resp, body := e.admin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["primary"],"overflow":{"enabled":true,"threshold_tokens":100000,"compact_groups":["compact"]}}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put status = %d, body %s", resp.StatusCode, body)
	}
	st := e.collections.strategy
	if st.PriorityChain[0] != "primary" || !st.Overflow.Enabled ||
		st.Overflow.ThresholdTokens != 100000 || st.Overflow.CompactGroups[0] != "compact" {
		t.Fatalf("stored strategy = %+v", st)
	}

	resp, body = e.admin(t, http.MethodGet, "/admin/collections/c1/strategy", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"threshold_tokens":100000`) {
		t.Fatalf("body = %s, want the stored strategy back", body)
	}
}

func TestAdminStrategyValidationErrorMapsTo400(t *testing.T) {
	e := newEnv(t)
	e.collections.err = apperr.Field(apperr.InvalidRequest, "priority_chain", `group "ghost" does not exist`)
	resp, _ := e.admin(t, http.MethodPut, "/admin/collections/c1/strategy",
		`{"priority_chain":["ghost"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestStrategyDryRunSharesComposeCore 试运行与真实调度共用 compose 核：
// 看到的序列就是上线后的序列，且不产生任何上游流量。
func TestStrategyDryRunSharesComposeCore(t *testing.T) {
	e := newEnv(t)
	resp, body := e.admin(t, http.MethodPost, "/admin/collections/c1/strategy/dry-run",
		`{"est_tokens":100}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"kimi-1/k3"`) || !strings.Contains(body, `"standard"`) {
		t.Fatalf("body = %s, want the composed sequence", body)
	}
	if e.runStates.resetID != "" {
		t.Fatal("dry-run must not touch runtime state")
	}
}

func TestMigratePoliciesReturnsReport(t *testing.T) {
	e := newEnv(t)
	resp, body := e.admin(t, http.MethodPost, "/admin/migrate-policies", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if e.migrator.calls != 1 {
		t.Fatalf("migrator calls = %d, want 1", e.migrator.calls)
	}
	if !strings.Contains(body, `"mapped"`) || !strings.Contains(body, `"preset"`) {
		t.Fatalf("body = %s, want the migration report", body)
	}
}

// TestMigratePoliciesWithoutMigrator 未接线的部署不该 500，而是明说功能不可用。
func TestMigratePoliciesWithoutMigrator(t *testing.T) {
	e := newEnv(t)
	srv := &Server{Health: e.health, AdminKey: adminKey, DispatchKey: dispatchKey}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/migrate-policies", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// TestPolicyRoutesAreGone 脚本策略的管理面整体下线，旧路径必须 404，
// 而不是静默漏到别的 handler 上。
func TestPolicyRoutesAreGone(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/admin/policies", "/admin/policies/preset", "/admin/policies/preset/dry-run"} {
		resp, _ := e.admin(t, http.MethodGet, path, "")
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 404/405", path, resp.StatusCode)
		}
	}
}

func TestAdminUserModelRoutes(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", http.MethodGet, "/admin/user-models", "", http.StatusOK},
		{"create", http.MethodPost, "/admin/user-models",
			`{"name":"opus","collection":"c1","client_key":"sk","enabled":true}`, http.StatusCreated},
		{"get", http.MethodGet, "/admin/user-models/opus", "", http.StatusOK},
		{"update", http.MethodPut, "/admin/user-models/opus",
			`{"collection":"c1","client_key":"sk2","enabled":false}`, http.StatusOK},
		{"delete", http.MethodDelete, "/admin/user-models/opus", "", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.admin(t, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d, body %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestUserModelResponseHidesClientKey(t *testing.T) {
	e := newEnv(t)
	_, body := e.admin(t, http.MethodPost, "/admin/user-models",
		`{"name":"opus","collection":"c1","client_key":"sk-super-secret","enabled":true}`)
	if strings.Contains(body, "sk-super-secret") {
		t.Fatalf("response leaks the client key: %s", body)
	}
}

func TestRuntimeRoutes(t *testing.T) {
	e := newEnv(t)
	e.runStates.states = []runstate.State{{ModelID: "kimi-1/k3", ConsecutiveFailures: 2}}
	resp, body := e.admin(t, http.MethodGet, "/admin/runtime", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "kimi-1/k3") || !strings.Contains(body, `"consecutive_failures":2`) {
		t.Fatalf("body = %s", body)
	}

	// model_id 含斜杠，路由必须整段捕获。
	resp, body = e.admin(t, http.MethodDelete, "/admin/runtime/kimi-1/k3", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body %s", resp.StatusCode, body)
	}
	if e.runStates.resetID != "kimi-1/k3" {
		t.Fatalf("reset id = %q, want the full slash-bearing id", e.runStates.resetID)
	}
}

func TestRuntimeResetUnknownTargetIs404(t *testing.T) {
	e := newEnv(t)
	e.runStates.err = apperr.New(apperr.NotFound, "no runtime state")
	resp, _ := e.admin(t, http.MethodDelete, "/admin/runtime/ghost/x", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUnknownMethodOnKnownPath(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.internal(t, http.MethodDelete, relayv1.DispatchBasePath+"/models", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}
