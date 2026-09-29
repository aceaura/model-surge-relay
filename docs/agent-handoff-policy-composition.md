# 策略组合化：agent 侧交接说明

面向消费 relay 调度契约的数据面/agent 家族（model-surge-stream 等）。本次变更把脚本策略整体替换为集合上的策略组合，`relayv1` 契约有**破坏性变更**，agent 必须同步改造后才能对接新版 relay。

## 契约变更点

### 1. 候选阶段化（核心）

`decision.candidates` 由 `array<string>` 变为 `array<PhasedCandidate>`：

```json
{"model_id": "kimi-1/k3", "phase": "standard"}
```

`phase` 封闭三值，agent **按序执行、不问语义**：

| phase | agent 动作 |
| --- | --- |
| `standard` | 正常发业务请求。未触发超长时整段都是这个值，行为与旧版一致 |
| `compact` | 对该目标发**压缩请求**（压缩上下文），拿到压缩结果后进入 resume 段 |
| `resume` | 携带压缩后的上下文**继续原任务** |

典型超长序列：`compact` 段在前（压缩组托管）、`resume` 段在后（回落原链）。同一 `model_id` 允许同时出现在两段（压缩者与回落者可以是同一目标），段内才保证去重。

换目标重试的既有约定不变：把已试过的 `model_id` 放进 `tried_ids` 再次调 dispatch。**注意**：`tried_ids` 是成员粒度的——同一目标在另一段仍可被返回，agent 如需"整目标不再用"应自行在两段间记账。

### 2. 决策溯源字段更替

| 旧字段 | 状态 | 替代 |
| --- | --- | --- |
| `decision.policy` / `decision.policy_version` | **移除** | `decision.collection_updated_at`（做出决策的集合配置版本，RFC 3339） |
| `decision.note` | 保留但不再产生（组合器无说明概念） | — |
| `decision.skipped[]` | 保留，成员粒度；新增 `detail` 恒带所属组（`group <name>`） | — |
| — | **新增** `decision.group_skips[]` | 整组跳过：`{group, reason: "group_exhausted", detail}`。排障时先看它——"为什么没走优先级更高的组" |

### 3. 错误码收缩

`policy_error` / `policy_timeout` 已删除（脚本沙箱随之下线）。调度失败只有 `503 target_unavailable`（retryable）与既有 4xx。agent 若对这两码有分支处理，可删除。

### 4. 模型列表瘦身

`GET /v1/models` 的 `UserModelSummary` 移除 `policy` 字段（策略不再挂在模型名上，组合配置在集合上）。

### 5. 未变更

- dispatch 请求体（`model` / `client_key` / `inbound_protocol` / `request_id` / `tried_ids` / `est_tokens`）。`est_tokens` 语义增强：relay 用它与集合 `overflow.threshold_tokens` 比较触发超长分支，agent 应如实上送估算值，虚低会让压缩托管永不触发。
- `POST /v1/results` 全量不变（含 `retry_after` 明示冷却语义）。限额降级链路靠它：上游限流 → 上报带 `retry_after` → 目标冷却 → 组内全冷即整组跳过 → 链上下一组接管。

## 验收建议

1. 普通请求：候选全 `standard`，选首个执行——回归旧行为。
2. 构造主组全冷却：`group_skips` 出现 `group_exhausted`，目标落在下一组。
3. 构造 `est_tokens` 超阈值：候选分 `compact` + `resume` 两段，验证压缩→回落执行顺序。

对照实现见 relay 仓 `backend/contract/relayv1/contract.go` 与 `docs/api.md` §4.2 / §6.3。
