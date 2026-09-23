// compact_overflow：主池粘性使用，请求超长时改走大窗口的压缩池。
//
// 分组约定（在管理面把 group.type 填成这两个值，relay 本身不解释其含义）：
//   "kimi"    主池，按配置顺序粘性使用
//   "compact" 压缩池（如 deepseek-4.1-flash，窗口远大于主池）
//
// 路由规则：
//   est_tokens 超过主池可容纳上限 → 只返回 compact 组，交给大窗口目标做压缩
//   否则                          → 只返回主池，按配置顺序粘性排列
//   两组皆空 / 全被过滤           → 返回空候选，调度层报 target_unavailable
//
// 两点必须知道：
// 1. input 里没有"这是压缩请求"的标记——压缩在架构上就是普通 request，
//    唯一的超长信号是 est_tokens 对比 context_window，故判定只能基于此。
// 2. "连续失败 N 次后切下一个"不在此实现：由服务端冷却阈值
//    (MSR_COOLDOWN_THRESHOLD，默认 3) 触发冷却，dispatch 过滤层会在本策略
//    返回后无条件丢弃冷却中的目标。脚本保持配置顺序，粘性与自动切回即成立。

const PRIMARY = "kimi";
const COMPACT = "compact";

const groups = input.collection.groups || [];
const req = input.request || {};

function membersOf(type) {
  const out = [];
  for (const g of groups) {
    if (g.type !== type) continue;
    for (const m of (g.members || [])) out.push(m);
  }
  return out;
}

const ids = (list) => list.map((m) => m.model_id);

// 主池可容纳上限：任一主池成员装得下就不算超长，故取最大 context_window。
// 上游没声明 context_window 时，在主池组 config 里填 compact_above_tokens 覆盖。
function primaryThreshold() {
  let maxWindow = 0;
  for (const g of groups) {
    if (g.type !== PRIMARY) continue;
    const cfg = g.config || {};
    if (typeof cfg.compact_above_tokens === "number" && cfg.compact_above_tokens > 0) {
      return cfg.compact_above_tokens;
    }
    for (const m of (g.members || [])) {
      const w = m.context_window || 0;
      if (w > maxWindow) maxWindow = w;
    }
  }
  return maxWindow;
}

const est = req.est_tokens || 0;
const threshold = primaryThreshold();
// est 为 0 表示数据面没给估算，按常规处理，绝不误判成超长把流量推给压缩池。
const overlong = est > 0 && threshold > 0 && est > threshold;

if (overlong) {
  return {
    candidates: ids(membersOf(COMPACT)),
    note: "overlong (est " + est + " > " + threshold + ") -> compact",
  };
}

return {
  candidates: ids(membersOf(PRIMARY)),
  note: "sticky primary",
};
