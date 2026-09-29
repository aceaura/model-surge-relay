# model-surge-relay

调度服务：把一个对外的 user model 名字，按集合上的策略组合（优先级链 + 超长压缩托管）解析成一个具体的 upstream 目标（含凭据），交给数据面去发请求。

## 三向对接关系

```
model-surge-stream ──POST /v1/dispatch──▶ model-surge-relay ──POST /v1/resolve──▶ model-surge-upstream
       (数据面)      ◀─── target + decision ───       (本服务)        ◀── target+headers ──   (静态配置中心)
                    ──POST /v1/results──▶
```

- **model-surge-upstream** 是静态配置中心，只回答"某个 upstream model 长什么样"并下发凭据。本服务只读它，不写、不代理其数据面。
- **model-surge-relay** 持有 Collection / Group / 成员三层配置、挂在 Collection 上的策略组合、user model 绑定，以及**自己的运行态**（冷却、连续失败、用量）。运行态放在本服务是因为 upstream 是静态的、不可写。
- **model-surge-stream** 是数据面：拿 `dispatch` 返回的 target 直接发上游请求，请求结束后回报 `results`。

一次请求的路径：鉴权 user model → 取 Collection 快照 → 取运行态 → 组合器展开候选（**每次请求恰好一次**，不重试）→ 逐个 resolve 直到成功 → 返回 target + 决策溯源。

组合器只负责**展开有序候选**，永不解析目标。取凭据发生在组合器返回之后，凭据结构上无法进入决策逻辑。

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `MSR_PG_DSN` | 是 | | PostgreSQL DSN，权威存储 |
| `MSR_DISPATCH_KEY` | 是 | | `/v1/*` 的 Bearer 密钥 |
| `MSR_ADMIN_KEY` | 是 | | `/admin/*` 的 Bearer 密钥，必须与 dispatch key 不同 |
| `MSR_UPSTREAM_BASE_URL` | 是 | | model-surge-upstream 下发面地址 |
| `MSR_UPSTREAM_DELIVERY_KEY` | 是 | | 下发面 Bearer 密钥 |
| `MSR_REDIS_ADDR` | 否 | 空 | 留空则不启用缓存，全程直读 PG |
| `MSR_REDIS_PASSWORD` | 否 | 空 | |
| `MSR_REDIS_DB` | 否 | `0` | |
| `MSR_CACHE_TTL` | 否 | `1m` | 缓存兜底过期时间 |
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

## 健康检查

`GET /healthz`，**免鉴权**。响应 `{ready, database, cache, upstream}` 全为 bool；`ready` 恒等于 `database`（PG 是权威存储），缓存与上游不可用只降级标注，仍 200。PG 不可达返回 503。

## 调度面 API

完整 API 参考（含全部字段表、错误码表与请求/响应示例）见 [docs/api.md](docs/api.md)，本节为速览。

