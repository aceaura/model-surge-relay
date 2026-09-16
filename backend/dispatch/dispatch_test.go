package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

type fakeUserModels struct {
	model usermodel.UserModel
	err   error
	// authCalls 记录鉴权入参，用于断言协议与密钥被原样传下去。
	authCalls []string
}

func (f *fakeUserModels) Authenticate(_ context.Context, name, protocol, clientKey string) (usermodel.UserModel, error) {
	f.authCalls = append(f.authCalls, fmt.Sprintf("%s|%s|%s", name, protocol, clientKey))
	if f.err != nil {
		return usermodel.UserModel{}, f.err
	}
	return f.model, nil
}

func (f *fakeUserModels) List(context.Context) ([]usermodel.UserModel, error) {
	return []usermodel.UserModel{f.model}, nil
}

type fakeCollections struct {
	snap collection.Snapshot
	err  error
}

func (f fakeCollections) Snapshot(context.Context, string) (collection.Snapshot, error) {
	return f.snap, f.err
}

type fakePolicies struct {
	policy policy.Policy
	err    error
}

func (f fakePolicies) Get(context.Context, string) (policy.Policy, error) {
	return f.policy, f.err
}

type fakeEngine struct {
	calls    int
	decision policy.Decision
	err      error
	// seen 保留最后一次输入，供断言策略确实拿到了快照与运行态。
	seen policy.Input
}

func (f *fakeEngine) Execute(_ context.Context, _ policy.Policy, in policy.Input) (policy.Decision, error) {
	f.calls++
	f.seen = in
	return f.decision, f.err
}

type fakeRunStates struct {
	states   map[string]runstate.State
	loadErr  error
	applied  bool
	applyIn  runstate.ResultReport
	applyErr error
}

func (f *fakeRunStates) Load(_ context.Context, ids []string) (map[string]runstate.State, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make(map[string]runstate.State, len(ids))
	for _, id := range ids {
		if s, ok := f.states[id]; ok {
			out[id] = s
			continue
		}
		out[id] = runstate.State{ModelID: id}
	}
	return out, nil
}

func (f *fakeRunStates) ApplyReport(_ context.Context, rep runstate.ResultReport, _ runstate.Thresholds) (bool, error) {
	f.applyIn = rep
	return f.applied, f.applyErr
}

type fakeResolver struct {
	// fail 按 model_id 指定解析失败。
	fail  map[string]error
	calls []string
	// latency 让调度耗时可测：Windows 的时钟粒度粗到能把整次调度记成 0s。
	latency time.Duration
}

func (f *fakeResolver) Resolve(_ context.Context, modelID string) (upstreamclient.ResolvedTarget, error) {
	f.calls = append(f.calls, modelID)
	time.Sleep(f.latency)
	if err, ok := f.fail[modelID]; ok {
		return upstreamclient.ResolvedTarget{}, err
	}
	return upstreamclient.ResolvedTarget{
		ModelID:     modelID,
		Account:     strings.Split(modelID, "/")[0],
		ProviderID:  "moonshot",
		Protocol:    relayv1.ProtocolAnthropic,
		BaseURL:     "https://example.invalid/coding",
		NativeModel: "k3",
		Headers:     map[string]string{"Authorization": "Bearer sk-upstream-secret"},
	}, nil
}

type recordingLogger struct{ entries []LogEntry }

func (l *recordingLogger) Dispatched(e LogEntry) { l.entries = append(l.entries, e) }

// twoGroupSnapshot 是多数测试共用的两组四成员布局。
func twoGroupSnapshot() collection.Snapshot {
	return snapshot(
		group("primary", "fast", 0, member("kimi-1/k3", 0), member("kimi-2/k3", 1)),
		group("backup", "cheap", 1, member("ark-1/ds", 0), member("ark-2/ds", 1)),
	)
}

type harness struct {
	svc       *Service
	users     *fakeUserModels
	policies  fakePolicies
	engine    *fakeEngine
	runStates *fakeRunStates
	resolver  *fakeResolver
	logger    *recordingLogger
}

func newHarness(t *testing.T, um usermodel.UserModel, snap collection.Snapshot) *harness {
	t.Helper()
	h := &harness{
		users:     &fakeUserModels{model: um},
		policies:  fakePolicies{policy: policy.Policy{Name: um.Policy, Language: policy.LangLua, Version: 4}},
		engine:    &fakeEngine{},
		runStates: &fakeRunStates{states: map[string]runstate.State{}},
		resolver:  &fakeResolver{fail: map[string]error{}},
		logger:    &recordingLogger{},
	}
	h.svc = &Service{
		UserModels:  h.users,
		Collections: fakeCollections{snap: snap},
		Policies:    h.policies,
		Engine:      h.engine,
		RunStates:   h.runStates,
		Resolver:    h.resolver,
		Thresholds:  runstate.Thresholds{FailureThreshold: 3, CooldownDuration: time.Minute},
		Logger:      h.logger,
	}
	return h
}

