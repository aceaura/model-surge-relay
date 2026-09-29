package collection

import (
	"testing"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/strategy"
)

func twoGroups() []Group {
	return []Group{
		{Collection: "c", Name: "main", Type: "primary"},
		{Collection: "c", Name: "backup", Type: "backup"},
	}
}

func TestValidateStrategy(t *testing.T) {
	cases := []struct {
		name      string
		strategy  strategy.Strategy
		wantField string // 空 = 期望通过
	}{
		{name: "零值合法（默认链）", strategy: strategy.Strategy{}},
		{
			name:     "链内组存在即可",
			strategy: strategy.Strategy{PriorityChain: []string{"backup", "main"}},
		},
		{
			name:      "链引用未知组",
			strategy:  strategy.Strategy{PriorityChain: []string{"main", "ghost"}},
			wantField: "priority_chain",
		},
		{
			name:      "链内重复组",
			strategy:  strategy.Strategy{PriorityChain: []string{"main", "main"}},
			wantField: "priority_chain",
		},
		{
			name: "压缩组引用未知组",
			strategy: strategy.Strategy{Overflow: strategy.Overflow{
				Enabled: true, ThresholdTokens: 100, CompactGroups: []string{"ghost"}}},
			wantField: "compact_groups",
		},
		{
			name: "启用压缩托管但阈值非正",
			strategy: strategy.Strategy{Overflow: strategy.Overflow{
				Enabled: true, ThresholdTokens: 0, CompactGroups: []string{"main"}}},
			wantField: "threshold_tokens",
		},
		{
			name: "启用压缩托管但压缩组为空",
			strategy: strategy.Strategy{Overflow: strategy.Overflow{
				Enabled: true, ThresholdTokens: 100}},
			wantField: "compact_groups",
		},
		{
			name: "压缩托管完整配置",
			strategy: strategy.Strategy{
				PriorityChain: []string{"main"},
				Overflow: strategy.Overflow{
					Enabled: true, ThresholdTokens: 100, CompactGroups: []string{"backup"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStrategy(tc.strategy, twoGroups())
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("validate: %v, want nil", err)
				}
				return
			}
			e := apperr.From(err)
			if e == nil || e.Code != apperr.InvalidRequest {
				t.Fatalf("err = %v, want invalid_request", err)
			}
			if e.Field != tc.wantField {
				t.Errorf("field = %q, want %q", e.Field, tc.wantField)
			}
		})
	}
}

func TestStrategyReferences(t *testing.T) {
	st := strategy.Strategy{
		PriorityChain: []string{"main"},
		Overflow: strategy.Overflow{
			Enabled: true, ThresholdTokens: 100, CompactGroups: []string{"backup"}},
	}
	if refs := strategyReferences(st, "main"); len(refs) != 1 || refs[0] != "priority_chain" {
		t.Errorf("main refs = %v", refs)
	}
	if refs := strategyReferences(st, "backup"); len(refs) != 1 || refs[0] != "compact_groups" {
		t.Errorf("backup refs = %v", refs)
	}
	if refs := strategyReferences(st, "other"); len(refs) != 0 {
		t.Errorf("other refs = %v, want none", refs)
	}
}
