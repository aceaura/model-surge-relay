# model-surge-relay

调度服务：把一个对外的 user model 名字，按可编程策略解析成一个具体的 upstream 目标（含凭据），交给数据面去发请求。

## 三向对接关系

```
model-surge-stream ──POST /internal/v1/dispatch──▶ model-surge-relay ──POST /v1/resolve──▶ model-surge-upstream
       (数据面)      ◀──── target + decision ─────       (本服务)        ◀── target+headers ──   (静态配置中心)
                    ──POST /internal/v1/results─▶
```

- **model-surge-upstream** 是静态配置中心，只回答"某个 upstream model 长什么样"并下发凭据。本服务只读它，不写、不代理其数据面。
- **model-surge-relay** 持有 Collection / Group / 成员三层配置、具名动态策略、user model 绑定，以及**自己的运行态**（冷却、连续失败、用量）。运行态放在本服务是因为 upstream 是静态的、不可写。
- **model-surge-stream** 是数据面：拿 `dispatch` 返回的 target 直接发上游请求，请求结束后回报 `results`。

一次请求的路径：鉴权 user model → 取 Collection 快照 → 取运行态 → 跑策略（**每次请求恰好一次**，不重试）→ 服务端过滤候选 → 逐个 resolve 直到成功 → 返回 target + 决策溯源。

策略只负责**排序**，永不解析目标。取凭据发生在脚本返回之后，因此凭据结构上无法进入脚本运行时；`policy.Input` 里没有任何凭据字段，这一点由一个反射测试守住。

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `MSR_PG_DSN` | 是 | | PostgreSQL DSN，权威存储 |
| `MSR_DISPATCH_KEY` | 是 | | `/internal/v1/*` 的 Bearer 密钥 |
| `MSR_ADMIN_KEY` | 是 | | `/admin/*` 的 `X-Admin-Key` 密钥，必须与 dispatch key 不同 |
| `MSR_UPSTREAM_BASE_URL` | 是 | | model-surge-upstream 下发面地址 |
| `MSR_UPSTREAM_DELIVERY_KEY` | 是 | | 下发面 Bearer 密钥 |
| `MSR_REDIS_ADDR` | 否 | 空 | 留空则不启用缓存，全程直读 PG |
| `MSR_REDIS_PASSWORD` | 否 | 空 | |
| `MSR_REDIS_DB` | 否 | `0` | |
| `MSR_CACHE_TTL` | 否 | `1m` | 缓存兜底过期时间 |
| `MSR_POLICY_TIMEOUT` | 否 | `200ms` | 单次策略执行超时，超时按可重试错误上报 |
| `MSR_COOLDOWN_THRESHOLD` | 否 | `3` | 连续失败达到该值进入冷却；`0` 表示永不冷却 |
| `MSR_COOLDOWN_DURATION` | 否 | `1m` | 冷却时长 |
| `MSR_LISTEN` | 否 | `:8080` | 监听地址 |

必填项缺失时启动失败，并一次列出全部缺失名称。

## 运行

```bash
export MSR_DISPATCH_KEY=... MSR_ADMIN_KEY=... \
       MSR_UPSTREAM_BASE_URL=http://host.docker.internal:8080 \
       MSR_UPSTREAM_DELIVERY_KEY=...
docker compose up -d --build
```

宿主端口取 5433 / 6380 / 8081，与 model-surge-upstream 的 5432 / 6379 / 8080 并存不冲突。

数据库 DDL 由 `//go:embed schema.sql` 在启动时幂等执行，没有迁移框架。

## 调度面 API