密钥：`Authorization: Bearer $MSR_DISPATCH_KEY`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/models` | 可用 user model 列表，不含任何密钥 |
| POST | `/v1/dispatch` | 选目标，返回 target（含认证头）+ decision |
| POST | `/v1/results` | 回报结果，按 `report_id` 幂等 |

`dispatch` 请求体：`model` / `inbound_protocol` / `client_key` / `request_id` / `tried_ids` / `est_tokens`。`tried_ids` 里的目标会被排除，供数据面自行重试换目标。

`results` 的 `outcome` 取 `normal` / `abnormal` / `retrying` / `invalid_model` / `context_exceeded`。前三种计入用量与失败计数；`context_exceeded` 不产生任何运行态记录（超窗是请求的问题，不是目标的问题）。

## 管理面 API

密钥：`Authorization: Bearer $MSR_ADMIN_KEY`。

- Collection：`GET|POST /admin/collections`、`GET|PUT|DELETE /admin/collections/{name}`
- Group：`GET|POST /admin/collections/{name}/groups`、`PUT|DELETE /admin/collections/{name}/groups/{group}`
- 成员：`PUT /admin/collections/{name}/groups/{group}/members`（整组替换）
- 策略组合：`GET|PUT /admin/collections/{name}/strategy`
- 试运行：`POST /admin/collections/{name}/strategy/dry-run`
- 脚本策略迁移：`POST /admin/migrate-policies`（一次性，可重复执行）
- User model：`GET|POST /admin/user-models`、`GET|PUT|DELETE /admin/user-models/{name}`
- 运行态：`GET /admin/runtime`、`DELETE /admin/runtime/{model_id...}`

几条约束：

- Group 的 `type` 与 `name` 是数据库里的自由文本，不是代码枚举；服务本身不解释。
- 策略组合挂在 Collection 上：`priority_chain` 是有序组链（空 = 按组 position 顺序），`overflow` 是超长压缩托管（开关 + 触发阈值 + 压缩组列表）。链与压缩组引用的组必须存在，写入即校验。
- 被策略组合引用的组删不掉，返回 409 并指出引用位置（`priority_chain` / `compact_groups`）。
- `dry-run` 读真实 Collection 快照与真实运行态，与调度共用同一个组合器核——看到的候选序列就是上线后的序列；它不解析目标、不写运行态。
- `DELETE /admin/runtime/{model_id...}` 用多段通配，因为 model_id 形如 `kimi-1/k3`，含斜杠。Reset 清冷却与失败计数，但**保留用量**（那是审计数据）。

## 管理面 UI

`frontend/` 是配套的 Flutter Windows 桌面管理面，直接调用上面的管理面 API。

```bash
cd frontend
flutter run -d windows
```

首次启动填服务地址与 `MSR_ADMIN_KEY`，之后记在本机。三个页面分别管：集合（组成员编排 + 策略组合 + 试运行）、对外模型名、运行态。

细节与几处反直觉的语义见 `frontend/README.md`。

## 策略组合

策略不是脚本，而是挂在 Collection 上的两组配置，由内置组合器展开成候选序列：

- **优先级链**（`priority_chain`）：有序组链。按链逐组展开候选；组内成员全部不可用（已试过 / 目录消失 / 未启用 / 冷却中，含限额冷却）时整组跳过并记录 `group_skips`，自动落到下一组。链为空 = 按组的 position 顺序展开。限额不做主动配额账本：上游限流经结果上报转化为冷却，被动参与"整组不可用"判定。
- **超长压缩托管**（`overflow`）：开关 + 触发阈值（`threshold_tokens`）+ 压缩组列表（`compact_groups`）。请求的 `est_tokens` 达到阈值时，先展开压缩组作为 `compact` 段（数据面对其发压缩请求），再展开链作为 `resume` 段（压缩完成后回落继续原任务）；未触发时只有 `standard` 段。

候选是**阶段化**的：`decision.candidates[]` 每项带 `phase`（`standard` / `compact` / `resume`），数据面按序执行、不问语义。决策溯源含 `collection_updated_at`（做出决策的配置版本）、`group_skips`（整组跳过）与 `skipped`（成员粒度跳过及原因）。

试运行（`POST /admin/collections/{name}/strategy/dry-run`）与真实调度共用同一个组合器核：输入 `est_tokens` 与 `tried_ids`，输出同形态的 Decision，不解析目标、不写运行态。

### 从脚本策略迁移

旧版 Lua/JS 脚本策略已整体下线。`POST /admin/migrate-policies` 执行一次性迁移：按 user model 的绑定关系把具名脚本映射为集合上的组合配置（`failover`/`sticky`/`preset`/`round_robin`/`least_used` → 优先级链按组顺序；`compact_overflow` → 主池链 + 压缩托管，阈值取组 `config.compact_above_tokens`、其次主池成员最大 `context_window`）。同一集合被多个脚本引用时取多数，冲突与无法映射的脚本进报告人工跟进。迁移幂等，可重复执行；`policies` 表与 `user_models.policy` 列物理保留供反查，确认无遗留后人工 DROP。

## 测试

```bash
cd backend
TEST_PG_DSN="postgres://msr:msr@127.0.0.1:15432/msr?sslmode=disable" \
TEST_REDIS_ADDR=127.0.0.1:16379 \
go test -count=1 -p 1 ./...
```

- 触库的包共用同一个测试库并互相 truncate，所以必须 `-p 1`，并行会互相清表。
- `TEST_PG_DSN` 未设时相关测试跳过；`TEST_REDIS_ADDR` 可选，设了才跑缓存路径。缓存路径值得跑一遍——缓存形态与对外形态不一致的 bug 只在命中缓存时暴露。
