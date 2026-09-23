package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/policy/runtime"
)

func member(id string) collection.Member {
	return collection.Member{ModelID: id, Enabled: true, Known: true}
}

// fixture 是全部示例共用的输入：两组四成员，运行态各不相同。
func fixture() policy.Input {
	return policy.Input{
		Request: policy.RequestContext{UserModel: "sonnet", RequestID: "req-abc", EstTokens: 1000},
		Collection: collection.Snapshot{Name: "c1", Groups: []collection.GroupSnapshot{
			{Name: "primary", Type: "fast", Position: 0,
				Members: []collection.Member{member("kimi-1/k3"), member("kimi-2/k3")}},
			{Name: "backup", Type: "cheap", Position: 1,
				Members: []collection.Member{member("ark-1/ds"), member("ark-2/ds")}},
		}},
		Runtime: map[string]policy.State{
			"kimi-1/k3": {RequestCount: 30},
			"kimi-2/k3": {RequestCount: 10, ConsecutiveFailures: 1},
			"ark-1/ds":  {RequestCount: 5},
			"ark-2/ds":  {RequestCount: 0},
		},
	}
}

func run(t *testing.T, name string, in policy.Input) policy.Decision {
	t.Helper()
	src, err := Source(name)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	lang, err := Language(name)
	if err != nil {
		t.Fatalf("language: %v", err)
	}
	var rt policy.Runtime
	switch lang {
	case policy.LangLua:
		rt = runtime.NewLua()
	case policy.LangJavaScript:
		rt = runtime.NewJavaScript()
	default:
		t.Fatalf("no runtime registered for language %q", lang)
	}
	engine := policy.NewEngine(policy.NewRegistry(rt), 2*time.Second)
	d, err := engine.Execute(context.Background(),
		policy.Policy{Name: name, Language: lang, Source: src, Version: 1}, in)
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return d
}

func TestEveryExampleCompilesAndReturnsCandidates(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			d := run(t, name, fixture())
			if d.Note == "" {
				t.Fatal("note should explain the decision")
			}
			// Lua 预设在共用夹具上返回全部四名成员。JS 的 compact_overflow
			// 按 group.type 分流，共用夹具没有 kimi/compact 组，故返回空候选，
			// 其行为由下方专属测试用对应夹具断言。
			if lang, _ := Language(name); lang == policy.LangLua && len(d.Candidates) != 4 {
				t.Fatalf("candidates = %v, want all four members", d.Candidates)
			}
		})
	}
}

func TestPresetFollowsConfiguredOrder(t *testing.T) {
	d := run(t, Preset, fixture())
	want := "[kimi-1/k3 kimi-2/k3 ark-1/ds ark-2/ds]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestFailoverKeepsGroupBoundaryOrder(t *testing.T) {
	d := run(t, Failover, fixture())
	want := "[kimi-1/k3 kimi-2/k3 ark-1/ds ark-2/ds]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestFailoverSkipsVanishedReferences(t *testing.T) {
	in := fixture()
	gone := in.Collection.Groups[0].Members[0]
	gone.Known = false
	in.Collection.Groups[0].Members[0] = gone

	d := run(t, Failover, in)
	for _, id := range d.Candidates {
		if id == "kimi-1/k3" {
			t.Fatalf("candidates = %v, should skip the vanished reference", d.Candidates)
		}
	}
	if len(d.Candidates) != 3 {
		t.Fatalf("candidates = %v, want three", d.Candidates)
	}
}

func TestStickyPrefersHealthyPreviouslyUsedTargets(t *testing.T) {
	d := run(t, Sticky, fixture())
	// kimi-1/k3 与 ark-1/ds 有成功记录且无连续失败，应排在前；
	// kimi-2/k3 有失败、ark-2/ds 从未用过，退到后面。
	want := "[kimi-1/k3 ark-1/ds kimi-2/k3 ark-2/ds]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestStickyFallsBackToConfiguredOrderOnColdStart(t *testing.T) {
	in := fixture()
	in.Runtime = map[string]policy.State{}
	d := run(t, Sticky, in)
	want := "[kimi-1/k3 kimi-2/k3 ark-1/ds ark-2/ds]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestLeastUsedSortsByRequestCount(t *testing.T) {
	d := run(t, LeastUsed, fixture())
	want := "[ark-2/ds ark-1/ds kimi-2/k3 kimi-1/k3]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestLeastUsedTiesFallBackToConfiguredOrder(t *testing.T) {
	in := fixture()
	for id := range in.Runtime {
		in.Runtime[id] = policy.State{RequestCount: 7}
	}
	d := run(t, LeastUsed, in)
	want := "[kimi-1/k3 kimi-2/k3 ark-1/ds ark-2/ds]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s (ties must be stable)", d.Candidates, want)
	}
}

