# ModelSurge Relay API 规范

| | |
| --- | --- |
| 服务 | model-surge-relay（调度面） |
| API 版本 | `v1`（契约定义：`backend/contract/relayv1`） |
| 基准地址 | `http://<host>:8081`（compose 默认宿主映射 8081） |
| 内容类型 | 请求与响应体均为 `application/json; charset=utf-8`；`204` 端点无响应体 |
| 时间格式 | RFC 3339 UTC，如 `2026-09-16T10:04:02.448393Z` |

## 目录

- [1. 概述](#1-概述)
- [2. 通用约定](#2-通用约定)
- [3. 健康检查](#3-健康检查)
- [4. 调度面 API](#4-调度面-api)
  - [4.1 模型列表](#41-模型列表) · [4.2 调度](#42-调度) · [4.3 结果上报](#43-结果上报)
- [5. 管理面 API](#5-管理面-api)
  - [5.1 Collection](#51-collection) · [5.2 Group 与成员](#52-group-与成员) · [5.3 集合快照](#53-集合快照) · [5.4 策略组合](#54-策略组合) · [5.5 策略组合试运行](#55-策略组合试运行) · [5.6 User Model](#56-user-model) · [5.7 运行态](#57-运行态) · [5.8 脚本策略迁移](#58-脚本策略迁移)
- [6. 附录](#6-附录)

---

## 1. 概述

本服务是 ModelSurge 的调度面。调用方分两类：

- **数据面**（model-surge-stream）：在每次客户端请求前调用调度接口选出目标，调用结束后回报结果。设计目标是"凭据不出调度链路"——数据面拿到的是已解析的连接信息，回流的只有用量与结果类别。
- **运维面**（人与 [frontend/](../frontend/) 管理界面）：维护 Collection（含策略组合）/ Group / User Model 三层配置，观察与干预运行态。

静态配置（账号、上游模型、凭据）托管在 model-surge-upstream，本服务只读消费其下发面；运行态（冷却、失败计数、用量）是本服务的私有状态，**只能**经调度面的结果上报变更。

## 2. 通用约定

### 2.1 认证

服务暴露两组接口与一个健康检查：

| 分组 | 前缀 | 密钥 | 用途 |
| --- | --- | --- | --- |
| 健康检查 | `/healthz` | 无 | 依赖就绪状态 |
| 管理面 | `/admin/*` | `MSR_ADMIN_KEY` | 配置的增删改查（桌面客户端使用） |
| 调度面 | `/v1/*` | `MSR_DISPATCH_KEY` | 选目标与结果回报（数据面使用） |

除 `GET /healthz` 外，所有接口要求请求头：

```
Authorization: Bearer <密钥>
```

| 约定 | 说明 |
| --- | --- |
| 前缀 | `Bearer`，大小写不敏感；其后允许空白 |
| 校验 | 常数时间比较（`crypto/subtle`），防时序侧信道 |
| 失败 | 密钥缺失、格式不符或不正确一律 `401` `unauthorized`，不区分原因，不回显配置内容 |

两把密钥相互独立：管理密钥访问调度面返回 `401`，反之亦然（密钥校验先于路由匹配，因此跨面访问不会泄漏"该路径是否存在"）。两者必须不同，启动时校验强制。某一面未配置密钥时拒绝该面一切请求——空密钥不是"无鉴权"。

### 2.2 通用类型

| 记法 | JSON 类型 | 说明 |
| --- | --- | --- |
| `string` | string | UTF-8 |
| `int` | number | 整数 |
| `int64` | number | 整数（token 数等大数语义） |
| `bool` | boolean | `true` / `false` |
| `array<T>` | array | 元素类型 `T`；本 API 中数组恒不为 `null`，空为 `[]` |
| `object` | object | JSON object；本 API 中映射恒不为 `null`，空为 `{}` |
| `time` | string | RFC 3339 UTC |

### 2.3 错误信封

一切非 2xx 响应体均为：

```json
{"error": {"code": "invalid_request", "message": "collection c9 does not exist", "retryable": false, "field": "collection"}}
```

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `error.code` | string | 恒有 | 错误码，封闭集合，见 [6.1](#61-错误码表) |
| `error.message` | string | 恒有 | 人可读说明（英文），不含凭据 |
| `error.retryable` | bool | 恒有 | `true` = 换目标或稍后重试**可能**成功；是调用方重试决策的依据 |
| `error.field` | string | 仅校验错误 | 出错字段名（如 `client_key`），供表单定位控件 |

请求体不是合法 JSON 时返回 `400` + `invalid_request` + `"malformed JSON body"`（无 `field`）。

### 2.4 命名与路径

- 名称（collection / group / user model）服务端仅要求非空；**含 `/` 的名称无法作为 URL 路径段寻址**，建议 `[a-z0-9._-]`。
- 唯一例外：runtime 的 `model_id`（形如 `ds-1/v4`）含斜杠，按多段路径传递，无需编码。
- 其余含特殊字符的路径段按常规 percent-encoding 编码。

---

## 3. 健康检查

### 3.1 查询就绪状态

**使用场景**：容器编排的 healthcheck、调用方启动自检。PG 不可用时服务整体未就绪；Redis 与 upstream 不可用只降级、不影响调度能力。

```http
GET /healthz
```

**请求**：无路径参数、无查询参数、无请求体；**免鉴权**。

**响应** `200`（就绪）/ `503`（PG 不可达），字段相同：

| 字段 | 类型 | 取值与含义 |
| --- | --- | --- |
| `ready` | bool | 服务可用性，恒等于 `database` |
| `database` | bool | PostgreSQL（权威存储）可达性；`false` 时整个服务判定不可用 |
| `cache` | bool | Redis 可达性；**`false` 不影响 `ready`**——缓存仅是加速，故障时降级直查 PG。未配置 Redis（`MSR_REDIS_ADDR` 缺省）时恒为 `false` |
| `upstream` | bool | 配置中心下发面可达性；**`false` 不影响 `ready`**——但此时解析目标会失败，调度将返回 `503 target_unavailable` |

```json
{"ready": true, "database": true, "cache": true, "upstream": true}
```

> `503` 响应体是上述健康状态体，**不是** [2.3](#23-错误信封) 的错误信封。

**注意**：探活摘流只看 `ready`（或 HTTP 状态码）；`cache` / `upstream` 的降级不构成摘流理由。

---

## 4. 调度面 API

### 4.1 模型列表

**使用场景**：数据面发现可调度的对外模型名（通常用于向客户端暴露模型清单）。

```http
GET /v1/models
```

**请求**：无请求体，无路径参数。

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `models` | `array<UserModelSummary>` | 已启用的 user model，按名称字典序；禁用的不出现在列表里 |

`UserModelSummary`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `name` | string | 恒有 | 对外模型名，作为 4.2 `model` 的取值 |
| `collection` | string | 恒有 | 所属集合名（调度策略组合挂在集合上） |
| `protocol` | string | 配置了约束时 | 期望入站协议；**缺省 = 不限** |
| `enabled` | bool | 恒有 | 此端点恒为 `true` |

```json
{"models": [{"name": "demo-pool", "collection": "demo", "protocol": "anthropic", "enabled": true}]}
```

### 4.2 调度

**使用场景**：数据面在转发客户端请求前调用，选出目标并获取其全套连接信息（含真实认证头）。每次客户端请求调用一次；换目标重试时把已试过的目标放进 `tried_ids` 再次调用。

```http
POST /v1/dispatch
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 须为存在的 user model 名 | 对外模型名。不存在 → `404`；被禁用 → `403 disabled` |
| `client_key` | string | 是 | 非空 | 该 user model 的调用方密钥。不匹配 → `401`（`"client key mismatch"`，与调度密钥错误同码） |
| `inbound_protocol` | string | 否 | `anthropic` \| `chat_completions` \| `responses` \| `gemini` | 入站协议。user model 配置了 `protocol` 且与此不符 → `400`（`field: inbound_protocol`）；两者任一为空则不校验 |
| `request_id` | string | 否 | 任意字符串 | 调用方请求标识；原样回显并写日志，用于链路对账 |
| `tried_ids` | `array<string>` | 否 | 目标 `model_id` 列表，重复值容忍（去重处理） | 本次会话已尝试的目标，调度时排除（跳过原因 `already_tried`） |
| `est_tokens` | int | 否 | 任意整数，缺省 0 | 请求预估 token 数。与集合策略组合的 `overflow.threshold_tokens` 比较：达到阈值触发超长压缩托管（候选分 `compact` / `resume` 两段，见 `Decision`） |

```json
{"model": "demo-pool", "inbound_protocol": "anthropic", "client_key": "sk-demo-pool-2026", "request_id": "req-1", "tried_ids": [], "est_tokens": 8192}
```

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `request_id` | string | 请求中的 `request_id` 原样回显 |
| `target` | `Target` | 选中目标的全套连接信息 |
| `decision` | `Decision` | 决策溯源 |

`Target`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `model_id` | string | 恒有 | 目标标识，可能含斜杠（如 `ds-1/v4`）；结果上报时引用 |
| `account` | string | 恒有 | 账号名（upstream 目录侧） |
| `provider_id` | string | 恒有 | 上游 provider 标识 |
| `protocol` | string | 恒有 | 出站协议（由 upstream 目录决定，非入站协议） |
| `base_url` | string | 恒有 | 上游基地址 |
| `native_model` | string | 恒有 | 传给上游的真实模型名（可能与 `model_id` 不同名） |
| `context_window` | int | 目录声明时 | 上下文窗口（token 数） |
| `headers` | object | 恒有 | **真实认证头**，键值由上游协议决定（anthropic 为 `x-api-key` + `anthropic-version`，OpenAI 系为 `Authorization: Bearer ...` 等）。原样并入上游请求，**不得落日志** |
| `defaults` | object | 恒有 | 参数默认层：请求体**缺失**的字段才填入 |
| `overrides` | object | 恒有 | 参数强制层：无论请求体是否携带一律压盖 |

`defaults` / `overrides` 是两层**未合并**的参数，本服务只搬运不解释；数据面在编码出上游请求体之后按"defaults 补缺 → overrides 压盖"作用。两层恒为 JSON object，目录未配置时为 `{}`。

`Decision`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `collection` | string | 恒有 | 本次调度读取的集合名 |
| `collection_updated_at` | time | 恒有 | 集合配置（含策略组合）的最近更新时间，即做出本次决策的配置版本 |
| `group` | string | 恒有 | 命中目标所属组名 |
| `group_type` | string | 恒有 | 该组类型（自由文本，服务不解释） |
| `candidates` | `array<PhasedCandidate>` | 恒有 | 阶段化候选，按执行顺序；首个即 `target` |
| `group_skips` | `array<GroupSkip>` | 有整组跳过时 | 被整组跳过的组及原因 |
| `skipped` | `array<Skip>` | 有成员被丢弃时 | 被丢弃的成员及原因 |

`PhasedCandidate`：

| 字段 | 类型 | 取值 / 含义 |
| --- | --- | --- |
| `model_id` | string | 候选目标 |
| `phase` | string | 执行阶段，封闭集合：`standard`（未触发超长时的唯一段）/ `compact`（压缩组托管段，数据面对其发压缩请求）/ `resume`（压缩完成后回落继续原任务的段）。**按序执行、不问语义**；同一目标允许同时出现在 `compact` 与 `resume` 两段 |

`GroupSkip`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `group` | string | 恒有 | 被跳过的组名 |
| `reason` | string | 恒有 | 恒为 `group_exhausted`：组内成员全部不可用（含限额冷却——限额是被动的，靠上游限流上报转化为冷却参与判定） |
| `detail` | string | 有补充信息时 | 如 `all 3 members unavailable` |

`Skip`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `model_id` | string | 恒有 | 被丢弃的候选 |
| `reason` | string | 恒有 | 封闭集合，见 [6.2](#62-跳过原因) |
| `detail` | string | 有补充信息时 | 成员跳过时为该成员所属组（`group <name>`）；`reason=resolve_failed` 时为解析失败的底层原因（不含凭据） |

```json
{
  "request_id": "req-1",
  "target": {
    "model_id": "kimi-k2-turbo", "account": "kimi-1", "provider_id": "kimi",
    "protocol": "anthropic", "base_url": "https://api.moonshot.cn/coding",
    "native_model": "kimi-k2-turbo", "context_window": 256000,
    "headers": {"x-api-key": "<真实上游密钥>", "anthropic-version": "2023-06-01"},
    "defaults": {"temperature": 0.6, "max_tokens": 4096},
    "overrides": {}
  },
  "decision": {
    "collection": "demo", "collection_updated_at": "2026-09-29T08:00:00Z",
    "group": "primary", "group_type": "fast",
    "candidates": [
      {"model_id": "kimi-k2-turbo", "phase": "standard"},
      {"model_id": "ds-1/v4", "phase": "standard"}
    ],
    "group_skips": [
      {"group": "premium", "reason": "group_exhausted", "detail": "all 2 members unavailable"}
    ],
    "skipped": [
      {"model_id": "ds-1/v4-thinking", "reason": "cooling", "detail": "group premium"}
    ]
  }
}
```

**失败语义**：

| 场景 | 状态码 / code | retryable |
| --- | --- | --- |
| 无可用候选（全部组被跳过或成员全被过滤） | `503` `target_unavailable` | 是 |
| 候选全部解析失败 | `503` `target_unavailable` | 是 |

### 4.3 结果上报

**使用场景**：数据面在调用结束后（无论成败）回报结果，驱动用量统计与冷却。按 `report_id` 幂等——重复上报（含崩溃后重放）不会重复计数。

```http
POST /v1/results
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `report_id` | string | 是 | 非空；全局唯一（幂等键） | 调用方生成。重复提交返回 `applied: false`，无状态变更 |
| `request_id` | string | 是 | 非空 | 对应 dispatch 的请求标识 |
| `model_id` | string | 是 | 非空 | 实际使用的目标 `model_id` |
| `outcome` | string | 是 | 封闭集合（下表） | 结果类别。集合外的值 → `400`（`field: outcome`，message 回显原值） |
| `usage` | object | 否 | 字段见下；应上报非负值 | 本次用量 |
| `retry_after` | string | 否 | RFC 3339 UTC 时刻 | **上游明示的该目标最早可重试时刻**。数据面从上游限流响应头（`Retry-After`、`anthropic-ratelimit-unified-*-reset`、`x-ratelimit-reset-*`、`x-codex-*-reset-after-seconds`）或 Gemini 的 `google.rpc.RetryInfo` 解出。**缺省（键不出现）表示上游没说**，回落到失败计数启发式 |

`outcome` 取值及对运行态的作用：

| 取值 | 用量累计 | 失败计数 | 冷却 |
| --- | --- | --- | --- |
| `normal` | 是 | 清零 | 立即解除 |
| `abnormal` | 是 | +1 | 达阈值触发 |
| `retrying` | 是 | +1 | 达阈值触发 |
| `invalid_model` | 是 | +1 | 达阈值触发 |
| `transport` | **否** | **不变** | **不变**（数据面出站连接层故障：它连接池里那条连接坏了，上游可能完全健康，不产生任何运行态记录） |
| `context_exceeded` | **否** | **不变** | **不变**（请求太大是请求的问题，不产生任何运行态记录） |

**`retry_after` 如何改变冷却判定**（只作用于上表中的失败类 outcome）：

| 情形 | 冷却时长 | 失败计数 |
| --- | --- | --- |
| 带可信的 `retry_after` | **立刻冷却到该时刻**，不要求失败计数达阈值 | 照样 +1 |
| 带 `retry_after` 但已有更晚的冷却 | 保留更晚的那个（只向后推进） | 照样 +1 |
| 不带 `retry_after` | 达阈值后按 `MSR_COOLDOWN_DURATION` | +1 |
| `retry_after` 不可信（过去的时刻 / 超出当下 24 小时） | **回落到启发式**（不是忽略本次失败） | +1 |
| `outcome` 为 `normal`、`transport` 或 `context_exceeded` | 忽略该字段 | 见上表 |

几点定死的语义：

- **明示优先于启发式。** 上游说「5 小时后再来」时按默认 1 分钟猜，等于在整个限流窗口里反复空转，而每次空转都是一次真实的失败上报。
- **失败计数在明示路径下仍然递增。** 明示的冷却解除后，一个反复限流的目标应当仍被启发式逐步压制，否则它会在每个窗口开头被重新选中。
- **接收侧独立校验可信性。** 数据面校验过的时刻到达这里时已经过了排队与网络往返，所以本服务再判一次：不晚于当下、或晚于当下 24 小时的，都按坏数据处理。
- **不可信时回落而非忽略。** 忽略会让这次真实的失败完全不计入冷却判定。
- **该字段不可信不会让整条上报被拒。** 上报本身有效（用量要记、失败要计），只是这一维不可用。
- **服务不会自己编造这个时刻**，也不接受把请求里的值当成上游的回答。

`usage`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `input_tokens` | int64 | 输入 token 数 |
| `output_tokens` | int64 | 输出 token 数 |
| `cache_read_tokens` | int64 | 缓存命中读取的 token 数 |

```json
{"report_id": "rep-1", "request_id": "req-1", "model_id": "kimi-k2-turbo", "outcome": "normal", "usage": {"input_tokens": 1200, "output_tokens": 340, "cache_read_tokens": 0}}
```

**响应** `200`：

| 字段 | 类型 | 取值 / 含义 |
| --- | --- | --- |
| `applied` | bool | `true` = 首次收到并已作用到运行态；`false` = 重复上报，无状态变更 |

```json
{"applied": true}
```

---

## 5. 管理面 API

面向运维与 frontend/ 管理界面。全部需要 `Authorization: Bearer $MSR_ADMIN_KEY`。通用约定：

- **列表端点解包装**（`{"<资源复数>": [...]}`）；**单体端点裸返实体**。
- 创建成功 `201` + 实体；更新类端点有的返回实体（`200`）、有的无体（`204`），各端点分别注明。
- user model 的 `client_key` 在任何读响应中都不出现。
- 列表排序：collections / user_models / runtime 按名称（或 model_id）字典序；groups 按 `position` 升序、并列按 `name`；组内成员按组内 `position` 升序、并列按 `model_id`。

### 5.1 Collection

集合是配置顶层容器：名称 + 备注，下挂有序 Group。

#### 5.1.1 列出集合

**使用场景**：管理界面集合列表；脚本盘点现有配置。

```http
GET /admin/collections
```

**请求**：无请求体。

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `collections` | `array<Collection>` | 按名称字典序 |

`Collection`（所有集合端点共用的实体形态）：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `name` | string | 集合名，主键 |
| `note` | string | 备注，自由文本，无长度上限 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 最近更新时间 |

#### 5.1.2 创建集合

**使用场景**：新建一组目标的编排容器。

```http
POST /admin/collections
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | 非空（trim 后）；全局唯一 | 集合名。重名 → `409` |
| `note` | string | 否 | 自由文本，缺省空串 | 备注 |

**请求示例**：

```json
{"name": "demo", "note": "演示集合"}
```

**响应** `201` + `Collection` 实体。

#### 5.1.3 查询集合

**使用场景**：读取单个集合的元数据。

```http
GET /admin/collections/{name}
```

| 路径参数 | 类型 | 约束 |
| --- | --- | --- |
| `name` | string | 须存在，否则 `404` |

**响应** `200` + `Collection` 实体（裸返，无包装）。

#### 5.1.4 修改备注

**使用场景**：只改备注。**注意**：此端点不做集合改名。

```http
PUT /admin/collections/{name}
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `note` | string | 是（可为空串） | 自由文本 | 新备注。`name` 字段即使提交也被忽略 |

**请求示例**：

```json
{"note": "新备注"}
```

**响应** `204`（无体）。集合不存在 → `404`。

#### 5.1.5 删除集合

**使用场景**：下线一组编排。级联删除其下全部 Group。

```http
DELETE /admin/collections/{name}
```

**响应** `204`。被 user model 引用 → `409`，`message` 列出引用者。不存在 → `404`。

### 5.2 Group 与成员

Group 是集合内的有序编组，`position` 决定顺序。

#### 5.2.1 列出组

**使用场景**：查看某集合的编组与成员引用。不含目录属性——需要判断引用有效性时用 [5.3 快照](#53-集合快照)。

```http
GET /admin/collections/{name}/groups
```

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `groups` | `array<Group>` | 按 `position` 升序、并列按 `name` |

`Group`（组端点共用的实体形态）：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `collection` | string | 恒有 | 所属集合名 |
| `name` | string | 恒有 | 组名，集合内唯一 |
| `type` | string | 恒有 | 组类型，**自由文本**（如 `fast` / `cheap` / `compact`），服务不解释；创建时须非空。迁移器按 `compact` 类型识别压缩组（见 [5.8](#58-脚本策略迁移)） |
| `position` | int | 恒有 | 排序键，任意整数，升序生效（惯例从 0 起） |
| `config` | 任意 JSON 值 | 恒有 | 组级参数（服务不解析、不校验形状，调度不消费；迁移器读 `compact_above_tokens`，见 [5.8](#58-脚本策略迁移)）；写入缺省或空归一为 `{}`，惯例用 object |
| `members` | `array<string>` | 恒有 | 成员 `model_id` 引用列表，按组内 position 序 |

#### 5.2.2 创建组

**使用场景**：在集合内新增编组（如新增一个备用组）。

```http
POST /admin/collections/{name}/groups
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | 非空；集合内唯一 | 组名。重名 → `409` |
| `type` | string | 是 | 非空 | 组类型（自由文本） |
| `position` | int | 否 | 任意整数，缺省 0 | 排序位置 |
| `config` | 任意 JSON 值 | 否 | — | 组级参数；缺省 `{}` |

**请求示例**：

```json
{"name": "primary", "type": "fast", "position": 0, "config": {"weight": 100}}
```

**响应** `201` + `Group` 实体。集合不存在 → `404`。

#### 5.2.3 更新组

**使用场景**：改组的类型、排序位置或配置。

```http
PUT /admin/collections/{name}/groups/{group}
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | — | 忽略 | 以路径 `{group}` 为准 |
| `type` | string | 是 | 非空 | 新组类型。缺失或空白 → `400`（`field: type`） |
| `position` | int | 否 | 任意整数，缺省 0 | 新排序位置 |
| `config` | 任意 JSON 值 | 否 | — | 新组级参数；缺省 `{}` |

**整体替换**语义：三个字段一律以请求体为准，不是"缺省保留"——未提交的 `position` 归零、`config` 清成 `{}`。

**请求示例**：

```json
{"type": "fast", "position": 0, "config": {"weight": 100}}
```

**响应** `204`。集合或组不存在 → `404`；`type` 缺失 → `400`。

#### 5.2.4 删除组

**使用场景**：移除编组（连同其成员引用）。

```http
DELETE /admin/collections/{name}/groups/{group}
```

**响应** `204`。不存在 → `404`。被策略组合引用（出现在 `priority_chain` 或 `overflow.compact_groups` 中）→ `409`，`message` 指出引用位置——先从策略组合中移除该组再删除。

#### 5.2.5 整组替换成员

**使用场景**：维护组内目标清单与顺序。**整体替换**语义：提交列表即最终列表。

```http
PUT /admin/collections/{name}/groups/{group}/members
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `members` | `array<string>` | 是 | `model_id` 引用列表，均须存在于 upstream 目录 | 最终成员清单（`[]` = 清空成员）；数组顺序即成员 position |

写入时**逐引用校验**目录存在性：任一未知引用 → 整批拒绝 `400`（`field: members`，message 列出未知引用；目录不可达同样拒写）；同一引用重复 → `400`（`duplicate member`）。引用在写入后从目录消失（账号或模型被删）时，快照标 `known: false`，调度跳过（原因 `unknown_model`）。

**请求示例**：

```json
{"members": ["kimi-k2-turbo", "ds-1/v4"]}
```

**响应** `204`。集合或组不存在 → `404`。

### 5.3 集合快照

**使用场景**：判断成员引用是否仍然有效（`groups` 端点只返回引用字符串，无从判断）。这是组合器展开候选时看到的视图：成员已叠加 upstream 目录属性。

```http
GET /admin/collections/{name}/snapshot
```

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `name` | string | 集合名 |
| `groups` | `array<GroupSnapshot>` | 按 position 序；恒为数组（空为 `[]`） |

`GroupSnapshot`：字段同 `Group`（无 `collection` 字段），但 `members` 为 `array<Member>`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `model_id` | string | 恒有 | 成员引用 |
| `account` | string | `known=true` 时 | 账号名 |
| `provider_id` | string | `known=true` 时 | provider 标识 |
| `protocol` | string | `known=true` 时 | 出站协议 |
| `native_model` | string | `known=true` 时 | 上游真实模型名 |
| `context_window` | int | 目录声明时 | 上下文窗口（token） |
| `enabled` | bool | `known=true` 时 | 目录侧启用状态。`false` 是**正常运维状态**（临时摘除），调度跳过（原因 `disabled`） |
| `known` | bool | 恒有 | `false` = 引用在目录中已消失（配置错误），调度跳过（原因 `unknown_model`）。与 `enabled: false` 语义不同，勿混用 |
| `position` | int | 恒有 | 组内顺序 |

`known=false` 时目录属性字段为零值，不应读取。

### 5.4 策略组合

策略组合挂在 Collection 上，是内置组合器的两组配置：**优先级链**（有序组链，整组不可用时自动落到下一组）与**超长压缩托管**（`est_tokens` 达阈值时先走压缩组、压缩完成回落原链）。组合语义与阶段化候选见 [6.3](#63-策略组合语义)。

`Strategy`（策略组合端点共用的实体形态）：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `priority_chain` | `array<string>` | 配置了链时 | 有序组名列表，元素须为本集合的组且不重复；**缺省 / 空 = 按组 `position` 顺序展开** |
| `overflow` | object | 恒有 | 超长压缩托管配置，见下 |

`overflow`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `enabled` | bool | 恒有 | 是否启用压缩托管 |
| `threshold_tokens` | int | 启用时 | 触发阈值；`est_tokens` ≥ 该值进入压缩托管。启用时须为正整数 |
| `compact_groups` | `array<string>` | 启用时 | 压缩组列表，元素须为本集合的组；启用时须非空 |

写入校验（任一不满足 → `400`，`field` 指明出错字段）：

| 情形 | `field` |
| --- | --- |
| 链引用未知组 / 链内重复组 | `priority_chain` |
| 启用但阈值非正 | `threshold_tokens` |
| 启用但压缩组为空 / 压缩组引用未知组 | `compact_groups` |

#### 5.4.1 查询策略组合

**使用场景**：管理界面「策略组合」编辑器回填。

```http
GET /admin/collections/{name}/strategy
```

**响应** `200` + `Strategy` 实体（裸返，无包装）。集合不存在 → `404`。

#### 5.4.2 写入策略组合

**使用场景**：配置或修改集合的优先级链与压缩托管。**整体替换**语义：提交的 `Strategy` 即最终配置。

```http
PUT /admin/collections/{name}/strategy
```

**请求体**：`Strategy` 实体（字段约束见上表）。

**请求示例**：

```json
{
  "priority_chain": ["premium", "standard"],
  "overflow": {"enabled": true, "threshold_tokens": 200000, "compact_groups": ["compact-pool"]}
}
```

**响应** `204`。集合不存在 → `404`；校验失败 → `400`（`field` 见上表）。写入立即生效——下一次调度即按新组合展开。

### 5.5 策略组合试运行

**使用场景**：上线前验证组合配置。读**真实**集合快照与**真实**运行态，与调度共用同一个组合器核——看到的候选序列就是上线后的序列。不解析目标、不写运行态、不写缓存，因此不影响任何真实请求。

```http
POST /admin/collections/{name}/strategy/dry-run
```

**请求体**：

| 字段 | 类型 | 必填 | 含义 |
| --- | --- | --- | --- |
| `est_tokens` | int | 否 | 模拟的预估 token 数（驱动超长判定，缺省 0） |
| `tried_ids` | `array<string>` | 否 | 模拟的已尝试列表（跳过原因 `already_tried`） |

**请求示例**：

```json
{"est_tokens": 300000, "tried_ids": ["kimi-k2-turbo"]}
```

**响应** `200`：`{"decision": <Decision>}`，`Decision` 形态同 [4.2](#42-调度)（阶段化 `candidates`、`group_skips`、`skipped` 齐全），唯 `group` / `group_type` 为空——试运行不选定目标。

```json
{
  "decision": {
    "collection": "demo", "collection_updated_at": "2026-09-29T08:00:00Z",
    "candidates": [
      {"model_id": "compact-1/c", "phase": "compact"},
      {"model_id": "ds-1/v4", "phase": "resume"}
    ],
    "group_skips": [{"group": "premium", "reason": "group_exhausted", "detail": "all 2 members unavailable"}],
    "skipped": [{"model_id": "kimi-k2-turbo", "reason": "already_tried", "detail": "group premium"}]
  }
}
```

**失败**：集合不存在 → `404`。

### 5.6 User Model

User model 是对外暴露的模型名：调用方以 `name` + `client_key` 走调度面。

#### 5.6.1 列出

**使用场景**：管理界面模型名列表。

```http
GET /admin/user-models
```

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `user_models` | `array<UserModel>` | 按名称字典序 |

`UserModel`（**读侧实体形态，不含 `client_key`**）：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `name` | string | 恒有 | 模型名，主键 |
| `collection` | string | 恒有 | 所属集合（调度按其策略组合展开候选） |
| `protocol` | string | 配置时 | 入站协议约束；缺省 = 不限 |
| `enabled` | bool | 恒有 | `false` 时调度面返回 `403 disabled` |
| `created_at` / `updated_at` | time | 恒有 | 时间戳 |

#### 5.6.2 创建

**使用场景**：对外发布一个模型名。

```http
POST /admin/user-models
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | 非空；全局唯一 | 模型名。重名 → `409` |
| `collection` | string | 是 | 须存在 | 所属集合。不存在 → `400`（`field: collection`） |
| `client_key` | string | 是 | 非空 | 调用方密钥。只在创建/更新请求体中出现，任何读响应都不返回 |
| `protocol` | string | 否 | `anthropic` \| `chat_completions` \| `responses` \| `gemini` | 入站协议约束；缺省 = 不限 |
| `enabled` | bool | 否 | 缺省 `false` | 是否启用。**创建时缺省即停用**，要立刻可用需显式 `true` |

**请求示例**：

```json
{"name": "demo-pool", "collection": "demo", "client_key": "sk-demo-pool-2026", "protocol": "anthropic", "enabled": true}
```

**响应** `201` + `UserModel` 实体（无密钥）。

#### 5.6.3 查询

**使用场景**：编辑表单回填。

```http
GET /admin/user-models/{name}
```

**响应** `200` + `UserModel` 实体（无密钥）。不存在 → `404`。

#### 5.6.4 更新

**使用场景**：修改绑定、密钥或启停。

```http
PUT /admin/user-models/{name}
```

**请求体**：

| 字段 | 类型 | 必填 | 允许取值 / 约束 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | — | 忽略 | 以路径 `{name}` 为准 |
| `collection` | string | 是 | 须存在 | 所属集合。不存在 → `400`（`field: collection`） |
| `client_key` | string | 是 | 非空 | 新密钥。**必须重新提交**（响应从不回显旧值）；缺失或空串 → `400`（`field: client_key`） |
| `protocol` | string | 否 | `anthropic` \| `chat_completions` \| `responses` \| `gemini`，或空串 | 入站协议约束；**空串 = 不限** |
| `enabled` | bool | 否 | 缺省 `false` | 是否启用。**漏提交即停用**，请显式携带 |

**整体替换**语义，无"空则保留"：未提交的 `protocol` 会被清空（不限）、`enabled` 归 `false`；`client_key` 旧值不回显，必须重填。

**请求示例**：

```json
{"collection": "demo", "client_key": "sk-demo-pool-2026", "protocol": "anthropic", "enabled": true}
```

**响应** `200` + `UserModel` 实体。引用的 collection 不存在 → `400`。不存在 → `404`。

#### 5.6.5 删除

**使用场景**：下线模型名。

```http
DELETE /admin/user-models/{name}
```

**响应** `204`。不存在 → `404`。

### 5.7 运行态

调度运行态（冷却 / 失败计数 / 用量）只读 + 重置：数据只经调度面的结果上报（4.3）变更。

#### 5.7.1 列出运行态

**使用场景**：观察各目标的健康与用量（管理界面"运行态"页）。

```http
GET /admin/runtime
```

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `runtime` | `array<RuntimeState>` | 按 `model_id` 字典序；恒为数组（空为 `[]`） |

`RuntimeState`：

| 字段 | 类型 | 出现条件 | 取值 / 含义 |
| --- | --- | --- | --- |
| `model_id` | string | 恒有 | 目标标识（可能含斜杠） |
| `cooling` | bool | 恒有 | 是否处于冷却。**判断冷却与否始终以此字段为准**，客户端时钟不参与 |
| `cooling_until` | time | 冷却中时 | 冷却截止时刻（RFC 3339；注意与 dry-run 的 Unix 秒形态不同） |
| `consecutive_failures` | int | 恒有 | 连续失败次数（`normal` 上报清零） |
| `usage` | object | 恒有 | 累计用量，字段见下 |
| `updated_at` | time | 恒有 | 最近一次状态变更时间 |

`usage`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `input_tokens` | int64 | 累计输入 token |
| `output_tokens` | int64 | 累计输出 token |
| `cache_read_tokens` | int64 | 累计缓存命中读取 token |
| `request_count` | int64 | 累计请求次数（有效上报次数） |

#### 5.7.2 重置运行态

**使用场景**：目标恢复后手动解除冷却（不想等冷却自然到期）。

```http
DELETE /admin/runtime/{model_id}
```

| 路径参数 | 类型 | 约束 |
| --- | --- | --- |
| `model_id` | string | 形如 `ds-1/v4` **含斜杠**，作为多段路径传递（`/admin/runtime/ds-1/v4`），无需编码成 `%2F` |

**响应** `204`。清零冷却与失败计数，**保留用量**（审计数据）。无该目标的运行态记录 → `404`（`"no runtime state for target ..."`）。

### 5.8 脚本策略迁移

**使用场景**：从已下线的脚本策略（`policies` 表 + `user_models.policy` 绑定）迁移到策略组合。一次性运维动作；幂等，可重复执行——已符合映射结果的集合会被重写为同一配置，不产生重复副作用。

```http
POST /admin/migrate-policies
```

**请求**：无请求体。

**迁移规则**：按 user model 的现存绑定把具名脚本映射为所在集合的组合配置。同一集合被多个脚本引用时取多数（并列取字典序最小者）。映射关系：

| 脚本名 | 迁移结果 |
| --- | --- |
| `failover` / `sticky` / `preset` / `round_robin` / `least_used` | `priority_chain` = 集合现有组顺序，`overflow` 关闭 |
| `compact_overflow` | 主池（非 `compact` 类型组）入链 + `overflow` 启用；阈值取主池组 `config.compact_above_tokens`，缺省取主池成员最大 `context_window`；`compact` 类型组入 `compact_groups`。阈值无从确定时 `overflow.enabled=false` 并在报告 `note` 说明 |
| 其他脚本名 | 写入默认链（组顺序），并入 `unmapped` 报告人工跟进 |

**响应** `200`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `mapped` | `array` | 已映射清单：`{collection, policy, note?}` |
| `conflicts` | `array` | 多脚本引用冲突清单：`{collection, policies, chosen}`（`chosen` 为实际采用的脚本） |
| `unmapped` | `array` | 无法映射、落了默认链的清单：`{collection, policy}` |

```json
{
  "mapped": [{"collection": "demo", "policy": "compact_overflow", "note": "threshold from group config"}],
  "conflicts": [{"collection": "c2", "policies": ["failover", "homegrown"], "chosen": "failover"}],
  "unmapped": []
}
```

**失败**：部署未启用迁移器 → `409`。迁移不删除 `policies` 表与 `user_models.policy` 列（保留供反查）；确认报告无遗留后由人工 DROP。

---

## 6. 附录

### 6.1 错误码表

| code | HTTP | retryable | 含义 |
| --- | --- | --- | --- |
| `unauthorized` | 401 | 否 | 密钥错误或缺失；调度面亦用于 client_key 不匹配 |
| `not_found` | 404 | 否 | 资源不存在 |
| `invalid_request` | 400 | 否 | 请求校验失败；`field` 指明出错字段 |
| `conflict` | 409 | 否 | 重名冲突，或删除被引用资源（`message` 列出引用者） |
| `disabled` | 403 | 否 | user model 被禁用 |
| `target_unavailable` | 503 | **是** | 无可用目标：候选全被过滤 / 全部解析失败 |
| `internal_error` | 500 | **是** | 其余一切；底层细节不外泄 |

`retryable: true` 的两码是数据面重试决策的依据；其余重试无意义。

### 6.2 跳过原因

dispatch 响应 `decision.skipped[].reason` 的取值（成员粒度，按判定优先级排列）：

| reason | 含义 |
| --- | --- |
| `already_tried` | 在请求的 `tried_ids` 里 |
| `unknown_model` | 引用在 upstream 目录中已消失 |
| `disabled` | 目录中该模型未启用 |
| `cooling` | 目标处于冷却期（含限额冷却——限流上报转化的冷却） |
| `resolve_failed` | 向 upstream 解析目标失败，`detail` 带原因 |

另有整组粒度：`decision.group_skips[].reason` 恒为 `group_exhausted`，表示该组全部成员因上述原因之一不可用，组合器已自动落到链上下一组。

### 6.3 策略组合语义

组合器是内置纯函数，每次调度执行一次，输入为集合快照、运行态、`tried_ids`、`est_tokens` 与集合上的 `Strategy`，输出为**阶段化候选序列**：

1. **未触发超长**（`overflow.enabled=false`，或 `est_tokens < threshold_tokens`）：按 `priority_chain`（空 = 组 `position` 顺序）逐组展开可用成员，`phase=standard`。
2. **触发超长**（`overflow.enabled=true` 且阈值为正且 `est_tokens` ≥ 阈值）：先展开 `compact_groups` 的可用成员，`phase=compact`（数据面对其发压缩请求）；再按链展开，`phase=resume`（压缩完成后回落继续原任务）。

展开规则的细节：

- 成员不可用（`already_tried` / `unknown_model` / `disabled` / `cooling`）即跳过并记 `skipped`；组内可用成员为零时整组跳过并记 `group_skips`，**继续展开后续组**——"整组不可用自动降级到下一优先级组"由此实现，限额耗尽走的是 `cooling` 这一被动判定，服务不维护配额账本。
- 候选段内去重；同一目标允许同时出现在 `compact` 与 `resume` 两段（压缩者与回落者可以是同一目标）。
- 组合器只展开不解析：取凭据在组合器返回后由调度层完成，凭据结构上无法进入决策逻辑。
- 数据面按 `candidates` 顺序执行、`phase` 决定动作，不问语义；决策溯源以 `collection_updated_at` 关联做出决策的配置版本。

### 6.4 运行参数（环境变量）

与本 API 行为相关的运行参数：

| 环境变量 | 默认 | 影响 |
| --- | --- | --- |
| `MSR_COOLDOWN_THRESHOLD` | `3` | 连续失败达到该值进入冷却；`<=0` 从不冷却 |
| `MSR_COOLDOWN_DURATION` | `1m` | 冷却时长 |
| `MSR_CACHE_TTL` | 见 config | Redis 读缓存 TTL |

连接类变量（`MSR_PG_DSN` / `MSR_REDIS_ADDR` / `MSR_REDIS_PASSWORD` / `MSR_REDIS_DB` / `MSR_DISPATCH_KEY` / `MSR_ADMIN_KEY` / `MSR_UPSTREAM_BASE_URL` / `MSR_UPSTREAM_DELIVERY_KEY` / `MSR_LISTEN`）见根 [README.md](../README.md)。