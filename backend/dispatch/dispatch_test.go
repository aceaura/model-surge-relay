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
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/strategy"
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

// member 造一个已知且启用的成员，便于用最少噪声表达测试意图。
func member(id string, pos int) collection.Member {
	return collection.Member{ModelID: id, Position: pos, Enabled: true, Known: true}
}

func group(name, typ string, pos int, members ...collection.Member) collection.GroupSnapshot {
	return collection.GroupSnapshot{Name: name, Type: typ, Position: pos, Members: members}
}

// snapshot 是多数测试共用的两组四成员布局。
func twoGroupSnapshot() collection.Snapshot {
	return collection.Snapshot{
		Name:      "c1",
		UpdatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
		Groups: []collection.GroupSnapshot{
			group("primary", "fast", 0, member("kimi-1/k3", 0), member("kimi-2/k3", 1)),
			group("backup", "cheap", 1, member("ark-1/ds", 0), member("ark-2/ds", 1)),
		},
	}
}

type harness struct {
	svc       *Service
	users     *fakeUserModels
	runStates *fakeRunStates
	resolver  *fakeResolver
	logger    *recordingLogger
}

func newHarness(t *testing.T, um usermodel.UserModel, snap collection.Snapshot) *harness {
	t.Helper()
	h := &harness{
		users:     &fakeUserModels{model: um},
		runStates: &fakeRunStates{states: map[string]runstate.State{}},
		resolver:  &fakeResolver{fail: map[string]error{}},
		logger:    &recordingLogger{},
	}
	h.svc = &Service{
		UserModels:  h.users,
		Collections: fakeCollections{snap: snap},
		RunStates:   h.runStates,
		Resolver:    h.resolver,
		Thresholds:  runstate.Thresholds{FailureThreshold: 3, CooldownDuration: time.Minute},
		Logger:      h.logger,
	}
	return h
}

func testUserModel() usermodel.UserModel {
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

func TestDispatchFollowsGroupOrderByDefault(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q, want first member of first group", resp.Target.ModelID)
	}
	if got := resp.Decision.Candidates; fmt.Sprint(got) !=
		fmt.Sprint([]relayv1.PhasedCandidate{
			{ModelID: "kimi-1/k3", Phase: strategy.PhaseStandard},
			{ModelID: "kimi-2/k3", Phase: strategy.PhaseStandard},
			{ModelID: "ark-1/ds", Phase: strategy.PhaseStandard},
			{ModelID: "ark-2/ds", Phase: strategy.PhaseStandard},
		}) {
		t.Fatalf("candidates = %v, want group-then-member order, all standard phase", got)
	}
}

func TestDispatchHonorsPriorityChain(t *testing.T) {
	snap := twoGroupSnapshot()
	snap.Strategy = strategy.Strategy{PriorityChain: []string{"backup", "primary"}}
	h := newHarness(t, testUserModel(), snap)

	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want the chain's first group", resp.Target.ModelID)
	}
	if resp.Decision.Group != "backup" || resp.Decision.GroupType != "cheap" {
		t.Fatalf("decision provenance = %+v", resp.Decision)
	}
}

func TestDispatchReportsGroupExhaustion(t *testing.T) {
	snap := twoGroupSnapshot()
	snap.Strategy = strategy.Strategy{PriorityChain: []string{"primary", "backup"}}
	h := newHarness(t, testUserModel(), snap)
	// 主组两人一个冷却一个已试，整组不可用 → 跳下一组并留 group_skip。
	h.runStates.states["kimi-1/k3"] = runstate.State{ModelID: "kimi-1/k3", Cooling: true}
	req := request()
	req.TriedIDs = []string{"kimi-2/k3"}

	resp, err := h.svc.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "ark-1/ds" {
		t.Fatalf("target = %q, want failover into the next group", resp.Target.ModelID)
	}
	if len(resp.Decision.GroupSkips) != 1 ||
		resp.Decision.GroupSkips[0].Group != "primary" ||
		resp.Decision.GroupSkips[0].Reason != strategy.GroupExhausted {
		t.Fatalf("group skips = %+v, want primary group_exhausted", resp.Decision.GroupSkips)
	}
}

