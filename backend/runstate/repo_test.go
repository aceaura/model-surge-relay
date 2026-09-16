package runstate

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE target_runtime, result_reports RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s.Pool()
}

func newRepo(t *testing.T) *Repo { return NewRepo(testPool(t)) }

var defaults = Thresholds{FailureThreshold: 3, CooldownDuration: time.Minute}

var reportSeq int

func report(modelID string, outcome Outcome) ResultReport {
	reportSeq++
	return ResultReport{
		ReportID:  fmt.Sprintf("rep-%d-%d", time.Now().UnixNano(), reportSeq),
		RequestID: "req-1",
		ModelID:   modelID,
		Outcome:   outcome,
	}
}

func apply(t *testing.T, r *Repo, rep ResultReport, cfg Thresholds) bool {
	t.Helper()
	applied, err := r.ApplyReport(context.Background(), rep, cfg)
	if err != nil {
		t.Fatalf("apply %s: %v", rep.Outcome, err)
	}
	return applied
}

func get(t *testing.T, r *Repo, modelID string) State {
	t.Helper()
	s, err := r.Get(context.Background(), modelID)
	if err != nil {
		t.Fatalf("get %s: %v", modelID, err)
	}
	return s
}

func TestLoadReturnsZeroStateForUnseenTargets(t *testing.T) {
	r := newRepo(t)
	states, err := r.Load(context.Background(), []string{"a/x", "b/y"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %+v, want one entry per requested id", states)
	}
	for id, s := range states {
		if s.ModelID != id || s.Cooling || s.ConsecutiveFailures != 0 {
			t.Fatalf("state %s = %+v, want zero value", id, s)
		}
	}
}

func TestLoadEmptyInput(t *testing.T) {
	r := newRepo(t)
	states, err := r.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want empty", states)
	}
}

func TestNormalAccumulatesUsageAndClearsFailures(t *testing.T) {
	r := newRepo(t)
	apply(t, r, report("kimi-1/k3", OutcomeAbnormal), defaults)
	apply(t, r, report("kimi-1/k3", OutcomeAbnormal), defaults)

	rep := report("kimi-1/k3", OutcomeNormal)
	rep.Usage = Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5}
	apply(t, r, rep, defaults)

	s := get(t, r, "kimi-1/k3")
	if s.ConsecutiveFailures != 0 {
		t.Fatalf("failures = %d, want 0", s.ConsecutiveFailures)
	}
	if s.Usage != (Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, RequestCount: 1}) {
		t.Fatalf("usage = %+v", s.Usage)
	}
}

func TestNormalReleasesCooling(t *testing.T) {
	r := newRepo(t)
	cfg := Thresholds{FailureThreshold: 1, CooldownDuration: time.Hour}
	apply(t, r, report("m", OutcomeAbnormal), cfg)
	if s := get(t, r, "m"); !s.Cooling {
		t.Fatalf("expected cooling, got %+v", s)
	}
	apply(t, r, report("m", OutcomeNormal), cfg)
	if s := get(t, r, "m"); s.Cooling {
		t.Fatalf("a success must release cooling, got %+v", s)
	}
}

func TestOutcomeTransitionTable(t *testing.T) {
	cases := []struct {
		outcome      Outcome
		wantFailures int
		wantUsage    int64
	}{
		{OutcomeAbnormal, 1, 0},
		{OutcomeRetrying, 1, 0},
		{OutcomeInvalidModel, 1, 0},
		{OutcomeContextExceeded, 0, 0},
		{OutcomeNormal, 0, 1},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			r := newRepo(t)
			rep := report("m", tc.outcome)
			rep.Usage = Usage{InputTokens: 7}
			apply(t, r, rep, defaults)
			s := get(t, r, "m")
			if s.ConsecutiveFailures != tc.wantFailures {
				t.Fatalf("failures = %d, want %d", s.ConsecutiveFailures, tc.wantFailures)
			}
			if s.Usage.RequestCount != tc.wantUsage {
				t.Fatalf("request_count = %d, want %d", s.Usage.RequestCount, tc.wantUsage)
			}
			if s.Cooling {
				t.Fatalf("single report below threshold must not cool: %+v", s)
			}
		})
	}
}

