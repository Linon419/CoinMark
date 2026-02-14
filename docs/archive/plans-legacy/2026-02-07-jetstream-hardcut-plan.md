# JetStream Hard Cut Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 一次性把链路从 `Redpanda(Kafka)` 硬切到 `NATS JetStream`，让 `collector-go` 只发 JetStream，`ingest` 只读 JetStream。

**Architecture:** 保留现有 `collector-go -> ingest -> DB/API` 业务边界，只替换中间消息总线与读写客户端实现。`topic` 改为 `subject + stream`，消费端用 durable consumer，按 `market` 字段做分流。

**Tech Stack:** NATS JetStream、Go (`nats.go`)、Python (`nats-py`)、Docker Compose。

---

## 范围与硬切原则

- 本次是**一步硬切**，不保留运行时 Kafka 回退开关。
- 代码里删除 Kafka 生产/消费主路径，配置项统一改为 NATS。
- Compose 中移除 `redpanda` 服务，新增 `nats`（开启 JetStream）。
- 允许短暂停机切换（维护窗口），不做双写双读。

### 新消息约定（MVP）

- Stream：`COINMARK_RAW`
- Subjects：
  - `coinmark.raw.trade`
  - `coinmark.raw.depth`
- 消息体继续沿用当前 JSON 字段（`market/symbol/event_time_ms/...`），避免下游聚合逻辑重写。

---

### Task 1: 引入 JetStream 基础设施（Compose 层）

**Files:**
- Modify: `infra/docker-compose.yml`
- Modify: `.env.example`
- Modify: `README.md`

**Step 1: 替换中间件服务定义**

- 删除 `redpanda` 服务段。
- 新增 `nats` 服务，示例命令：

```yaml
nats:
  image: nats:2.10-alpine
  command: ["-js", "-m", "8222"]
  ports:
    - "4222:4222"
    - "8222:8222"
```

**Step 2: 调整依赖关系**

- `collector-go` 与 `ingest` 的 `depends_on` 从 `redpanda` 改为 `nats`。

**Step 3: 更新环境变量模板**

- 在 `.env.example` 增加（或替换）:

```env
NATS_URL=nats://nats:4222
NATS_STREAM_RAW=COINMARK_RAW
NATS_SUBJECT_TRADE=coinmark.raw.trade
NATS_SUBJECT_DEPTH=coinmark.raw.depth
```

**Step 4: 验证 compose 配置**

Run: `docker compose -f infra/docker-compose.yml config`

Expected: 输出成功，无 `redpanda` 相关依赖错误。

**Step 5: Commit**

```bash
git add infra/docker-compose.yml .env.example README.md
git commit -m "infra: replace redpanda with nats jetstream"
```

---

### Task 2: collector-go 改为 JetStream 发布

**Files:**
- Create: `apps/collector-go/internal/nats/publisher.go`
- Modify: `apps/collector-go/internal/config/config.go`
- Modify: `apps/collector-go/internal/collector/collector.go`
- Modify: `apps/collector-go/go.mod`
- Delete: `apps/collector-go/internal/kafka/producer.go`

**Step 1: 写配置测试（先红）**

Create: `apps/collector-go/internal/config/config_test.go`

测试点：
- `COLLECTOR_NATS_URL` 为空时使用默认值
- `COLLECTOR_NATS_STREAM_RAW`、`COLLECTOR_NATS_SUBJECT_*` 生效

Run: `go test ./internal/config -v`

Expected: FAIL（配置字段尚未实现）。

**Step 2: 最小实现 NATS 配置字段**

在 `Config` 中新增：

```go
NATSURL        string
NATSStreamRaw  string
NATSSubjectTrade string
NATSSubjectDepth string
```

并在 `Load()` 完成默认值和非空校验。

**Step 3: 实现 JetStream Publisher**

`internal/nats/publisher.go` 提供：

```go
type Publisher struct { ... }
func NewPublisher(url, stream, subject string) (*Publisher, error)
func (p *Publisher) Send(ctx context.Context, payload []byte) error
func (p *Publisher) Close() error
```

要求：
- 初始化时确保 stream 存在（不存在就创建）
- `Send` 使用 `js.Publish`（带 timeout）

**Step 4: collector 主逻辑切换为 NATS**

- 把 `tradeProducer/depthProducer` 从 Kafka 类型替换为 NATS Publisher。
- 发送时不再传 key，仅发 JSON payload。
- 统计指标逻辑保留。

**Step 5: 跑 collector-go 测试**

Run: `go test ./...`

Expected: PASS。

**Step 6: Commit**

```bash
git add apps/collector-go
git commit -m "feat(collector-go): publish trade/depth to nats jetstream"
```

---

### Task 3: ingest 改为 JetStream 消费（硬切）

**Files:**
- Modify: `apps/api/coinmark_api/config.py`
- Modify: `apps/api/coinmark_api/ingest/run.py`
- Modify: `apps/api/requirements.txt`
- Modify: `apps/api/README.md`