func boundUserModel() usermodel.UserModel {
	return usermodel.UserModel{Name: "sonnet", Collection: "c1", Policy: "preset", Enabled: true}
}

func unboundUserModel() usermodel.UserModel {
	return usermodel.UserModel{Name: "sonnet", Collection: "c1", Enabled: true}
}

func request() relayv1.DispatchRequest {
	return relayv1.DispatchRequest{
		Model:           "sonnet",
		InboundProtocol: relayv1.ProtocolAnthropic,
		ClientKey:       "sk-client",
		RequestID:       "req-1",
	}
}

func asAppErr(t *testing.T, err error) *apperr.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var e *apperr.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *apperr.Error", err)
	}
	return e
}

func TestDispatchExecutesPolicyExactlyOnce(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-2/k3", "kimi-1/k3"}}

	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if h.engine.calls != 1 {
		t.Fatalf("policy executed %d times, want exactly 1", h.engine.calls)
	}
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the policy's first candidate", resp.Target.ModelID)
	}
}

func TestDispatchPolicyStillRunsOnceWhenAllCandidatesFail(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3", "kimi-2/k3"}}
	h.resolver.fail["kimi-1/k3"] = apperr.New(apperr.TargetUnavailable, "boom 1")
	h.resolver.fail["kimi-2/k3"] = apperr.New(apperr.TargetUnavailable, "boom 2")

	if _, err := h.svc.Dispatch(context.Background(), request()); err == nil {
		t.Fatal("expected failure")
	}
	if h.engine.calls != 1 {
		t.Fatalf("policy executed %d times, want 1 (no retry, no fallback)", h.engine.calls)
	}
}

func TestDispatchPassesSnapshotAndRuntimeToPolicy(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.runStates.states["ark-1/ds"] = runstate.State{
		ModelID: "ark-1/ds", ConsecutiveFailures: 2,
		Usage: runstate.Usage{InputTokens: 100, RequestCount: 3},
	}
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3"}}

	req := request()
	req.EstTokens = 4096
	req.TriedIDs = []string{"kimi-2/k3"}
	if _, err := h.svc.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	in := h.engine.seen
	if in.Collection.Name != "c1" || len(in.Collection.Groups) != 2 {
		t.Fatalf("collection input = %+v", in.Collection)
	}
	if in.Request.EstTokens != 4096 || in.Request.RequestID != "req-1" {
		t.Fatalf("request input = %+v", in.Request)
	}
	if fmt.Sprint(in.Request.TriedIDs) != "[kimi-2/k3]" {
		t.Fatalf("tried ids = %v", in.Request.TriedIDs)
	}
	st := in.Runtime["ark-1/ds"]
	if st.ConsecutiveFailures != 2 || st.InputTokens != 100 || st.RequestCount != 3 {
		t.Fatalf("runtime input = %+v", st)
	}
	if len(in.Runtime) != 4 {
		t.Fatalf("runtime entries = %d, want one per member", len(in.Runtime))
	}
}

func TestDispatchExposesCoolingUntilToPolicy(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	until := time.Now().Add(time.Hour)
	h.runStates.states["kimi-1/k3"] = runstate.State{ModelID: "kimi-1/k3", Cooling: true, CoolingUntil: until}
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-2/k3"}}
	if _, err := h.svc.Dispatch(context.Background(), request()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	st := h.engine.seen.Runtime["kimi-1/k3"]
	if !st.Cooling || st.CoolingUntil != until.Unix() {
		t.Fatalf("cooling state = %+v, want cooling until %d", st, until.Unix())
	}
}

func TestDispatchFallsBackToGroupOrderWithoutPolicy(t *testing.T) {
	h := newHarness(t, unboundUserModel(), twoGroupSnapshot())
	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if h.engine.calls != 0 {
		t.Fatalf("engine called %d times for an unbound user model", h.engine.calls)
	}
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q, want first member of first group", resp.Target.ModelID)
	}
	if resp.Decision.Policy != "" || resp.Decision.PolicyVersion != 0 {
		t.Fatalf("decision = %+v, want no policy provenance", resp.Decision)
	}
}

func TestDispatchEmptyCandidateListSkipsResolve(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{}}

	_, err := h.svc.Dispatch(context.Background(), request())
	e := asAppErr(t, err)
	if e.Code != apperr.TargetUnavailable {
		t.Fatalf("code = %s, want target_unavailable", e.Code)
	}
	if len(h.resolver.calls) != 0 {
		t.Fatalf("resolve called %v, want no upstream traffic for an empty list", h.resolver.calls)
	}
}