func TestContextExceededLeavesStateUntouched(t *testing.T) {
	r := newRepo(t)
	apply(t, r, report("m", OutcomeAbnormal), defaults)
	before := get(t, r, "m")

	for i := 0; i < 5; i++ {
		apply(t, r, report("m", OutcomeContextExceeded), defaults)
	}
	after := get(t, r, "m")
	if after.ConsecutiveFailures != before.ConsecutiveFailures || after.Usage != before.Usage {
		t.Fatalf("context_exceeded mutated state: before %+v after %+v", before, after)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("context_exceeded touched updated_at: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestContextExceededCreatesNoRow(t *testing.T) {
	pool := testPool(t)
	r := NewRepo(pool)
	apply(t, r, report("fresh", OutcomeContextExceeded), defaults)
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM target_runtime WHERE model_id = 'fresh'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows = %d, want 0 (context_exceeded should not fabricate state)", count)
	}
}

func TestCoolingTriggersAtThreshold(t *testing.T) {
	r := newRepo(t)
	cfg := Thresholds{FailureThreshold: 3, CooldownDuration: time.Hour}
	for i := 1; i <= 2; i++ {
		apply(t, r, report("m", OutcomeAbnormal), cfg)
		if s := get(t, r, "m"); s.Cooling {
			t.Fatalf("cooling triggered early at failure %d", i)
		}
	}
	apply(t, r, report("m", OutcomeAbnormal), cfg)
	s := get(t, r, "m")
	if !s.Cooling {
		t.Fatalf("cooling should trigger at threshold, got %+v", s)
	}
	if !s.CoolingUntil.After(time.Now()) {
		t.Fatalf("cooling_until = %v, want future", s.CoolingUntil)
	}
}

func TestCoolingTriggersOnFirstFailureWhenThresholdIsOne(t *testing.T) {
	r := newRepo(t)
	apply(t, r, report("m", OutcomeAbnormal), Thresholds{FailureThreshold: 1, CooldownDuration: time.Hour})
	if s := get(t, r, "m"); !s.Cooling {
		t.Fatalf("expected cooling, got %+v", s)
	}
}

func TestZeroThresholdNeverCools(t *testing.T) {
	r := newRepo(t)
	cfg := Thresholds{FailureThreshold: 0, CooldownDuration: time.Hour}
	for i := 0; i < 10; i++ {
		apply(t, r, report("m", OutcomeAbnormal), cfg)
	}
	s := get(t, r, "m")
	if s.Cooling {
		t.Fatalf("threshold 0 means never cool, got %+v", s)
	}
	if s.ConsecutiveFailures != 10 {
		t.Fatalf("failures = %d, want 10", s.ConsecutiveFailures)
	}
}

func TestExpiredCoolingReadsAsNotCooling(t *testing.T) {
	r := newRepo(t)
	// 负时长会被归零，冷却时间落在 now()，读时已过期。
	apply(t, r, report("m", OutcomeAbnormal), Thresholds{FailureThreshold: 1, CooldownDuration: 0})
	time.Sleep(10 * time.Millisecond)
	s := get(t, r, "m")
	if s.Cooling {
		t.Fatalf("expired cooling must read as not cooling, got %+v", s)
	}
	if s.ConsecutiveFailures != 1 {
		t.Fatalf("failures = %d, want 1 (expiry must not clear the counter)", s.ConsecutiveFailures)
	}
}

func TestCoolingDoesNotShrinkOnLaterShorterCooldown(t *testing.T) {
	r := newRepo(t)
	apply(t, r, report("m", OutcomeAbnormal), Thresholds{FailureThreshold: 1, CooldownDuration: time.Hour})
	long := get(t, r, "m").CoolingUntil

	apply(t, r, report("m", OutcomeAbnormal), Thresholds{FailureThreshold: 1, CooldownDuration: time.Second})
	after := get(t, r, "m").CoolingUntil
	if after.Before(long) {
		t.Fatalf("cooling_until moved backwards: %v -> %v", long, after)
	}
}

func TestDuplicateReportIDIsIdempotent(t *testing.T) {
	r := newRepo(t)
	rep := report("m", OutcomeAbnormal)
	if !apply(t, r, rep, defaults) {
		t.Fatal("first apply should report applied=true")
	}
	before := get(t, r, "m")

	for i := 0; i < 3; i++ {
		if apply(t, r, rep, defaults) {
			t.Fatalf("replay %d should report applied=false", i)
		}
	}
	after := get(t, r, "m")
	if after.ConsecutiveFailures != before.ConsecutiveFailures {
		t.Fatalf("replayed report changed state: %d -> %d",
			before.ConsecutiveFailures, after.ConsecutiveFailures)
	}
}

func TestDuplicateNormalReportDoesNotDoubleCountUsage(t *testing.T) {
	r := newRepo(t)
	rep := report("m", OutcomeNormal)
	rep.Usage = Usage{InputTokens: 50}
	apply(t, r, rep, defaults)
	apply(t, r, rep, defaults)
	s := get(t, r, "m")
	if s.Usage.InputTokens != 50 || s.Usage.RequestCount != 1 {
		t.Fatalf("usage = %+v, want single application", s.Usage)
	}
}

func TestApplyReportValidatesInput(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		rep   ResultReport
		field string
	}{
		{"no report id", ResultReport{ModelID: "m", Outcome: OutcomeNormal}, "report_id"},
		{"no model id", ResultReport{ReportID: "r", Outcome: OutcomeNormal}, "model_id"},
		{"unknown outcome", ResultReport{ReportID: "r", ModelID: "m", Outcome: "exploded"}, "outcome"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ApplyReport(ctx, tc.rep, defaults)
			var e *apperr.Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v is not *apperr.Error", err)
			}
			if e.Code != apperr.InvalidRequest || e.Field != tc.field {
				t.Fatalf("error = %+v, want field %q", e, tc.field)
			}
		})
	}
}

