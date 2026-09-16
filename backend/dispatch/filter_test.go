package dispatch

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/runstate"
)

// member 造一个已知且启用的成员，便于用最少噪声表达测试意图。
func member(id string, pos int) collection.Member {
	return collection.Member{ModelID: id, Position: pos, Enabled: true, Known: true}
}

func snapshot(groups ...collection.GroupSnapshot) collection.Snapshot {
	return collection.Snapshot{Name: "c1", Groups: groups}
}

func group(name, typ string, pos int, members ...collection.Member) collection.GroupSnapshot {
	return collection.GroupSnapshot{Name: name, Type: typ, Position: pos, Members: members}
}

func TestFallbackOrderFollowsGroupThenMemberOrder(t *testing.T) {
	snap := snapshot(
		group("primary", "fast", 0, member("a/1", 0), member("a/2", 1)),
		group("backup", "cheap", 1, member("b/1", 0)),
	)
	got := fallbackOrder(snap)
	want := []string{"a/1", "a/2", "b/1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestFallbackOrderOnEmptyCollection(t *testing.T) {
	if got := fallbackOrder(snapshot()); len(got) != 0 {
		t.Fatalf("order = %v, want empty", got)
	}
}

func TestFilterDropsOutOfCollectionReference(t *testing.T) {
	snap := snapshot(group("g", "t", 0, member("a/1", 0)))
	kept, skipped := filterCandidates([]string{"a/1", "elsewhere/9"}, snap, nil, nil)
	if fmt.Sprint(kept) != "[a/1]" {
		t.Fatalf("kept = %v", kept)
	}
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipOutOfCollection {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestFilterDropsAlreadyTried(t *testing.T) {
	snap := snapshot(group("g", "t", 0, member("a/1", 0), member("a/2", 1)))
	kept, skipped := filterCandidates([]string{"a/1", "a/2"}, snap, nil, []string{"a/1"})
	if fmt.Sprint(kept) != "[a/2]" {
		t.Fatalf("kept = %v", kept)
	}
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipAlreadyTried {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestFilterDropsDisabled(t *testing.T) {
	disabled := member("a/2", 1)
	disabled.Enabled = false
	snap := snapshot(group("g", "t", 0, member("a/1", 0), disabled))
	kept, skipped := filterCandidates([]string{"a/2", "a/1"}, snap, nil, nil)
	if fmt.Sprint(kept) != "[a/1]" {
		t.Fatalf("kept = %v", kept)
	}
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipDisabled {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestFilterDropsVanishedCatalogReference(t *testing.T) {
	gone := collection.Member{ModelID: "a/gone", Enabled: true, Known: false}
	snap := snapshot(group("g", "t", 0, gone, member("a/1", 1)))
	kept, skipped := filterCandidates([]string{"a/gone", "a/1"}, snap, nil, nil)
	if fmt.Sprint(kept) != "[a/1]" {
		t.Fatalf("kept = %v", kept)
	}
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipUnknownModel {
		t.Fatalf("skipped = %+v", skipped)
	}
}

// TestFilterRejectsCoolingTargetEvenWhenPolicyInsists 是这层存在的理由：
// 脚本在输入里看到了冷却状态却仍然返回该目标，服务侧照样拒绝。
func TestFilterRejectsCoolingTargetEvenWhenPolicyInsists(t *testing.T) {
	snap := snapshot(group("g", "t", 0, member("a/1", 0), member("a/2", 1)))
	states := map[string]runstate.State{
		"a/1": {ModelID: "a/1", Cooling: true, CoolingUntil: time.Now().Add(time.Hour)},
	}
	kept, skipped := filterCandidates([]string{"a/1", "a/2"}, snap, states, nil)
	if fmt.Sprint(kept) != "[a/2]" {
		t.Fatalf("kept = %v, cooling target must be rejected", kept)
	}
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipCooling {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestFilterAppliesReasonsInFixedPrecedence(t *testing.T) {
	// 同时越界与已尝试：越界优先，因为越界的引用连"尝试过"都无从谈起。
	snap := snapshot(group("g", "t", 0, member("a/1", 0)))
	_, skipped := filterCandidates([]string{"outside/1"}, snap, nil, []string{"outside/1"})
	if len(skipped) != 1 || skipped[0].Reason != relayv1.SkipOutOfCollection {
		t.Fatalf("skipped = %+v, want out_of_collection to win", skipped)
	}
}

func TestFilterDeduplicatesRepeatedCandidates(t *testing.T) {
	snap := snapshot(group("g", "t", 0, member("a/1", 0)))
	kept, skipped := filterCandidates([]string{"a/1", "a/1", "a/1"}, snap, nil, nil)
	if fmt.Sprint(kept) != "[a/1]" {
		t.Fatalf("kept = %v", kept)
	}
	if len(skipped) != 0 {
		t.Fatalf("duplicates should be collapsed silently, got %+v", skipped)
	}
}

func TestFilterPreservesCandidateOrder(t *testing.T) {
	snap := snapshot(group("g", "t", 0, member("a/1", 0), member("a/2", 1), member("a/3", 2)))
	kept, _ := filterCandidates([]string{"a/3", "a/1", "a/2"}, snap, nil, nil)
	if fmt.Sprint(kept) != "[a/3 a/1 a/2]" {
		t.Fatalf("kept = %v, filter must not reorder", kept)
	}
}

// TestPropertyFilterOutputIsAlwaysEligibleSubset 断言过滤结果恒为
// "属于该 Collection ∧ 已知 ∧ 启用 ∧ 未冷却 ∧ 未在 tried_ids"的子集。
func TestPropertyFilterOutputIsAlwaysEligibleSubset(t *testing.T) {
	rng := rand.New(rand.NewSource(20260916))
	for iter := 0; iter < 500; iter++ {
		var members []collection.Member
		universe := []string{}
		for i := 0; i < rng.Intn(6); i++ {
			id := fmt.Sprintf("acct/%d", i)
			universe = append(universe, id)
			members = append(members, collection.Member{
				ModelID: id, Position: i,
				Enabled: rng.Intn(2) == 0,
				Known:   rng.Intn(4) != 0,
			})
		}
		snap := snapshot(group("g", "t", 0, members...))

		states := map[string]runstate.State{}
		for _, id := range universe {
			if rng.Intn(3) == 0 {
				states[id] = runstate.State{ModelID: id, Cooling: true,
					CoolingUntil: time.Now().Add(time.Hour)}
			}
		}

		tried := []string{}
		triedSet := map[string]bool{}
		for _, id := range universe {
			if rng.Intn(4) == 0 {
				tried = append(tried, id)
				triedSet[id] = true
			}
		}

		// 候选里混入越界引用，确保过滤真的在把关。
		candidates := append([]string{}, universe...)
		for i := 0; i < rng.Intn(3); i++ {
			candidates = append(candidates, fmt.Sprintf("outside/%d", i))
		}
		rng.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})

		kept, skipped := filterCandidates(candidates, snap, states, tried)
		for _, id := range kept {
			m, ok := snap.Member(id)
			switch {
			case !ok:
				t.Fatalf("iter %d: kept out-of-collection %q", iter, id)
			case !m.Known:
				t.Fatalf("iter %d: kept vanished reference %q", iter, id)
			case !m.Enabled:
				t.Fatalf("iter %d: kept disabled %q", iter, id)
			case states[id].Cooling:
				t.Fatalf("iter %d: kept cooling %q", iter, id)
			case triedSet[id]:
				t.Fatalf("iter %d: kept already-tried %q", iter, id)
			}
		}
		// 每个去重后的候选要么留下要么带原因被丢弃，不允许静默消失。
		distinct := map[string]bool{}
		for _, id := range candidates {
			distinct[id] = true
		}
		if len(kept)+len(skipped) != len(distinct) {
			t.Fatalf("iter %d: %d kept + %d skipped != %d distinct candidates",
				iter, len(kept), len(skipped), len(distinct))
		}
	}
}