func TestDispatchAllFilteredOutSkipsResolve(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"outside/1", "outside/2"}}
	if _, err := h.svc.Dispatch(context.Background(), request()); err == nil {
		t.Fatal("expected target_unavailable")
	}
	if len(h.resolver.calls) != 0 {
		t.Fatalf("resolve called %v", h.resolver.calls)
	}
}

func TestDispatchFallsForwardToNextCandidateOnResolveFailure(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3", "kimi-2/k3"}}
	h.resolver.fail["kimi-1/k3"] = apperr.New(apperr.TargetUnavailable, "upstream 503")

	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the second candidate", resp.Target.ModelID)
	}
	if fmt.Sprint(h.resolver.calls) != "[kimi-1/k3 kimi-2/k3]" {
		t.Fatalf("resolve calls = %v, want sequential attempts", h.resolver.calls)
	}
	var found bool
	for _, s := range resp.Decision.Skipped {
		if s.ModelID == "kimi-1/k3" && s.Reason == relayv1.SkipResolveFailed {
			found = true
			if !strings.Contains(s.Detail, "503") {
				t.Fatalf("skip detail = %q, want the upstream reason", s.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("skipped = %+v, want a resolve_failed entry", resp.Decision.Skipped)
	}
}

func TestDispatchAllResolveFailuresCarrySummary(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3", "ark-1/ds"}}
	h.resolver.fail["kimi-1/k3"] = apperr.New(apperr.TargetUnavailable, "first down")
	h.resolver.fail["ark-1/ds"] = apperr.New(apperr.TargetUnavailable, "second down")

	_, err := h.svc.Dispatch(context.Background(), request())
	e := asAppErr(t, err)
	if e.Code != apperr.TargetUnavailable || !e.Retryable {
		t.Fatalf("error = %+v, want retryable target_unavailable", e)
	}
	for _, want := range []string{"kimi-1/k3", "first down", "ark-1/ds", "second down"} {
		if !strings.Contains(e.Message, want) {
			t.Fatalf("message %q missing %q", e.Message, want)
		}
	}
}

func TestDispatchCarriesFullTargetAndProvenance(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"ark-1/ds"}, Note: "cheapest first"}

	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tgt := resp.Target
	if tgt.ModelID != "ark-1/ds" || tgt.Account != "ark-1" || tgt.ProviderID != "moonshot" ||
		tgt.Protocol != relayv1.ProtocolAnthropic || tgt.BaseURL == "" || tgt.NativeModel != "k3" {
		t.Fatalf("target = %+v, want the full upstream description", tgt)
	}
	if tgt.Headers["Authorization"] != "Bearer sk-upstream-secret" {
		t.Fatalf("headers = %v, credentials must pass through verbatim", tgt.Headers)
	}
	d := resp.Decision
	if d.Policy != "preset" || d.PolicyVersion != 4 || d.Collection != "c1" ||
		d.Group != "backup" || d.GroupType != "cheap" || d.Note != "cheapest first" {
		t.Fatalf("decision = %+v", d)
	}
	if resp.RequestID != "req-1" {
		t.Fatalf("request id = %q", resp.RequestID)
	}
}

func TestDispatchStringRedactsCredentials(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3"}}
	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(resp.String(), "sk-upstream-secret") {
		t.Fatalf("String() leaks the credential: %s", resp)
	}
}

func TestDispatchPropagatesAuthFailure(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.users.err = apperr.New(apperr.Unauthorized, "client key mismatch")
	_, err := h.svc.Dispatch(context.Background(), request())
	if e := asAppErr(t, err); e.Code != apperr.Unauthorized {
		t.Fatalf("code = %s, want unauthorized", e.Code)
	}
	if h.engine.calls != 0 || len(h.resolver.calls) != 0 {
		t.Fatal("an unauthenticated request must not reach the policy or upstream")
	}
}