func TestDispatchOverflowRoutesCompactThenResume(t *testing.T) {
	snap := collection.Snapshot{
		Name: "c1",
		Strategy: strategy.Strategy{
			PriorityChain: []string{"primary"},
			Overflow: strategy.Overflow{
				Enabled:         true,
				ThresholdTokens: 1000,
				CompactGroups:   []string{"compact"},
			},
		},
		Groups: []collection.GroupSnapshot{
			group("primary", "kimi", 0, member("kimi-1/k3", 0)),
			group("compact", "compact", 1, member("ds-1/flash", 0)),
		},
	}
	h := newHarness(t, testUserModel(), snap)
	req := request()
	req.EstTokens = 5000

	resp, err := h.svc.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "ds-1/flash" {
		t.Fatalf("target = %q, want the compact pool under overflow", resp.Target.ModelID)
	}
	want := []relayv1.PhasedCandidate{
		{ModelID: "ds-1/flash", Phase: strategy.PhaseCompact},
		{ModelID: "kimi-1/k3", Phase: strategy.PhaseResume},
	}
	if fmt.Sprint(resp.Decision.Candidates) != fmt.Sprint(want) {
		t.Fatalf("candidates = %v, want compact-then-resume %v", resp.Decision.Candidates, want)
	}
}

func TestDispatchBelowThresholdStaysStandard(t *testing.T) {
	snap := collection.Snapshot{
		Name: "c1",
		Strategy: strategy.Strategy{
			Overflow: strategy.Overflow{
				Enabled:         true,
				ThresholdTokens: 1000,
				CompactGroups:   []string{"compact"},
			},
		},
		Groups: []collection.GroupSnapshot{
			group("primary", "kimi", 0, member("kimi-1/k3", 0)),
			group("compact", "compact", 1, member("ds-1/flash", 0)),
		},
	}
	h := newHarness(t, testUserModel(), snap)
	req := request()
	req.EstTokens = 500

	resp, err := h.svc.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "kimi-1/k3" {
		t.Fatalf("target = %q, want the primary pool below threshold", resp.Target.ModelID)
	}
	for _, c := range resp.Decision.Candidates {
		if c.Phase != strategy.PhaseStandard {
			t.Fatalf("phase = %q below threshold, want all standard", c.Phase)
		}
	}
}

