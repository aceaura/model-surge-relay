package collection

import (
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/strategy"
)

// validateStrategy 校验策略组合对本集合的引用完整性与字段合法性：
// 链内组存在且无重复、压缩组存在、启用压缩托管时阈值为正且压缩组非空。
func validateStrategy(st strategy.Strategy, groups []Group) error {
	names := make(map[string]bool, len(groups))
	for _, g := range groups {
		names[g.Name] = true
	}

	seen := make(map[string]bool, len(st.PriorityChain))
	unknown, dup := []string{}, []string{}
	for _, n := range st.PriorityChain {
		if !names[n] {
			unknown = append(unknown, n)
			continue
		}
		if seen[n] {
			dup = append(dup, n)
		}
		seen[n] = true
	}
	if len(unknown) > 0 {
		return apperr.Field(apperr.InvalidRequest, "priority_chain",
			fmt.Sprintf("unknown groups: %s", strings.Join(unknown, ", ")))
	}
	if len(dup) > 0 {
		return apperr.Field(apperr.InvalidRequest, "priority_chain",
			fmt.Sprintf("duplicate groups: %s", strings.Join(dup, ", ")))
	}

	unknown = unknown[:0]
	for _, n := range st.Overflow.CompactGroups {
		if !names[n] {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return apperr.Field(apperr.InvalidRequest, "compact_groups",
			fmt.Sprintf("unknown groups: %s", strings.Join(unknown, ", ")))
	}
	if st.Overflow.Enabled {
		if st.Overflow.ThresholdTokens <= 0 {
			return apperr.Field(apperr.InvalidRequest, "threshold_tokens",
				"threshold_tokens must be positive when overflow is enabled")
		}
		if len(st.Overflow.CompactGroups) == 0 {
			return apperr.Field(apperr.InvalidRequest, "compact_groups",
				"compact_groups is required when overflow is enabled")
		}
	}
	return nil
}

// strategyReferences 报告某组被策略组合引用的位置（priority_chain /
// compact_groups），用于组删除时的 409 明细。
func strategyReferences(st strategy.Strategy, group string) []string {
	refs := []string{}
	for _, n := range st.PriorityChain {
		if n == group {
			refs = append(refs, "priority_chain")
			break
		}
	}
	for _, n := range st.Overflow.CompactGroups {
		if n == group {
			refs = append(refs, "compact_groups")
			break
		}
	}
	return refs
}