func TestRoundRobinRotatesByRequestID(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range []string{"req-1", "req-2", "req-3", "req-4", "req-5", "req-6", "req-7", "req-8"} {
		in := fixture()
		in.Request.RequestID = id
		d := run(t, RoundRobin, in)
		if len(d.Candidates) != 4 {
			t.Fatalf("%s: candidates = %v", id, d.Candidates)
		}
		seen[d.Candidates[0]] = true
	}
	if len(seen) < 2 {
		t.Fatalf("first candidate never rotated across request ids: %v", seen)
	}
}

func TestRoundRobinIsDeterministicForSameRequestID(t *testing.T) {
	first := run(t, RoundRobin, fixture())
	second := run(t, RoundRobin, fixture())
	if fmt.Sprint(first.Candidates) != fmt.Sprint(second.Candidates) {
		t.Fatalf("same request id produced %v then %v", first.Candidates, second.Candidates)
	}
}

func TestRoundRobinCoversEveryCandidateExactlyOnce(t *testing.T) {
	d := run(t, RoundRobin, fixture())
	seen := map[string]int{}
	for _, id := range d.Candidates {
		seen[id]++
	}
	if len(seen) != 4 {
		t.Fatalf("candidates = %v, want each member exactly once", d.Candidates)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s appears %d times", id, n)
		}
	}
}

func TestExamplesHandleEmptyCollection(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			in := fixture()
			in.Collection.Groups = nil
			d := run(t, name, in)
			if len(d.Candidates) != 0 {
				t.Fatalf("candidates = %v, want empty", d.Candidates)
			}
		})
	}
}

// jsFixture 是 compact_overflow 的专属夹具：kimi 主池（窗口 256000）
// 与 compact 压缩池（窗口 1000000），运行态留空（粘性与切换由配置顺序+冷却决定）。
func jsFixture() policy.Input {
	win := func(id string, w int) collection.Member {
		return collection.Member{ModelID: id, Enabled: true, Known: true, ContextWindow: w}
	}
	return policy.Input{
		Request: policy.RequestContext{UserModel: "sonnet", RequestID: "req-abc", EstTokens: 1000},
		Collection: collection.Snapshot{Name: "c1", Groups: []collection.GroupSnapshot{
			{Name: "kimi", Type: "kimi", Position: 0,
				Members: []collection.Member{win("kimi-1/k3", 256000), win("kimi-2/k3", 256000)}},
			{Name: "compact", Type: "compact", Position: 1,
				Members: []collection.Member{win("ds-1/v4", 1000000)}},
		}},
		Runtime: map[string]policy.State{},
	}
}

func TestCompactOverflowUsesPrimaryForNormalRequests(t *testing.T) {
	d := run(t, CompactOverflow, jsFixture())
	want := "[kimi-1/k3 kimi-2/k3]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestCompactOverflowRoutesOverlongToCompact(t *testing.T) {
	in := jsFixture()
	in.Request.EstTokens = 300000 // 超过 kimi 窗口 256000
	d := run(t, CompactOverflow, in)
	want := "[ds-1/v4]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestCompactOverflowTreatsZeroEstAsNormal(t *testing.T) {
	in := jsFixture()
	in.Request.EstTokens = 0 // 数据面没给估算，不得误判超长
	d := run(t, CompactOverflow, in)
	want := "[kimi-1/k3 kimi-2/k3]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestCompactOverflowHonorsConfigThreshold(t *testing.T) {
	in := jsFixture()
	// 主池组显式阈值 500：est 1000 虽远小于 kimi 窗口，也应触发压缩池。
	in.Collection.Groups[0].Config = json.RawMessage(`{"compact_above_tokens": 500}`)
	in.Request.EstTokens = 1000
	d := run(t, CompactOverflow, in)
	want := "[ds-1/v4]"
	if fmt.Sprint(d.Candidates) != want {
		t.Fatalf("candidates = %v, want %s", d.Candidates, want)
	}
}

func TestSourceRejectsUnknownName(t *testing.T) {
	if _, err := Source("nonexistent"); err == nil {
		t.Fatal("expected an error for an unknown example")
	}
}