func TestResetClearsCoolingKeepsUsage(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	rep := report("m", OutcomeNormal)
	rep.Usage = Usage{InputTokens: 42}
	apply(t, r, rep, defaults)
	apply(t, r, report("m", OutcomeAbnormal), Thresholds{FailureThreshold: 1, CooldownDuration: time.Hour})

	if err := r.Reset(ctx, "m"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	s := get(t, r, "m")
	if s.Cooling || s.ConsecutiveFailures != 0 {
		t.Fatalf("reset should clear health state, got %+v", s)
	}
	if s.Usage.InputTokens != 42 {
		t.Fatalf("reset must keep usage for auditing, got %+v", s.Usage)
	}
}

func TestResetUnknownTargetIsNotFound(t *testing.T) {
	r := newRepo(t)
	err := r.Reset(context.Background(), "ghost")
	var e *apperr.Error
	if !errors.As(err, &e) || e.Code != apperr.NotFound {
		t.Fatalf("error = %v, want not_found", err)
	}
}

func TestListReturnsAllRecordedTargets(t *testing.T) {
	r := newRepo(t)
	apply(t, r, report("b/y", OutcomeNormal), defaults)
	apply(t, r, report("a/x", OutcomeAbnormal), defaults)
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ModelID != "a/x" || list[1].ModelID != "b/y" {
		t.Fatalf("list = %+v, want model_id order", list)
	}
}

// TestPropertyArbitraryOutcomeSequences 断言三条收敛性质：
// 失败计数非负、冷却时间单调不倒退、纯 context_exceeded 序列零变更。
func TestPropertyArbitraryOutcomeSequences(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	outcomes := []Outcome{
		OutcomeNormal, OutcomeAbnormal, OutcomeRetrying,
		OutcomeInvalidModel, OutcomeContextExceeded,
	}
	rng := rand.New(rand.NewSource(20260916))
	cfg := Thresholds{FailureThreshold: 2, CooldownDuration: time.Hour}

	for seq := 0; seq < 20; seq++ {
		modelID := fmt.Sprintf("prop/%d", seq)
		var lastCoolingUntil time.Time
		for step := 0; step < 12; step++ {
			outcome := outcomes[rng.Intn(len(outcomes))]
			if _, err := r.ApplyReport(ctx, report(modelID, outcome), cfg); err != nil {
				t.Fatalf("seq %d step %d (%s): %v", seq, step, outcome, err)
			}
			s := get(t, r, modelID)
			if s.ConsecutiveFailures < 0 {
				t.Fatalf("seq %d: negative failure count %d", seq, s.ConsecutiveFailures)
			}
			// 成功会主动解除冷却，那是设计中的清零点，不算倒退。
			if outcome != OutcomeNormal && !s.CoolingUntil.IsZero() &&
				!lastCoolingUntil.IsZero() && s.CoolingUntil.Before(lastCoolingUntil) {
				t.Fatalf("seq %d: cooling_until moved backwards %v -> %v",
					seq, lastCoolingUntil, s.CoolingUntil)
			}
			if outcome == OutcomeNormal {
				lastCoolingUntil = time.Time{}
			} else if !s.CoolingUntil.IsZero() {
				lastCoolingUntil = s.CoolingUntil
			}
		}
	}
}

func TestPropertyPureContextExceededSequenceIsInert(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		if _, err := r.ApplyReport(ctx, report("inert", OutcomeContextExceeded), defaults); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	s := get(t, r, "inert")
	if s != (State{ModelID: "inert"}) {
		t.Fatalf("state = %+v, want pristine zero value", s)
	}
}