**Step 1: 写失败测试（配置层）**

Create: `apps/api/tests/test_ingest_nats_config.py`

测试点：
- 默认 source 为 `nats`
- `NATS_*` 配置能读到

Run: `pytest apps/api/tests/test_ingest_nats_config.py -v`

Expected: FAIL（配置未改）。

**Step 2: 配置改造为 NATS 默认**

- 新增配置：
  - `ingest_nats_url`
  - `ingest_nats_stream_raw`
  - `ingest_nats_subject_trade`
  - `ingest_nats_subject_depth`
  - `ingest_nats_consumer_prefix`
- `ingest_trade_source_*` 与 `ingest_depth_source_*` 默认值改为 `nats`。

**Step 3: run.py 新增 NATS 消费循环**

- 新增 `_consume_trade_from_nats(...)`
- 新增 `_consume_depth_from_nats(...)`
- 逻辑对齐当前 Kafka 版本：解析 JSON -> 过滤 market -> 喂聚合器。

**Step 4: main() 硬切调度**

- `main()` 中 spot/swap trade/depth 只走 NATS 消费函数。
- 删除（或不再引用）Kafka 消费函数调用分支。

**Step 5: 安装依赖并跑 API 侧测试**

Run:
- `pip install -r apps/api/requirements.txt`
- `pytest apps/api/tests -q`

Expected: 新增与相关回归测试通过。

**Step 6: Commit**

```bash
git add apps/api
git commit -m "feat(ingest): consume trade/depth from nats jetstream"
```

---

### Task 4: 删除 Kafka 遗留与文档收口

**Files:**
- Modify: `infra/docker-compose.yml`
- Modify: `.env.example`
- Modify: `README.md`
- Modify: `apps/collector-go/README.md`
- Modify: `docs/plans/2026-02-07-full-market-architecture-v1.md`

**Step 1: 清理文档中的 Kafka/Redpanda 默认描述**

- 全量替换“默认 Kafka/Redpanda”为“默认 NATS JetStream”。
- 补充 JetStream stream/subject 对应关系。

**Step 2: 清理无用配置项**

- 去掉 compose 和 `.env.example` 中未再使用的 Kafka 变量。

**Step 3: 全仓 grep 验证残留**

Run: `rg -n "redpanda|kafka" apps infra README.md .env.example`

Expected: 只剩“迁移说明或历史文档”中的可接受引用。

**Step 4: Commit**

```bash
git add infra/docker-compose.yml .env.example README.md apps/collector-go/README.md docs/plans/2026-02-07-full-market-architecture-v1.md
git commit -m "docs: hard cut architecture to nats jetstream"
```

---

### Task 5: 端到端联调与验收

**Files:**
- Modify (if needed): `scripts/collector-poc-check.ps1`
- Create (optional): `scripts/nats-smoke.ps1`

**Step 1: 启动最小链路**

Run:

```bash
docker compose -f infra/docker-compose.yml up -d --build nats collector-go collector-go-spot ingest api
```

Expected: 容器全部 healthy/running。

**Step 2: 检查 JetStream 运行状态**

Run: `curl http://localhost:8222/healthz`

Expected: 返回 `ok`。

**Step 3: 检查采集与消费日志**

Run:
- `docker compose -f infra/docker-compose.yml logs --tail=200 collector-go`
- `docker compose -f infra/docker-compose.yml logs --tail=200 ingest`

Expected:
- collector-go 持续输出 `trade/depth msg` 递增。
- ingest 持续输出 `kafka_*` 替换后的 `nats_*` 计数递增。

**Step 4: API 验证**

Run: `curl http://localhost:8000/healthz`

Expected: HTTP 200。

**Step 5: 验收门槛**

- 30 分钟内无持续重连风暴。
- ingest flush 正常，无持续异常堆积。
- 关键榜单 API 可返回非空数据。

**Step 6: Commit**

```bash
git add scripts
git commit -m "chore: add nats smoke checks for hard cut"
```

---

## 风险与控制

- 风险 1：JetStream subject/stream 配错导致“采集成功但 ingest 无消费”。
  - 控制：启动阶段强校验 stream 和 subject，失败即退出。
- 风险 2：一次性硬切没有实时回退通道。
  - 控制：保留切换前 commit/tag，故障时 `git revert` + 重启服务。
- 风险 3：消费组 durable 名冲突导致重复或漏消费。
  - 控制：consumer 名统一 `coinmark-{market}-{trade|depth}` 前缀。

## 完成定义（DoD）

- Compose 不再包含 Redpanda 服务。
- `collector-go` 无 Kafka 依赖，`go test ./...` 通过。
- `ingest` 默认并实际使用 NATS 消费，相关测试通过。
- README 与 `.env.example` 与运行方式一致。
- 端到端 smoke 通过并有日志证据。