func TestDispatchCoolingMemberSkippedWithReason(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.states["kimi-1/k3"] = runstate.State{ModelID: "kimi-1/k3", Cooling: true}

	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.Target.ModelID != "kimi-2/k3" {
		t.Fatalf("target = %q, want the non-cooling member", resp.Target.ModelID)
	}
	var found bool
	for _, s := range resp.Decision.Skipped {
		if s.ModelID == "kimi-1/k3" && s.Reason == relayv1.SkipCooling {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v, want a cooling entry for kimi-1/k3", resp.Decision.Skipped)
	}
}

func TestDispatchEmptyCandidateListSkipsResolve(t *testing.T) {
	h := newHarness(t, testUserModel(), collection.Snapshot{Name: "c1"})

	_, err := h.svc.Dispatch(context.Background(), request())
	e := asAppErr(t, err)
	if e.Code != apperr.TargetUnavailable {
		t.Fatalf("code = %s, want target_unavailable", e.Code)
	}
	if len(h.resolver.calls) != 0 {
		t.Fatalf("resolve called %v, want no upstream traffic for an empty list", h.resolver.calls)
	}
}

func TestDispatchFallsForwardToNextCandidateOnResolveFailure(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
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
	snap := twoGroupSnapshot()
	snap.Strategy = strategy.Strategy{PriorityChain: []string{"primary", "backup"}}
	h := newHarness(t, testUserModel(), snap)
	h.resolver.fail["kimi-1/k3"] = apperr.New(apperr.TargetUnavailable, "first down")
	h.resolver.fail["kimi-2/k3"] = apperr.New(apperr.TargetUnavailable, "k2 down")
	h.resolver.fail["ark-1/ds"] = apperr.New(apperr.TargetUnavailable, "second down")
	h.resolver.fail["ark-2/ds"] = apperr.New(apperr.TargetUnavailable, "a2 down")

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
	snap := twoGroupSnapshot()
	snap.Strategy = strategy.Strategy{PriorityChain: []string{"backup", "primary"}}
	h := newHarness(t, testUserModel(), snap)

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
	if d.Collection != "c1" || d.Group != "backup" || d.GroupType != "cheap" {
		t.Fatalf("decision = %+v", d)
	}
	if !d.CollectionUpdatedAt.Equal(snap.UpdatedAt) {
		t.Fatalf("collection_updated_at = %v, want the snapshot's version stamp %v",
			d.CollectionUpdatedAt, snap.UpdatedAt)
	}
	if resp.RequestID != "req-1" {
		t.Fatalf("request id = %q", resp.RequestID)
	}
}

func TestDispatchStringRedactsCredentials(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	resp, err := h.svc.Dispatch(context.Background(), request())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(resp.String(), "sk-upstream-secret") {
		t.Fatalf("String() leaks the credential: %s", resp)
	}
}

func TestDispatchPropagatesAuthFailure(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.users.err = apperr.New(apperr.Unauthorized, "client key mismatch")
	_, err := h.svc.Dispatch(context.Background(), request())
	if e := asAppErr(t, err); e.Code != apperr.Unauthorized {
		t.Fatalf("code = %s, want unauthorized", e.Code)
	}
	if len(h.resolver.calls) != 0 {
		t.Fatal("an unauthenticated request must not reach upstream")
	}
}

func TestDispatchPassesProtocolAndKeyToAuth(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	if _, err := h.svc.Dispatch(context.Background(), request()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fmt.Sprint(h.users.authCalls) != "[sonnet|anthropic|sk-client]" {
		t.Fatalf("auth calls = %v", h.users.authCalls)
	}
}

func TestDispatchRequiresModel(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	req := request()
	req.Model = "  "
	_, err := h.svc.Dispatch(context.Background(), req)
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "model" {
		t.Fatalf("error = %+v", e)
	}
}

func TestDispatchTriedIDsExcludeCandidate(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
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
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.resolver.latency = 5 * time.Millisecond
	h.runStates.states["kimi-1/k3"] = runstate.State{ModelID: "kimi-1/k3", Cooling: true}
	if _, err := h.svc.Dispatch(context.Background(), request()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(h.logger.entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(h.logger.entries))
	}
	e := h.logger.entries[0]
	if e.RequestID != "req-1" || e.UserModel != "sonnet" {
		t.Fatalf("entry = %+v", e)
	}
	if e.Selected != "kimi-2/k3" || e.SelectedPhase != strategy.PhaseStandard ||
		e.Candidates != 3 || len(e.Skipped) != 1 {
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
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.users.err = apperr.New(apperr.Internal, "boom")
	if _, err := h.svc.Dispatch(context.Background(), request()); err == nil {
		t.Fatal("expected failure")
	}
	if len(h.logger.entries) != 1 || !strings.Contains(h.logger.entries[0].Error, "boom") {
		t.Fatalf("entries = %+v, want the failure recorded", h.logger.entries)
	}
}

// TestDryRunMatchesDispatchSequence 试运行与真实调度共用 compose 核，
// 这条把「看到的序列就是上线后的序列」钉成断言。
func TestDryRunMatchesDispatchSequence(t *testing.T) {
	snap := twoGroupSnapshot()
	snap.Strategy = strategy.Strategy{PriorityChain: []string{"backup", "primary"}}
	h := newHarness(t, testUserModel(), snap)

	decision, err := h.svc.DryRun(context.Background(), "c1", 0, []string{"ark-1/ds"})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(h.resolver.calls) != 0 {
		t.Fatalf("dry run resolved %v, want zero upstream traffic", h.resolver.calls)
	}
	if decision.Collection != "c1" || !decision.CollectionUpdatedAt.Equal(snap.UpdatedAt) {
		t.Fatalf("decision = %+v", decision)
	}
	want := []relayv1.PhasedCandidate{
		{ModelID: "ark-2/ds", Phase: strategy.PhaseStandard},
		{ModelID: "kimi-1/k3", Phase: strategy.PhaseStandard},
		{ModelID: "kimi-2/k3", Phase: strategy.PhaseStandard},
	}
	if fmt.Sprint(decision.Candidates) != fmt.Sprint(want) {
		t.Fatalf("candidates = %v, want %v", decision.Candidates, want)
	}
}

func TestReportForwardsToRunState(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
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

// TestReportDoesNotAccumulateUnpricedUsage 契约收下五位，运行态只累前三位。
//
// 这条把一个刻意决定钉成断言：缓存写入与推理的单价与输入输出不同（1h 缓存写入
// 约为 5m 的两倍），把它们加进同一组累计列等于用错的权重记账，而正确加权需要
// 定价模型——那在 upstream 配置中心，不在这里。
//
// 不钉住的话下一个人看到契约有五位而运行态只累三位，会当成漏了并顺手加上，
// 于是配额计算悄悄开始用错的权重。要改这个决定得先改这条测试，那时会读到理由。
func TestReportDoesNotAccumulateUnpricedUsage(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.applied = true
	if _, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-unpriced", RequestID: "req-unpriced", ModelID: "kimi-1/k3",
		Outcome: string(runstate.OutcomeNormal),
		Usage: relayv1.Usage{
			InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
			CacheWriteTokens: 40, ReasoningTokens: 50,
		},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	// runstate.Usage 只有三位加一个 RequestCount，所以「没漏进来」由类型保证；
	// 这里断言的是前三位没被新两位污染（比如错手把 CacheWrite 加进 CacheRead）。
	got := h.runStates.applyIn.Usage
	if got.InputTokens != 10 || got.OutputTokens != 20 || got.CacheReadTokens != 5 {
		t.Errorf("前三位被新两位污染了：%+v", got)
	}
}

// TestReportForwardsRetryAfter 上游明示的到期时刻必须穿过契约映射。
//
// 单列一条而不并入上一条：那条用的是 normal 上报，而到期时刻只在失败上报上
// 出现。这一层是纯字段搬运，断掉之后 runstate 与 codec 两侧的测试仍然全绿，
// 而整条链路已经失效——所以它必须有自己的断言。
func TestReportForwardsRetryAfter(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.applied = true
	at := time.Now().Add(30 * time.Minute).UTC()

	if _, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-1", RequestID: "req-1", ModelID: "kimi-1/k3",
		Outcome: string(runstate.OutcomeRetrying), RetryAfter: at,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := h.runStates.applyIn.RetryAfter; !got.Equal(at) {
		t.Fatalf("forwarded RetryAfter = %v, want %v", got, at)
	}
}

// TestReportForwardsZeroRetryAfter 上游没说时不能凭空造出一个时刻。
func TestReportForwardsZeroRetryAfter(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.applied = true

	if _, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-1", RequestID: "req-1", ModelID: "kimi-1/k3",
		Outcome: string(runstate.OutcomeAbnormal),
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := h.runStates.applyIn.RetryAfter; !got.IsZero() {
		t.Fatalf("forwarded RetryAfter = %v，没给就该是零值", got)
	}
}

func TestReportDuplicateReturnsNotApplied(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
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
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.applyErr = apperr.Field(apperr.InvalidRequest, "outcome", "unknown outcome")
	_, err := h.svc.Report(context.Background(), relayv1.ResultReport{ReportID: "r", ModelID: "m", Outcome: "?"})
	if e := asAppErr(t, err); e.Code != apperr.InvalidRequest {
		t.Fatalf("code = %s, want invalid_request", e.Code)
	}
}

func TestModelsSummarizesUserModels(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	resp, err := h.svc.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("models = %+v", resp.Models)
	}
	m := resp.Models[0]
	if m.Name != "sonnet" || m.Collection != "c1" || !m.Enabled {
		t.Fatalf("summary = %+v", m)
	}
}

// TestReportAcceptsTransportOutcome 新增的 transport outcome 必须能穿过受理面。
//
// 这一层把字符串直接转成 runstate.Outcome，没有白名单，所以真正的风险是
// 有人日后在这里加校验时漏掉新成员——那会让数据面的连接故障上报整条被拒，
// 连流水都留不下。
func TestReportAcceptsTransportOutcome(t *testing.T) {
	h := newHarness(t, testUserModel(), twoGroupSnapshot())
	h.runStates.applied = true

	if _, err := h.svc.Report(context.Background(), relayv1.ResultReport{
		ReportID: "rep-1", RequestID: "req-1", ModelID: "kimi-1/k3",
		Outcome: string(runstate.OutcomeTransport),
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := h.runStates.applyIn.Outcome; got != runstate.OutcomeTransport {
		t.Fatalf("转写后的 outcome = %q，要 %q", got, runstate.OutcomeTransport)
	}
}
