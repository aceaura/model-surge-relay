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
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/runstate"
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
	snap    collection.Snapshot
	created collection.Collection
	groups  []collection.Group
	members []string
	err     error
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
	return collection.Collection{Name: name}, nil
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

type stubPolicies struct {
	record  policy.Policy
	err     error
	deleted string
}

func (p *stubPolicies) Create(_ context.Context, in policy.Policy) (policy.Policy, error) {
	if p.err != nil {
		return policy.Policy{}, p.err
	}
	in.Version = 1
	p.record = in
	return in, nil
}

func (p *stubPolicies) Update(_ context.Context, in policy.Policy) (policy.Policy, error) {
	if p.err != nil {
		return policy.Policy{}, p.err
	}
	in.Version = p.record.Version + 1
	p.record = in
	return in, nil
}

func (p *stubPolicies) Get(context.Context, string) (policy.Policy, error) {
	return p.record, p.err
}

func (p *stubPolicies) List(context.Context) ([]policy.Policy, error) {
	return []policy.Policy{p.record}, p.err
}

func (p *stubPolicies) Delete(_ context.Context, name string) error {
	if p.err != nil {
		return p.err
	}
	p.deleted = name
	return nil
}

type stubEngine struct {
	decision policy.Decision
	err      error
	seen     policy.Input
	calls    int
}

func (e *stubEngine) Execute(_ context.Context, _ policy.Policy, in policy.Input) (policy.Decision, error) {
	e.calls++
	e.seen = in
	return e.decision, e.err
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
	policies    *stubPolicies
	engine      *stubEngine
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
		policies:    &stubPolicies{record: policy.Policy{Name: "preset", Language: policy.LangLua, Source: "return {}", Version: 3}},
		engine:      &stubEngine{decision: policy.Decision{Candidates: []string{"kimi-1/k3"}}},
		users:       &stubUserModels{model: usermodel.UserModel{Name: "sonnet", Collection: "c1", ClientKey: "sk-client", Enabled: true}},
		runStates:   &stubRunStates{},
		health:      &stubHealth{cache: true, upstream: true},
	}
	srv := &Server{
		Dispatch: &dispatch.Service{
			UserModels:  e.users,
			Collections: e.collections,
			Policies:    e.policies,
			Engine:      e.engine,
			RunStates:   e.runStates,
			Resolver:    stubResolver{},
			Thresholds:  runstate.Thresholds{FailureThreshold: 3, CooldownDuration: time.Minute},
		},
		Health:      e.health,
		Collections: e.collections,
		Policies:    e.policies,
		Engine:      e.engine,
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
	return e.do(t, call{method, path, body, map[string]string{"X-Admin-Key": adminKey}})
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

func TestAdminKeyCannotReachInternal(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, call{http.MethodGet, relayv1.InternalBasePath + "/models", "",
		map[string]string{"X-Admin-Key": adminKey}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMissingKeyIsUnauthorized(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{relayv1.InternalBasePath + "/models", "/admin/collections"} {
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
	req, _ := http.NewRequest(http.MethodGet, ts.URL+relayv1.InternalBasePath+"/health", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (an unset key must not open the door)", resp.StatusCode)
	}
}

func TestHealthReportsThreeComponents(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ok" || got.Database != "ok" || got.Cache != "ok" || got.Upstream != "ok" {
		t.Fatalf("health = %+v", got)
	}
}

func TestHealthUnreadyWhenDatabaseDown(t *testing.T) {
	e := newEnv(t)
	e.health.dbErr = errors.New("connection refused")
	resp, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/health", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "unready" || got.Database != "unreachable" {
		t.Fatalf("health = %+v", got)
	}
}

func TestHealthDegradedCacheStaysReady(t *testing.T) {
	e := newEnv(t)
	e.health.cache = false
	e.health.upstream = false
	resp, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (only PG gates readiness)", resp.StatusCode)
	}
	if !strings.Contains(body, `"cache":"disabled"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestModelsEndpoint(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodGet, relayv1.InternalBasePath+"/models", "")
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
	resp, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/dispatch",
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
}

func TestDispatchEndpointMapsErrorStatus(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/dispatch",
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
	resp, _ := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/dispatch", `{not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestResultsEndpointIsIdempotentShape(t *testing.T) {
	e := newEnv(t)
	resp, body := e.internal(t, http.MethodPost, relayv1.InternalBasePath+"/results",
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

func TestAdminPolicyRoutes(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", http.MethodGet, "/admin/policies", "", http.StatusOK},
		{"create", http.MethodPost, "/admin/policies", `{"name":"rr","language":"lua","source":"return {}"}`, http.StatusCreated},
		{"get", http.MethodGet, "/admin/policies/rr", "", http.StatusOK},
		{"update", http.MethodPut, "/admin/policies/rr", `{"language":"lua","source":"return {1}"}`, http.StatusOK},
		{"delete", http.MethodDelete, "/admin/policies/rr", "", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.admin(t, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d, body %s", resp.StatusCode, tc.want, body)
			}
		})
	}
	if e.policies.deleted != "rr" {
		t.Fatalf("deleted = %q", e.policies.deleted)
	}
}

func TestDeletePolicyConflictListsReferences(t *testing.T) {
	e := newEnv(t)
	e.policies.err = apperr.New(apperr.Conflict, `policy "preset" is bound to user models: sonnet, opus`)
	resp, body := e.admin(t, http.MethodDelete, "/admin/policies/preset", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	for _, want := range []string{"sonnet", "opus"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s missing referencing user model %q", body, want)
		}
	}
}

// TestDryRunIsIsolated 断言试运行只读快照：不加载运行态、不解析目标。
func TestDryRunIsIsolated(t *testing.T) {
	e := newEnv(t)
	resp, body := e.admin(t, http.MethodPost, "/admin/policies/preset/dry-run",
		`{"collection":"c1","request":{"user_model":"sonnet","est_tokens":100},
		  "runtime":{"kimi-1/k3":{"cooling":true,"consecutive_failures":9}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if e.engine.calls != 1 {
		t.Fatalf("engine calls = %d, want 1", e.engine.calls)
	}
	if !strings.Contains(body, `"policy":"preset"`) || !strings.Contains(body, `"policy_version":3`) {
		t.Fatalf("body = %s, want policy provenance", body)
	}
	// 传入的运行态被原样交给脚本，而不是从库里读真实状态。
	if st := e.engine.seen.Runtime["kimi-1/k3"]; !st.Cooling || st.ConsecutiveFailures != 9 {
		t.Fatalf("runtime input = %+v, dry-run must use the supplied state", st)
	}
	if e.runStates.resetID != "" {
		t.Fatal("dry-run must not touch runtime state")
	}
}

func TestDryRunSurfacesPolicyError(t *testing.T) {
	e := newEnv(t)
	e.engine.err = apperr.New(apperr.PolicyError, "attempt to index a nil value")
	resp, body := e.admin(t, http.MethodPost, "/admin/policies/preset/dry-run", `{"collection":"c1"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(body, "policy_error") {
		t.Fatalf("body = %s", body)
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
			`{"name":"opus","collection":"c1","policy":"preset","client_key":"sk","enabled":true}`, http.StatusCreated},
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
	resp, _ := e.internal(t, http.MethodDelete, relayv1.InternalBasePath+"/models", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}
