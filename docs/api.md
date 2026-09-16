# ModelSurge Relay API 参考

- 版本：`v1`（契约包 `backend/contract/relayv1`）
- 基准地址：`http://<host>:8081`（compose 默认映射宿主 8081）
- 内容类型：请求与响应体均为 `application/json`；`204 No Content` 端点无响应体
- 时间格式：RFC 3339 UTC（如 `2026-09-16T10:04:02.448393Z`）

## 目录

- [服务定位](#服务定位)
- [认证](#认证)
- [错误信封](#错误信封)
- [调度面 API](#调度面-api)
  - [健康探测](#1-健康探测) · [模型列表](#2-模型列表) · [调度](#3-调度) · [结果上报](#4-结果上报)
- [管理面 API](#管理面-api)
  - [Collection](#collection) · [Group 与成员](#group-与成员) · [策略](#策略) · [User Model](#user-model) · [运行态](#运行态)
- [附录](#附录)
  - [错误码表](#错误码表) · [跳过原因](#跳过原因) · [策略脚本输入](#策略脚本输入) · [冷却参数](#冷却参数)

---

## 服务定位

本服务是调度面：数据面（model-surge-stream）调用 `/internal/v1/*` 完成目标选择与结果回报；运维通过 `/admin/*` 管理配置。配置静态部分（账号、上游模型、凭据）托管在 model-surge-upstream，本服务只读消费其下发面。

两个平面各持一把独立密钥，挂在不同的前缀子树上 —— 隔离由路由结构保证。两把密钥配置相同会被启动校验直接拒绝。

## 认证

| 平面 | 路径前缀 | 凭据位置 |
| --- | --- | --- |
| 调度面 | `/internal/v1` | `Authorization: Bearer <MSR_DISPATCH_KEY>` |
| 管理面 | `/admin` | `X-Admin-Key: <MSR_ADMIN_KEY>` |

- 凭据不匹配统一返回 `401` + 错误信封，不区分"密钥错误"与"密钥缺失"，不回显任何配置内容。
- 走错平面（如对 `/admin` 用 Bearer）表现为未认证。
- 未配置密钥的平面拒绝一切请求 —— 空密钥不是"无鉴权"。

```bash
curl -H "Authorization: Bearer $MSR_DISPATCH_KEY" http://127.0.0.1:8081/internal/v1/models
curl -H "X-Admin-Key: $MSR_ADMIN_KEY"           http://127.0.0.1:8081/admin/collections
```

## 错误信封

一切非 2xx 响应体都是同一形态：

```json
{
  "error": {
    "code": "invalid_request",
    "message": "collection c9 does not exist",
    "retryable": false,
    "field": "collection"
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `code` | string | 机器可读错误码，见[错误码表](#错误码表) |
| `message` | string | 人可读说明，开发语言为英文 |
| `retryable` | bool | 换目标或稍后重试**可能**成功时为 `true`；它是调用方重试决策的依据 |
| `field` | string | 出错字段名，仅校验类错误携带（如 `client_key`、`inbound_protocol`） |

请求体不是合法 JSON 时返回 `400` + `invalid_request` + `"malformed JSON body"`。

---

## 调度面 API

面向数据面（model-surge-stream）。除健康探测外全部需要 Bearer 调度密钥。

### 1. 健康探测

```http
GET /internal/v1/health
```

**鉴权**：需要（与调度面其余端点一致）。

**响应** `200`：

```json
{
  "status": "ok",
  "database": "ok",
  "cache": "ok",
  "upstream": "ok"
}
```

| 字段 | 取值 | 说明 |
| --- | --- | --- |
| `status` | `ok` / `unready` | PG 不可用即整体 `unready`，此时返回 `503` |
| `database` | `ok` / `unreachable` | PostgreSQL（权威存储） |
| `cache` | `ok` / `disabled` | Redis；不可用只降级标注，不影响就绪判定 |
| `upstream` | `ok` / `unreachable` | 配置中心下发面；同上，只降级 |

PG 恢复后自动回到 `ok`，无需重启。Redis 与 upstream 不可用时服务仍可调度（读库直查、解析报错），因此**不要**拿整体 `status` 之外的字段做探活摘流。

### 2. 模型列表

```http
GET /internal/v1/models
```

返回所有**已启用**的 user model 摘要。不含任何密钥。

**响应** `200`：

```json
{
  "models": [
    {
      "name": "demo-pool",
      "collection": "demo",
      "policy": "failover",
      "protocol": "anthropic",
      "enabled": true
    }
  ]
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 对外模型名，dispatch 的 `model` 取值 |
| `collection` | string | 所属集合 |
| `policy` | string | 绑定策略；空表示走兜底顺序（组顺序展平） |
| `protocol` | string | 入站协议约束；空表示不限 |
| `enabled` | bool | 恒为 `true`（禁用的不出现） |

### 3. 调度

```http
POST /internal/v1/dispatch
```

选择一个目标，返回其全套连接信息（含真实认证头）与决策溯源。

**请求体**：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是 | user model 名。不存在 → `404`；密钥不匹配 → `401`；被禁用 → `403 disabled` |
| `inbound_protocol` | string | 否 | 入站协议：`anthropic` / `chat_completions` / `responses` / `gemini`。user model 配了 `protocol` 且与此不符 → `400`（`field: inbound_protocol`） |
| `client_key` | string | 是 | 该 user model 的调用方密钥 |
| `request_id` | string | 建议 | 调用方请求标识，原样回显并进日志，用于链路对账 |
| `tried_ids` | []string | 否 | 本次会话已尝试过的目标 `model_id`，这些目标会被排除，供数据面换目标重试 |
| `est_tokens` | int | 否 | 请求预估 token 数，只作为策略脚本输入，不做硬过滤 |

```json
{
  "model": "demo-pool",
  "inbound_protocol": "anthropic",
  "client_key": "sk-client-1",
  "request_id": "req-20260916-001",
  "tried_ids": [],
  "est_tokens": 8192
}
```

**响应** `200`：

```json
{
  "request_id": "req-20260916-001",
  "target": {
    "model_id": "kimi-k2-turbo",
    "account": "kimi-1",
    "provider_id": "kimi",
    "protocol": "anthropic",
    "base_url": "https://api.moonshot.cn/coding",
    "native_model": "kimi-k2-turbo",
    "context_window": 256000,
    "headers": {
      "x-api-key": "<真实上游密钥>",
      "anthropic-version": "2023-06-01"
    },
    "defaults": {"temperature": 0.6, "max_tokens": 4096},
    "overrides": {}
  },
  "decision": {
    "policy": "failover",
    "policy_version": 2,
    "collection": "demo",
    "group": "primary",
    "group_type": "fast",
    "note": "group-by-group failover",
    "candidates": ["kimi-k2-turbo", "ds-1/v4", "ds-1/v4-thinking"],
    "skipped": []
  }
}
```

**`target` 对象**（数据面可直接据此发上游请求）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `model_id` | string | 目标标识（可能含斜杠，如 `ds-1/v4`），结果上报时引用 |
| `account` | string | 账号名 |
| `provider_id` | string | 上游 provider 标识 |
| `protocol` | string | 出站协议（由 upstream 目录决定） |
| `base_url` | string | 上游基地址 |
| `native_model` | string | 传给上游的真实模型名（与 `model_id` 可能不同名） |
| `context_window` | int | 上下文窗口（token），目录未声明时缺省 |
| `headers` | object | **真实认证头**，具体头由上游协议决定（如 anthropic 为 `x-api-key` + `anthropic-version`）。原样并入上游请求；不要落日志 |
| `defaults` | object | 参数默认层：请求体里**缺失**的字段才填入 |
| `overrides` | object | 参数强制层：无论请求体是否携带一律压盖 |

`defaults` 与 `overrides` 是两层**未合并**的参数，本服务只搬运不解释；由数据面在编码出上游请求体之后按"defaults 补缺 → overrides 压盖"的顺序作用上去。两层恒为 JSON 对象：目录未配置时序列化为 `{}`，不会缺省。

**`decision` 对象**（决策溯源，`policy` 为空表示走了兜底顺序）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `policy` | string | 执行的策略名；空 = 兜底顺序 |
| `policy_version` | int | 执行时的策略版本号 |
| `collection` | string | 本次调度读的集合 |
| `group` | string | 命中目标所属的组 |
| `group_type` | string | 该组的类型（自由文本，语义由策略约定） |
| `note` | string | 策略脚本返回的说明 |
| `candidates` | []string | 过滤后仍然有效的候选，按优先顺序 |
| `skipped` | []Skip | 每个被丢弃候选的原因，见[跳过原因](#跳过原因) |

**失败语义**：候选全部被过滤或全部解析失败时返回 `503` + `target_unavailable`（`retryable: true`），`message` 汇总原因。这是"换目标稍后重试可能成功"的信号；策略脚本自身出错则是 `policy_error`（`retryable: false`，重试必然再失败）。

### 4. 结果上报

```http
POST /internal/v1/results
```

数据面回报一次调用的结果，驱动用量统计与冷却。按 `report_id` **幂等**：同一 `report_id` 重复上报返回 `{"applied": false}`，不重复计数。

**请求体**：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `report_id` | string | 是 | 幂等键，调用方生成并保证唯一 |
| `request_id` | string | 是 | 对应 dispatch 的请求标识 |
| `model_id` | string | 是 | 实际使用的目标 `model_id` |
| `outcome` | string | 是 | 结果类别，见下表 |
| `usage` | object | 否 | 本次用量 |

```json
{
  "report_id": "rep-20260916-001",
  "request_id": "req-20260916-001",
  "model_id": "kimi-k2-turbo",
  "outcome": "normal",
  "usage": {"input_tokens": 1200, "output_tokens": 340, "cache_read_tokens": 0}
}
```

**`outcome` 取值**：

| 取值 | 对运行态的作用 |
| --- | --- |
| `normal` | 累加用量；失败计数清零；解除冷却 |
| `abnormal` | 累加用量；失败计数 +1 |
| `retrying` | 调用方换目标重试；同 `abnormal` 计失败 |
| `invalid_model` | 目标模型不可用；同 `abnormal` 计失败 |
| `context_exceeded` | 上下文超限，是请求的问题不是目标的问题；**零变更** |

连续失败达到阈值（默认 3，可配）的目标进入冷却，冷却期内被调度过滤跳过。`normal` 上报会立刻解除冷却。

**响应** `200`：

```json
{"applied": true}
```

---

## 管理面 API

面向运维与 [frontend/](../frontend/) 管理界面。全部需要 `X-Admin-Key`。

约定：

- **列表端点解包装**：`GET /admin/collections` 返回 `{"collections": [...]}`；**单体端点裸返实体**：`GET /admin/collections/demo` 直接返回集合对象。创建成功返回 `201` + 实体；更新类 `PUT` 语义分为两种（返回实体或 `204`），各端点分别注明。
- user model 的 `client_key` 在任何读响应中都**不出现**（脱敏由类型定义保证，非运行时过滤）。
- URL 路径中含斜杠的名字（如 model_id `ds-1/v4`）需逐段编码。

### Collection

集合是配置的顶层容器：`名称 + 备注`，下挂有序的 Group。

#### 列出集合

```http
GET /admin/collections
```

**响应** `200`：

```json
{
  "collections": [
    {"name": "demo", "note": "演示集合：主备两组", "created_at": "...", "updated_at": "..."}
  ]
}
```

#### 创建集合

```http
POST /admin/collections
```

```json
{"name": "demo", "note": "演示集合：主备两组"}
```

**响应** `201` + 集合实体。重名返回 `409 conflict`。

#### 查询单个集合

```http
GET /admin/collections/{name}
```

**响应** `200` + 集合实体（裸返，无包装）。不存在返回 `404`。

#### 修改备注

```http
PUT /admin/collections/{name}
```

```json
{"note": "新备注"}
```

只更新备注（`name` 字段被忽略）。**响应** `204`。不存在返回 `404`。

#### 删除集合

```http
DELETE /admin/collections/{name}
```

级联删除其下全部 Group。被 user model 引用的集合删不掉，返回 `409` + 引用者清单。**响应** `204`。

### Group 与成员

Group 是集合内的有序编组，`position` 决定顺序（0 起，升序）。`type` 是**自由文本**（如 `fast` / `cheap`），语义由策略脚本约定，服务不解释。`config` 是给策略的组级参数（JSON object），缺省与 `{}` 是两种语义。

#### 列出组

```http
GET /admin/collections/{name}/groups
```

**响应** `200`（成员以引用字符串返回）：

```json
{
  "groups": [
    {
      "collection": "demo",
      "name": "primary",
      "type": "fast",
      "position": 0,
      "config": {"weight": 3},
      "members": ["kimi-k2-turbo", "ds-1/v4"]
    }
  ]
}
```

#### 创建组

```http
POST /admin/collections/{name}/groups
```

```json
{"name": "primary", "type": "fast", "position": 0, "config": {"weight": 3}}
```

**响应** `201` + 组实体。同集合内重名返回 `409`。

#### 更新组

```http
PUT /admin/collections/{name}/groups/{group}
```

请求体同创建（`name` 字段被忽略，以路径为准）。整体替换 `type` / `position` / `config`。**响应** `204`。

#### 删除组

```http
DELETE /admin/collections/{name}/groups/{group}
```

**响应** `204`。

#### 整组替换成员

```http
PUT /admin/collections/{name}/groups/{group}/members
```

```json
{"members": ["kimi-k2-turbo", "ds-1/v4"]}
```

**整体替换**语义：提交的列表即最终列表（顺序即成员顺序）。不校验引用是否存在 —— 允许先挂引用再建上游模型；无效引用在快照里标 `known: false` 且调度时被跳过。**响应** `204`。

#### 集合快照

```http
GET /admin/collections/{name}/snapshot
```

成员叠加 upstream 目录属性后的视图。`groups` 端点只返回引用字符串，无从判断引用是否仍然有效 —— 这个端点回答该问题。

**响应** `200`：

```json
{
  "name": "demo",
  "groups": [
    {
      "name": "primary",
      "type": "fast",
      "position": 0,
      "config": {"weight": 3},
      "members": [
        {
          "model_id": "kimi-k2-turbo",
          "account": "kimi-1",
          "provider_id": "kimi",
          "protocol": "anthropic",
          "native_model": "kimi-k2-turbo",
          "context_window": 256000,
          "enabled": true,
          "known": true,
          "position": 0
        }
      ]
    }
  ]
}
```

| 成员字段 | 说明 |
| --- | --- |
| `model_id` | 成员引用 |
| `known` | `false` 表示该引用在 upstream 目录中已消失（账号或模型被删） |
| `enabled` | 目录侧的启用状态；`false` 是正常运维状态，与 `known: false`（配置错误）语义不同 |
| `position` | 组内顺序 |

`groups` 与 `members` 恒为数组（空为 `[]`），不会是 `null`。

### 策略

策略以 Lua / JavaScript / TypeScript 脚本编写，保存时即编译，`version` 在 `source` 或 `language` 变化时递增（改备注不动版本）。

#### 列出策略

```http
GET /admin/policies
```

**响应** `200`：`{"policies": [<Policy>]}`，形态同单体。

#### 创建策略

```http
POST /admin/policies
```

```json
{
  "name": "failover",
  "language": "lua",
  "note": "group-by-group",
  "source": "local out = {}\n..."
}
```

**响应** `201` + 策略实体（含编译后的 `version`）。**编译失败返回 `400`**，`message` 带行列号，源码不入库。

#### 查询策略

```http
GET /admin/policies/{name}
```

**响应** `200`：

```json
{
  "name": "failover",
  "language": "lua",
  "source": "local out = {}...",
  "version": 2,
  "note": "group-by-group",
  "created_at": "...",
  "updated_at": "..."
}
```

#### 更新策略

```http
PUT /admin/policies/{name}
```

请求体同创建（`name` 以路径为准）。**响应** `200` + 更新后的策略实体（版本可能递增）。编译失败同样 `400` 且**不落任何变更**。

#### 删除策略

```http
DELETE /admin/policies/{name}
```

被 user model 绑定的策略删不掉，返回 `409` 并在 `message` 列出全部引用者。**响应** `204`。

#### 试运行

```http
POST /admin/policies/{name}/dry-run
```

用给定输入跑一次策略：读**真实**集合快照，但运行态由调用方给定，不碰真实运行态、不写缓存、不解析目标 —— 因此不影响任何真实请求。

**请求体**：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `collection` | string | 是 | 快照来源集合 |
| `request` | object | 是 | 请求上下文，形态同 dispatch 请求（`user_model` / `inbound_protocol` / `est_tokens` / `tried_ids` / `request_id`） |
| `runtime` | map | 否 | 模拟运行态，键为 `model_id`；缺省为空（全部不冷却、零用量） |

```json
{
  "collection": "demo",
  "request": {"user_model": "demo-pool", "inbound_protocol": "anthropic", "est_tokens": 8192, "tried_ids": [], "request_id": "dry-run-1"},
  "runtime": {
    "ds-1/v4": {"cooling": true, "cooling_until": 1789550000, "consecutive_failures": 3, "input_tokens": 0, "output_tokens": 0, "request_count": 5}
  }
}
```

**响应** `200`：

```json
{
  "policy": "failover",
  "policy_version": 2,
  "decision": {
    "candidates": ["kimi-k2-turbo", "ds-1/v4", "ds-1/v4-thinking"],
    "note": "group-by-group failover"
  }
}
```

`decision.candidates` 是脚本原始输出，**未经调度过滤**（不做已尝试/冷却/启用过滤）——试运行验证的是脚本逻辑，不是调度结果。

### User Model

User model 是对外暴露的模型名：调用方拿 `name` + `client_key` 走调度面。

#### 列出

```http
GET /admin/user-models
```

**响应** `200`：`{"user_models": [...]}`。**任何读响应都不含 `client_key`**。

#### 创建

```http
POST /admin/user-models
```

```json
{
  "name": "demo-pool",
  "collection": "demo",
  "policy": "failover",
  "client_key": "sk-client-1",
  "protocol": "anthropic",
  "enabled": true
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` / `collection` / `client_key` | 是 | 密钥只在此处（及更新处）出现 |
| `policy` | 否 | 空 = 兜底顺序 |
| `protocol` | 否 | 空 = 不限入站协议 |
| `enabled` | 是 | 禁用后调度面立即拒绝（`403 disabled`） |

**响应** `201` + 实体（无密钥）。引用的 collection / policy 不存在返回 `400`（`field` 指明）。

#### 查询

```http
GET /admin/user-models/{name}
```

**响应** `200` + 实体（无密钥）。

#### 更新

```http
PUT /admin/user-models/{name}
```

请求体同创建。**整体替换**语义，无"空则保留"：`client_key` 留空会提交空密钥，导致该模型名无法通过鉴权 —— 要保留原密钥必须重新填入。**响应** `200` + 实体。

#### 删除

```http
DELETE /admin/user-models/{name}
```

**响应** `204`。

### 运行态

调度运行态（冷却 / 失败计数 / 用量）是**只读 + 重置**的：数据只经 `/internal/v1/results` 变更。

#### 列出运行态

```http
GET /admin/runtime
```

**响应** `200`：

```json
{
  "runtime": [
    {
      "model_id": "ds-1/v4",
      "cooling": false,
      "consecutive_failures": 3,
      "usage": {"input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "request_count": 5},
      "updated_at": "..."
    }
  ]
}
```

`cooling_until` 仅在冷却中时出现（RFC 3339）。判断冷却与否始终以服务端 `cooling` 字段为准，客户端时钟不参与。

#### 重置运行态

```http
DELETE /admin/runtime/{model_id}
```

`model_id` 形如 `ds-1/v4` **含斜杠**，直接作为多段路径传递（无需编码成 `%2F`）。清零冷却与失败计数，**保留用量**（审计数据）。**响应** `204`。

---

## 附录

### 错误码表

| code | HTTP | retryable | 含义 |
| --- | --- | --- | --- |
| `unauthorized` | 401 | 否 | 密钥错误或缺失；调度面亦用于 client_key 不匹配 |
| `not_found` | 404 | 否 | 资源不存在 |
| `invalid_request` | 400 | 否 | 请求校验失败；`field` 指明出错字段 |
| `conflict` | 409 | 否 | 重名冲突，或删除被引用资源（`message` 列出引用者） |
| `disabled` | 403 | 否 | user model 被禁用 |
| `target_unavailable` | 503 | **是** | 无可用目标：候选全被过滤 / 全部解析失败 |
| `policy_error` | 503 | 否 | 策略脚本运行出错（脚本 bug，重试必然再失败） |
| `policy_timeout` | 503 | **是** | 策略执行超时 |
| `internal_error` | 500 | **是** | 其余一切；底层细节不外泄 |

`retryable: true` 的三码是数据面重试决策的依据；其余重试无意义。

### 跳过原因

dispatch 响应 `decision.skipped[].reason` 的取值，按过滤顺序：

| reason | 含义 |
| --- | --- |
| `out_of_collection` | 引用不属于本集合（策略返回了越界目标） |
| `already_tried` | 在请求的 `tried_ids` 里 |
| `unknown_model` | 引用在 upstream 目录中已消失 |
| `disabled` | 目录中该模型未启用 |
| `cooling` | 目标处于冷却期 |
| `resolve_failed` | 向 upstream 解析目标失败，`detail` 带原因 |

### 策略脚本输入

脚本收到的唯一入参 `input`（JSON 形态，由服务注入）：

```json
{
  "request": {
    "user_model": "demo-pool",
    "inbound_protocol": "anthropic",
    "est_tokens": 8192,
    "tried_ids": [],
    "request_id": "req-1"
  },
  "collection": { "name": "demo", "groups": [ /* 同快照形态 */ ] },
  "runtime": {
    "kimi-k2-turbo": {"cooling": false, "consecutive_failures": 0, "input_tokens": 1200, "output_tokens": 340, "request_count": 1}
  }
}
```

- `input` 结构里**没有任何凭据字段** —— "策略读不到凭据"由类型定义保证，而非运行时过滤。
- `runtime` 与 `request.tried_ids` 恒为容器（空为 `{}` / `[]`），不会是 `null`。
- 脚本 `return { candidates = {...}, note = "..." }`（Lua）或 `return { candidates: [...], note: "..." }`（JS/TS）。`candidates` 是 `model_id` 有序数组，`note` 可选。
- 返回的候选仍会经调度过滤（已尝试/目录消失/禁用/冷却），脚本写错也不会把流量打到坏目标上。
- 策略输入速查也内置于管理界面（frontend/ 策略编辑页侧栏）。

### 冷却参数

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `MSR_COOLDOWN_THRESHOLD` | `3` | 连续失败达到该值进入冷却；`<=0` 从不冷却 |
| `MSR_COOLDOWN_DURATION` | `1m` | 冷却时长 |

其余环境变量（`MSR_PG_DSN` / `MSR_REDIS_ADDR` / `MSR_DISPATCH_KEY` / `MSR_ADMIN_KEY` / `MSR_UPSTREAM_BASE_URL` / `MSR_UPSTREAM_DELIVERY_KEY` 等）见根 [README.md](../README.md)。
