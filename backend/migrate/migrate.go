// Package migrate 承担脚本策略到策略组合的一次性迁移。
//
// 迁移只认示例脚本的名字：任意脚本的源码不可机器翻译，能映射的
// 从来就是那几个语义已知的模板。识别不了的集合写默认链（组顺序），
// 并把策略名留在报告里交由人工跟进——迁移不阻断，报告是唯一的账本。
//
// 入口是管理面 POST /admin/migrate-policies，可重复执行：结果幂等，
// 报告每次重新生成。迁移确认无误后，policies 表与 user_models.policy
// 遗产列由人工 DROP（见 schema.sql 注释），本包随即可以整体删除。
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/strategy"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Report 是迁移账本的三类清单。
type Report struct {
	Mapped    []Mapped   `json:"mapped"`
	Conflicts []Conflict `json:"conflicts"`
	Unmapped  []Unmapped `json:"unmapped"`
}

// Mapped 记录一次成功映射。Note 承载语义有损的说明（如运行态加权不再支持）。
type Mapped struct {
	Collection string `json:"collection"`
	Policy     string `json:"policy"`
	Note       string `json:"note,omitempty"`
}

// Conflict 记录同一集合被多个策略引用（R4.2）：取多数者胜出，竞争名单留档。
type Conflict struct {
	Collection string   `json:"collection"`
	Policies   []string `json:"policies"`
	Chosen     string   `json:"chosen"`
}

// Unmapped 记录识别不了的脚本：集合已写默认链，策略名留待人工跟进。
type Unmapped struct {
	Collection string `json:"collection"`
	Policy     string `json:"policy"`
}

// Collections 是迁移对集合存储的最小依赖。
type Collections interface {
	Groups(ctx context.Context, collectionName string) ([]collection.Group, error)
	Snapshot(ctx context.Context, name string) (collection.Snapshot, error)
	SetStrategy(ctx context.Context, name string, st strategy.Strategy) error
}

type Migrator struct {
	pool        *pgxpool.Pool
	collections Collections
}

func New(pool *pgxpool.Pool, collections Collections) *Migrator {
	return &Migrator{pool: pool, collections: collections}
}

// 示例脚本名，与已删除的 policy/examples 包一一对应。映射按名不按源码：
// 名字是运维语义的唯一稳定锚点。
const (
	scriptPreset         = "preset"
	scriptSticky         = "sticky"
	scriptFailover       = "failover"
	scriptRoundRobin     = "round_robin"
	scriptLeastUsed      = "least_used"
	scriptCompactOverflow = "compact_overflow"
)

// compact_overflow 的分组约定：type 为 "compact" 的组是压缩池，其余是主池。
const compactGroupType = "compact"

// Migrate 执行一次全量迁移。逐集合处理：单个集合失败不拖垮整批，
// 失败以 error 形式整体返回前已写入的集合保持写入状态（幂等，重跑收敛）。
func (m *Migrator) Migrate(ctx context.Context) (Report, error) {
	refs, err := m.policyRefs(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Mapped: []Mapped{}, Conflicts: []Conflict{}, Unmapped: []Unmapped{}}
	// 集合名排序处理，报告顺序稳定。
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, collectionName := range names {
		chosen, conflicted := majority(refs[collectionName])
		if conflicted {
			report.Conflicts = append(report.Conflicts, Conflict{
				Collection: collectionName,
				Policies:   sortedKeys(refs[collectionName]),
				Chosen:     chosen,
			})
		}
		st, note, mapped := m.mapPolicy(ctx, collectionName, chosen)
		if err := m.collections.SetStrategy(ctx, collectionName, st); err != nil {
			return report, fmt.Errorf("migrate collection %q: %w", collectionName, err)
		}
		if mapped {
			report.Mapped = append(report.Mapped, Mapped{Collection: collectionName, Policy: chosen, Note: note})
		} else {
			report.Unmapped = append(report.Unmapped, Unmapped{Collection: collectionName, Policy: chosen})
		}
	}
	return report, nil
}

