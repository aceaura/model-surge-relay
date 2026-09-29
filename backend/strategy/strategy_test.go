package strategy

import (
	"reflect"
	"testing"

	"github.com/aceaura/model-surge-relay/backend/runstate"
)

func member(id string) Member {
	return Member{ModelID: id, Known: true, Enabled: true}
}

func groups() []Group {
	return []Group{
		{Name: "main", Members: []Member{member("a/1"), member("a/2")}},
		{Name: "backup", Members: []Member{member("b/1")}},
		{Name: "compact", Members: []Member{member("c/1")}},
	}
}

func TestCompose(t *testing.T) {
	cases := []struct {
		name      string
		strategy  Strategy
		states    map[string]runstate.State
		tried     []string
		estTokens int
		wantCands []Candidate
		wantSkips []GroupSkip
	}{
		{
			name:     "空链退化为快照组序",
			strategy: Strategy{},
			wantCands: []Candidate{
				{"a/1", PhaseStandard}, {"a/2", PhaseStandard}, {"b/1", PhaseStandard}, {"c/1", PhaseStandard},
			},
		},
		{
			name:      "独立链按配置顺序且只含勾选子集",
			strategy:  Strategy{PriorityChain: []string{"backup", "main"}},
			wantCands: []Candidate{{"b/1", PhaseStandard}, {"a/1", PhaseStandard}, {"a/2", PhaseStandard}},
		},
		{
			name:     "组内全灭跳整组继续下一组",
			strategy: Strategy{PriorityChain: []string{"main", "backup"}},
			states: map[string]runstate.State{
				"a/1": {Cooling: true}, "a/2": {Cooling: true},
			},
			wantCands: []Candidate{{"b/1", PhaseStandard}},
			wantSkips: []GroupSkip{{Group: "main", Reason: GroupExhausted, Detail: "all 2 members unavailable"}},
		},
		{
			name:      "组内部分不可用不跳组",
			strategy:  Strategy{PriorityChain: []string{"main", "backup"}},
			states:    map[string]runstate.State{"a/1": {Cooling: true}},
			wantCands: []Candidate{{"a/2", PhaseStandard}, {"b/1", PhaseStandard}},
		},
		{
			name:      "已试过/禁用/目录未知均判不可用",
			strategy:  Strategy{PriorityChain: []string{"main"}},
			tried:     []string{"a/1"},
			wantCands: []Candidate{{"a/2", PhaseStandard}},
		},
		{
			name:      "链内组运行期被删降级为跳过",
			strategy:  Strategy{PriorityChain: []string{"ghost", "backup"}},
			wantCands: []Candidate{{"b/1", PhaseStandard}},
			wantSkips: []GroupSkip{{Group: "ghost", Reason: GroupExhausted, Detail: "group not in snapshot"}},
		},
		{
			name: "达到阈值出压缩段加回落段",
			strategy: Strategy{
				PriorityChain: []string{"main", "backup"},
				Overflow:      Overflow{Enabled: true, ThresholdTokens: 1000, CompactGroups: []string{"compact"}},
			},
			estTokens: 1000,
			wantCands: []Candidate{
				{"c/1", PhaseCompact}, {"a/1", PhaseResume}, {"a/2", PhaseResume}, {"b/1", PhaseResume},
			},
		},
		{
			name: "低于阈值不出压缩段",
			strategy: Strategy{
				PriorityChain: []string{"main"},
				Overflow:      Overflow{Enabled: true, ThresholdTokens: 1000, CompactGroups: []string{"compact"}},
			},
			estTokens: 999,
			wantCands: []Candidate{
				{"a/1", PhaseStandard}, {"a/2", PhaseStandard},
			},
		},
		{
			name: "启用但阈值为零不触发",
			strategy: Strategy{
				Overflow: Overflow{Enabled: true, ThresholdTokens: 0, CompactGroups: []string{"compact"}},
			},
			estTokens: 99999,
			wantCands: []Candidate{
				{"a/1", PhaseStandard}, {"a/2", PhaseStandard}, {"b/1", PhaseStandard}, {"c/1", PhaseStandard},
			},
		},
		{
			name: "压缩段全灭仍出回落段并记跳过",
			strategy: Strategy{
				PriorityChain: []string{"main"},
				Overflow:      Overflow{Enabled: true, ThresholdTokens: 100, CompactGroups: []string{"compact"}},
			},
			estTokens: 500,
			states:    map[string]runstate.State{"c/1": {Cooling: true}},
			wantCands: []Candidate{{"a/1", PhaseResume}, {"a/2", PhaseResume}},
			wantSkips: []GroupSkip{{Group: "compact", Reason: GroupExhausted, Detail: "all 1 members unavailable"}},
		},
		{
			name: "同一成员允许跨段出现",
			strategy: Strategy{
				PriorityChain: []string{"compact"},
				Overflow:      Overflow{Enabled: true, ThresholdTokens: 100, CompactGroups: []string{"compact"}},
			},
			estTokens: 500,
			wantCands: []Candidate{{"c/1", PhaseCompact}, {"c/1", PhaseResume}},
		},
		{
			name:     "所有组耗尽输出空序列",
			strategy: Strategy{PriorityChain: []string{"main", "backup"}},
			states: map[string]runstate.State{
				"a/1": {Cooling: true}, "a/2": {Cooling: true}, "b/1": {Cooling: true},
			},
			wantCands: []Candidate{},
			wantSkips: []GroupSkip{
				{Group: "main", Reason: GroupExhausted, Detail: "all 2 members unavailable"},
				{Group: "backup", Reason: GroupExhausted, Detail: "all 1 members unavailable"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands, skips := Compose(groups(), tc.states, tc.tried, tc.estTokens, tc.strategy)
			if !reflect.DeepEqual(cands, tc.wantCands) {
				t.Errorf("candidates = %+v, want %+v", cands, tc.wantCands)
			}
			wantSkips := tc.wantSkips
			if wantSkips == nil {
				wantSkips = []GroupSkip{}
			}
			if !reflect.DeepEqual(skips, wantSkips) {
				t.Errorf("skips = %+v, want %+v", skips, wantSkips)
			}
		})
	}
}
