// Package strategy 持有挂在 Collection 上的策略组合与纯函数组合器。
//
// 组合器只做展开，不做解析：输出是带阶段的候选 model_id 序列，取凭据与
// 合并参数始终由调度层在组合器返回后完成。与脚本时代同一条边界——凭据
// 永不进入决策逻辑。
//
// 本包不依赖 collection：快照视图用本包自带的 Group/Member，由调度层
// 适配。这样 collection 可以反向持有 Strategy 类型而不成环。
package strategy

import (
	"fmt"

	"github.com/aceaura/model-surge-relay/backend/runstate"
)

// 候选阶段。agent 按序执行、不问语义：先 compact 段做压缩请求，
// 再 resume 段继续原任务；standard 为未触发超长时的唯一段。
const (
	PhaseStandard = "standard"
	PhaseCompact  = "compact"
	PhaseResume   = "resume"
)

// GroupExhausted 是整组跳过的唯一原因：组内成员全部不可用。
const GroupExhausted = "group_exhausted"

// Strategy 是挂在 Collection 上的策略组合。
type Strategy struct {
	// PriorityChain 是独立配置的有序组链；空 = 默认（快照组顺序）。
	PriorityChain []string `json:"priority_chain,omitempty"`
	Overflow      Overflow `json:"overflow"`
}

// Overflow 是上下文超长压缩托管配置。
type Overflow struct {
	Enabled         bool     `json:"enabled"`
	ThresholdTokens int      `json:"threshold_tokens,omitempty"`
	CompactGroups   []string `json:"compact_groups,omitempty"`
}

// Group 是组合器看到的组视图（由 collection.Snapshot 适配而来）。
type Group struct {
	Name    string
	Members []Member
}

// Member 是组合器看到的成员视图：只含可用性判定需要的三位。
type Member struct {
	ModelID string
	Known   bool
	Enabled bool
}

// Candidate 是阶段化候选。
type Candidate struct {
	ModelID string `json:"model_id"`
	Phase   string `json:"phase"`
}

// GroupSkip 记录一次整组跳过，供决策溯源解释"为什么没走这组"。
type GroupSkip struct {
	Group  string `json:"group"`
	Reason string `json:"reason"` // 恒为 GroupExhausted
	Detail string `json:"detail,omitempty"`
}

// Compose 是纯函数：无 IO、无沙箱、可并发。groups 需已按期望的默认顺序
// （组 position）排好——Strategy.PriorityChain 为空时直接按此序展开。
//
// 展开规则：未触发超长时按链（或默认组序）逐组展开，phase=standard；
// 触发超长（enabled 且阈值有效且 est_tokens 达到）时先展开压缩组
// （phase=compact）再展开链（phase=resume）。组内成员全部不可用则整组
// 跳过并记录 GroupSkip，继续展开后续组。去重按段内进行：同一成员允许
// 同时出现在 compact 与 resume 两段（压缩者与回落者可以是同一目标）。
func Compose(
	groups []Group,
	states map[string]runstate.State,
	triedIDs []string,
	estTokens int,
	st Strategy,
) ([]Candidate, []GroupSkip) {
	tried := make(map[string]bool, len(triedIDs))
	for _, id := range triedIDs {
		tried[id] = true
	}

	chain := st.PriorityChain
	if len(chain) == 0 {
		chain = make([]string, 0, len(groups))
		for _, g := range groups {
			chain = append(chain, g.Name)
		}
	}

	cands := []Candidate{}
	skips := []GroupSkip{}
	if st.Overflow.triggered(estTokens) {
		c, s := expand(groups, st.Overflow.CompactGroups, PhaseCompact, states, tried)
		cands = append(cands, c...)
		skips = append(skips, s...)
		c, s = expand(groups, chain, PhaseResume, states, tried)
		cands = append(cands, c...)
		skips = append(skips, s...)
		return cands, skips
	}

	c, s := expand(groups, chain, PhaseStandard, states, tried)
	return append(cands, c...), append(skips, s...)
}

// triggered 判定是否进入超长分支。阈值必须为正才有效：配置校验保证
// enabled 时阈值 > 0，这里再挡一手零值，组合器不信任任何上游已校验。
func (o Overflow) triggered(estTokens int) bool {
	return o.Enabled && o.ThresholdTokens > 0 && estTokens >= o.ThresholdTokens
}

// expand 按给定组名序逐组展开可用成员。段内按 model_id 去重。
func expand(
	groups []Group,
	chain []string,
	phase string,
	states map[string]runstate.State,
	tried map[string]bool,
) ([]Candidate, []GroupSkip) {
	byName := make(map[string]Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}

	cands := []Candidate{}
	skips := []GroupSkip{}
	seen := map[string]bool{}
	for _, name := range chain {
		g, ok := byName[name]
		if !ok {
			// 配置校验保证链内组存在；运行期组被并发删掉时降级为跳过而非 panic。
			skips = append(skips, GroupSkip{
				Group: name, Reason: GroupExhausted, Detail: "group not in snapshot"})
			continue
		}
		available := 0
		for _, m := range g.Members {
			if seen[m.ModelID] || unavailable(m, states[m.ModelID], tried) {
				continue
			}
			seen[m.ModelID] = true
			available++
			cands = append(cands, Candidate{ModelID: m.ModelID, Phase: phase})
		}
		if available == 0 {
			skips = append(skips, GroupSkip{
				Group:  name,
				Reason: GroupExhausted,
				Detail: fmt.Sprintf("all %d members unavailable", len(g.Members)),
			})
		}
	}
	return cands, skips
}

// unavailable 与调度层过滤语义一致：已试过、目录未知、未启用、冷却中
// （含限额冷却——被动限额判定，不新增配额账本）。
func unavailable(m Member, s runstate.State, tried map[string]bool) bool {
	return tried[m.ModelID] || !m.Known || !m.Enabled || s.Cooling
}