密钥：`Authorization: Bearer $MSR_DISPATCH_KEY`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/internal/v1/health` | PG 不可用返回 503 `unready`；缓存/上游不可用只降级标注，仍 200 |
| GET | `/internal/v1/models` | 可用 user model 列表，不含任何密钥 |
| POST | `/internal/v1/dispatch` | 选目标，返回 target（含认证头）+ decision |
| POST | `/internal/v1/results` | 回报结果，按 `report_id` 幂等 |

`dispatch` 请求体：`model` / `inbound_protocol` / `client_key` / `request_id` / `tried_ids` / `est_tokens`。`tried_ids` 里的目标会被排除，供数据面自行重试换目标。

`results` 的 `outcome` 取 `normal` / `abnormal` / `retrying` / `invalid_model` / `context_exceeded`。前三种计入用量与失败计数；`context_exceeded` 不产生任何运行态记录（超窗是请求的问题，不是目标的问题）。

## 管理面 API

密钥：`X-Admin-Key: $MSR_ADMIN_KEY`。

- Collection：`GET|POST /admin/collections`、`GET|PUT|DELETE /admin/collections/{name}`
- Group：`GET|POST /admin/collections/{name}/groups`、`PUT|DELETE /admin/collections/{name}/groups/{group}`
- 成员：`PUT /admin/collections/{name}/groups/{group}/members`（整组替换）
- 策略：`GET|POST /admin/policies`、`GET|PUT|DELETE /admin/policies/{name}`
- 试运行：`POST /admin/policies/{name}/dry-run`
- User model：`GET|POST /admin/user-models`、`GET|PUT|DELETE /admin/user-models/{name}`
- 运行态：`GET /admin/runtime`、`DELETE /admin/runtime/{model_id...}`

几条约束：

- Group 的 `type` 与 `name` 是数据库里的自由文本，不是代码枚举；策略脚本按业务语义读取，服务本身不解释。
- 策略保存时即编译，编译失败带行列号返回 400。`version` 只在 `source` 或 `language` 真的变化时递增，改备注不动版本。
- 被 user model 绑定的策略删不掉，返回 409 并列出全部引用者。
- `dry-run` 读真实 Collection 快照，但运行态由调用方给定，不碰真实运行态、不解析目标。
- `DELETE /admin/runtime/{model_id...}` 用多段通配，因为 model_id 形如 `kimi-1/k3`，含斜杠。Reset 清冷却与失败计数，但**保留用量**（那是审计数据）。

## 管理面 UI

`frontend/` 是配套的 Flutter Windows 桌面管理面，直接调用上面的管理面 API。

```bash
cd frontend
flutter run -d windows
```

首次启动填服务地址与 `MSR_ADMIN_KEY`，之后记在本机。四个页面分别管：集合与组成员编排、策略（含试运行）、对外模型名、运行态。

细节与几处反直觉的语义见 `frontend/README.md`。

## 策略编写

支持 `lua` / `javascript` / `typescript`。TypeScript 经 esbuild 转 ES2015 后与 JS 共用 goja 执行路径。脚本用 `return` 返回决策。

`policy/examples/` 里有五个可直接用的 Lua 范例：`preset`、`sticky`、`failover`、`round_robin`、`least_used`。

### 输入

全局变量 `input`（JS 里是函数参数 `input`）：

```jsonc
{
  "request": {
    "user_model": "sonnet-pool",
    "inbound_protocol": "anthropic",   // anthropic | chat_completions | responses | gemini
    "est_tokens": 12000,
    "tried_ids": [],                    // 本次请求已试过的目标，应当排除
    "request_id": "..."
  },
  "collection": {
    "name": "...",
    "groups": [                          // 已按 position 排好序
      {
        "name": "primary", "type": "...", "position": 0,
        "config": {},                    // 组自定义配置，服务不解释
        "members": [
          {
            "model_id": "kimi-1/k3", "account": "kimi-1", "provider_id": "...",
            "protocol": "anthropic", "native_model": "k3", "context_window": 262144,
            "enabled": true,
            "known": true,               // false = 该引用在 upstream 目录中已消失
            "position": 0
          }
        ]
      }
    ]
  },
  "runtime": {
    "kimi-1/k3": {
      "cooling": false, "cooling_until": 0,
      "consecutive_failures": 0,
      "input_tokens": 0, "output_tokens": 0, "request_count": 0
    }
  }
}
```

`runtime` 与 `tried_ids` 恒为容器而非 `null`，`groups` / `members` 同理，所以脚本可以直接索引和遍历。

### 输出

两种形态都接受：

```lua
return { "kimi-1/k3", "ark-2/doubao" }                        -- 纯候选数组
return { candidates = {...}, note = "为什么这么排" }            -- 带说明
```

`note` 会原样出现在 `decision.note` 里，便于线上溯源。

### 服务端过滤是兜底，不是替代

脚本返回后，服务端按固定优先级丢弃候选，并把每一条记进 `decision.skipped`：不在 Collection 内 → 已试过 → 目录中不存在 → 已禁用 → 正在冷却。也就是说脚本刻意返回一个冷却中的目标依然会被拒；这层是保险，正确的冷却判断仍应写在脚本里。

候选全被过滤时直接返回 `target_unavailable`，不会去 resolve。

### 沙箱

- 不装 `io` / `os` / `package` / `debug`；`base` 库里的 `load` / `loadfile` / `dofile` / `require` / `print` 也逐一摘掉。JS 侧不注入任何宿主对象。
- 没有文件、网络、进程环境与动态加载能力。
- 编译一次多次执行，每次执行新建解释器，执行之间不共享可变状态。
- 超时由 ctx 控制；Lua 另有 2000 万条指令上限，让无系统调用的死循环更快中断。
- panic 被 recover，映射为策略错误（不可重试）；超时映射为可重试。

## 测试

```bash
cd backend
TEST_PG_DSN="postgres://msr:msr@127.0.0.1:15432/msr?sslmode=disable" \
TEST_REDIS_ADDR=127.0.0.1:16379 \
go test -count=1 -p 1 ./...
```

- 触库的包共用同一个测试库并互相 truncate，所以必须 `-p 1`，并行会互相清表。
- `TEST_PG_DSN` 未设时相关测试跳过；`TEST_REDIS_ADDR` 可选，设了才跑缓存路径。缓存路径值得跑一遍——缓存形态与对外形态不一致的 bug 只在命中缓存时暴露。