func TestDispatchPassesProtocolAndKeyToAuth(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3"}}
	if _, err := h.svc.Dispatch(context.Background(), request()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fmt.Sprint(h.users.authCalls) != "[sonnet|anthropic|sk-client]" {
		t.Fatalf("auth calls = %v", h.users.authCalls)
	}
}

func TestDispatchRequiresModel(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	req := request()
	req.Model = "  "
	_, err := h.svc.Dispatch(context.Background(), req)
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "model" {
		t.Fatalf("error = %+v", e)
	}
}

func TestDispatchPropagatesPolicyTimeoutAsRetryable(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.err = apperr.New(apperr.PolicyTimeout, "policy timed out")
	_, err := h.svc.Dispatch(context.Background(), request())
	e := asAppErr(t, err)
	if e.Code != apperr.PolicyTimeout || !e.Retryable {
		t.Fatalf("error = %+v, want retryable policy_timeout", e)
	}
	if len(h.resolver.calls) != 0 {
		t.Fatal("a timed-out policy must not lead to resolution")
	}
}

func TestDispatchPropagatesPolicyErrorAsNonRetryable(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.err = apperr.New(apperr.PolicyError, "attempt to index a nil value")
	_, err := h.svc.Dispatch(context.Background(), request())
	e := asAppErr(t, err)
	if e.Code != apperr.PolicyError || e.Retryable {
		t.Fatalf("error = %+v, want non-retryable policy_error", e)
	}
}

func TestDispatchTriedIDsExcludeCandidate(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.decision = policy.Decision{Candidates: []string{"kimi-1/k3", "kimi-2/k3"}}
	req := request()
	req.TriedIDs = []string{"kimi-1/k3"}

	resp, err := h.svc.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the untried candidate", resp.Target.ModelID)
	}
	if fmt.Sprint(h.resolver.calls) != "[kimi-2/k3]" {
		t.Fatalf("resolve calls = %v, tried target must not be resolved", h.resolver.calls)
	}
}

func TestDispatchLogsProvenanceWithoutCredentials(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.resolver.latency = 5 * time.Millisecond
	h.engine.decision = policy.Decision{Candidates: []string{"outside/1", "kimi-1/k3"}}
	if _, err := h.svc.Dispatch(context.Background(), request()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(h.logger.entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(h.logger.entries))
	}
	e := h.logger.entries[0]
	if e.RequestID != "req-1" || e.UserModel != "sonnet" || e.Policy != "preset" || e.PolicyVersion != 4 {
		t.Fatalf("entry = %+v", e)
	}
	if e.Selected != "kimi-1/k3" || e.Candidates != 1 || len(e.Skipped) != 1 {
		t.Fatalf("entry = %+v", e)
	}
	if e.Duration <= 0 {
		t.Fatalf("duration = %v, want positive", e.Duration)
	}
	if strings.Contains(fmt.Sprintf("%+v", e), "sk-") {
		t.Fatalf("log entry leaks a key: %+v", e)
	}
}

func TestDispatchLogsFailureReason(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.engine.err = apperr.New(apperr.PolicyError, "boom")
	if _, err := h.svc.Dispatch(context.Background(), request()); err == nil {
		t.Fatal("expected failure")
	}
	if len(h.logger.entries) != 1 || !strings.Contains(h.logger.entries[0].Error, "boom") {
		t.Fatalf("entries = %+v, want the failure recorded", h.logger.entries)
	}
}

func TestReportForwardsToRunState(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.runStates.applied = true
	resp, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-1", RequestID: "req-1", ModelID: "kimi-1/k3",
		Outcome: string(runstate.OutcomeNormal),
		Usage:   relayv1.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !resp.Applied {
		t.Fatal("applied = false, want true")
	}
	in := h.runStates.applyIn
	if in.ReportID != "rep-1" || in.ModelID != "kimi-1/k3" || in.Outcome != runstate.OutcomeNormal {
		t.Fatalf("forwarded report = %+v", in)
	}
	if in.Usage != (runstate.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5}) {
		t.Fatalf("forwarded usage = %+v", in.Usage)
	}
}

func TestReportDuplicateReturnsNotApplied(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.runStates.applied = false
	resp, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-1", ModelID: "kimi-1/k3", Outcome: string(runstate.OutcomeAbnormal),
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if resp.Applied {
		t.Fatal("a replayed report id must report applied=false")
	}
}

func TestReportPropagatesValidationError(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	h.runStates.applyErr = apperr.Field(apperr.InvalidRequest, "outcome", "unknown outcome")
	_, err := h.svc.Report(context.Background(), relayv1.ResultReport{ReportID: "r", ModelID: "m", Outcome: "?"})
	if e := asAppErr(t, err); e.Code != apperr.InvalidRequest {
		t.Fatalf("code = %s, want invalid_request", e.Code)
	}
}

func TestModelsSummarizesUserModels(t *testing.T) {
	h := newHarness(t, boundUserModel(), twoGroupSnapshot())
	resp, err := h.svc.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("models = %+v", resp.Models)
	}
	m := resp.Models[0]
	if m.Name != "sonnet" || m.Collection != "c1" || m.Policy != "preset" || !m.Enabled {
		t.Fatalf("summary = %+v", m)
	}
}
