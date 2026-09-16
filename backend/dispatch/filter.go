package dispatch

import (
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/runstate"
)

// fallbackOrder 是未绑定策略时的默认候选顺序：Group 顺序在前，成员顺序在后。
// 快照本身已按 position 排好，这里只做展平。
func fallbackOrder(snap collection.Snapshot) []string {
	out := []string{}
	for _, g := range snap.Groups {
		for _, m := range g.Members {
			out = append(out, m.ModelID)
		}
	}
	return out
}

// filterCandidates 按固定顺序丢弃不可用候选：越界 → 已尝试 → 目录中已消失
// → 未启用 → 冷却中。每条丢弃都带原因进 Skipped。
//
// 策略已在输入里看到冷却与启用状态，这层是兜底而非替代：脚本写错也不会
// 把流量打到坏目标上。
func filterCandidates(
	candidates []string,
	snap collection.Snapshot,
	states map[string]runstate.State,
	tried []string,
) ([]string, []relayv1.Skip) {
	triedSet := make(map[string]bool, len(tried))
	for _, id := range tried {
		triedSet[id] = true
	}
	seen := make(map[string]bool, len(candidates))

	kept := []string{}
	skipped := []relayv1.Skip{}
	for _, id := range candidates {
		if seen[id] {
			continue
		}
		seen[id] = true

		member, inCollection := snap.Member(id)
		switch {
		case !inCollection:
			skipped = append(skipped, relayv1.Skip{ModelID: id, Reason: relayv1.SkipOutOfCollection})
		case triedSet[id]:
			skipped = append(skipped, relayv1.Skip{ModelID: id, Reason: relayv1.SkipAlreadyTried})
		case !member.Known:
			skipped = append(skipped, relayv1.Skip{ModelID: id, Reason: relayv1.SkipUnknownModel})
		case !member.Enabled:
			skipped = append(skipped, relayv1.Skip{ModelID: id, Reason: relayv1.SkipDisabled})
		case states[id].Cooling:
			skipped = append(skipped, relayv1.Skip{ModelID: id, Reason: relayv1.SkipCooling})
		default:
			kept = append(kept, id)
		}
	}
	return kept, skipped
}