// policyRefs 读遗产列 user_models.policy，按集合归集策略名计数。
// 只关心引用关系，不读 policies 表本身：映射按名进行，表内容用不上。
func (m *Migrator) policyRefs(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := m.pool.Query(ctx,
		`SELECT collection, policy FROM user_models WHERE policy IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var collectionName, policyName string
		if err := rows.Scan(&collectionName, &policyName); err != nil {
			return nil, err
		}
		if out[collectionName] == nil {
			out[collectionName] = map[string]int{}
		}
		out[collectionName][policyName]++
	}
	return out, rows.Err()
}

// majority 取计数最多者；并列时取名字序最小者保证确定性。
// 返回值第二项表示是否存在竞争（多于一个不同策略名）。
func majority(counts map[string]int) (string, bool) {
	names := sortedKeys(counts)
	best := names[0]
	for _, n := range names[1:] {
		if counts[n] > counts[best] {
			best = n
		}
	}
	return best, len(names) > 1
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mapPolicy 把脚本名翻译成策略组合。返回第三项为假表示脚本未识别，
// 此时返回默认链——行为与空组合一致，集合不因此失去调度能力。
func (m *Migrator) mapPolicy(ctx context.Context, collectionName, policyName string) (strategy.Strategy, string, bool) {
	groups, err := m.collections.Groups(ctx, collectionName)
	if err != nil {
		// 组读取失败按未识别处理：默认链兜底，错误留待 SetStrategy 校验暴露。
		return strategy.Strategy{}, "", false
	}
	switch policyName {
	case scriptPreset, scriptSticky, scriptFailover:
		return strategy.Strategy{PriorityChain: groupNames(groups)}, "", true
	case scriptRoundRobin, scriptLeastUsed:
		return strategy.Strategy{PriorityChain: groupNames(groups)},
			"运行态加权（round_robin/least_used）不再支持，已退化为组顺序链", true
	case scriptCompactOverflow:
		return m.mapCompactOverflow(ctx, collectionName, groups)
	default:
		return strategy.Strategy{PriorityChain: groupNames(groups)}, "", false
	}
}

// mapCompactOverflow 复刻脚本的三分结构：主池链 + 压缩组 + 阈值。
// 阈值取主池组 config 的 compact_above_tokens，其次主池成员的最大窗口；
// 都取不到时保留组配置但关掉开关，由人工补阈值后开启。
func (m *Migrator) mapCompactOverflow(ctx context.Context, collectionName string, groups []collection.Group) (strategy.Strategy, string, bool) {
	var chain, compact []string
	for _, g := range groups {
		if g.Type == compactGroupType {
			compact = append(compact, g.Name)
		} else {
			chain = append(chain, g.Name)
		}
	}
	st := strategy.Strategy{PriorityChain: chain}
	if len(compact) == 0 {
		return st, "脚本引用压缩池但集合里没有 type=compact 的组，已退化为纯优先级链", true
	}
	threshold := configThreshold(groups)
	if threshold == 0 {
		threshold = m.maxPrimaryWindow(ctx, collectionName)
	}
	st.Overflow = strategy.Overflow{
		Enabled:         threshold > 0,
		ThresholdTokens: threshold,
		CompactGroups:   compact,
	}
	if threshold == 0 {
		return st, "阈值无法从组配置或成员窗口推断，压缩托管已保留组配置但保持关闭", true
	}
	return st, "", true
}

// configThreshold 读主池组 config 的 compact_above_tokens，与脚本里的
// 覆盖约定同名。第一个声明了的组生效，与脚本 primaryThreshold 的提前返回一致。
func configThreshold(groups []collection.Group) int {
	for _, g := range groups {
		if g.Type == compactGroupType || len(g.Config) == 0 {
			continue
		}
		var cfg struct {
			CompactAboveTokens int `json:"compact_above_tokens"`
		}
		if err := json.Unmarshal(g.Config, &cfg); err == nil && cfg.CompactAboveTokens > 0 {
			return cfg.CompactAboveTokens
		}
	}
	return 0
}

// maxPrimaryWindow 取主池成员的最大 context_window，与脚本"任一主池成员
// 装得下就不算超长"的判定对齐。快照不可达时返回 0，由调用方降级。
func (m *Migrator) maxPrimaryWindow(ctx context.Context, collectionName string) int {
	snap, err := m.collections.Snapshot(ctx, collectionName)
	if err != nil {
		return 0
	}
	maxWindow := 0
	for _, g := range snap.Groups {
		if g.Type == compactGroupType {
			continue
		}
		for _, member := range g.Members {
			if member.ContextWindow > maxWindow {
				maxWindow = member.ContextWindow
			}
		}
	}
	return maxWindow
}

func groupNames(groups []collection.Group) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	return out
}
