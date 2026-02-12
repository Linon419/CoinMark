# CoinMark API — Python → Go 重构方案

## 1. 背景与目标

### 当前状态
- **语言**：Python 3.10 / FastAPI / SQLAlchemy / asyncio
- **代码量**：~8,500 行（14+ 核心文件）
- **运行时资源**：内存 407MB（768MB 上限），CPU 闲时 0.17%
- **痛点**：Python 运行时基线内存高（~200MB）、GC 碎片无法归还 OS、asyncio 调度开销

### 重构目标
- 内存降至 **60–100MB**（参考 collector-go 8MB / ingest-go 17MB）
- 保持与 collector-go / ingest-go **一致的代码风格和项目结构**
- 功能 **100% 对等**，API 接口 / WebSocket 协议不变，前端零改动
- 模块路径：`coinmark/api-go`

---

## 2. 技术选型

| 领域 | 选型 | 理由 |
|------|------|------|
| **HTTP 框架** | **Gin** | 项目整体偏轻量，Gin 生态成熟、性能优异、社区最大；路由 + 中间件 + JSON 序列化开箱即用 |
| **WebSocket** | **gorilla/websocket** | 与现有 collector-go 一致（已用 gorilla），减少依赖碎片；Hub 场景下手动控制读写更灵活 |
| **SQLite** | **modernc.org/sqlite** + **jmoiron/sqlx** | 纯 Go 实现无 CGO，sqlx 的 NamedExec/StructScan 足够覆盖 upsert/query |
| **ClickHouse** | **clickhouse-go/v2** | 与 ingest-go 一致，原生协议 + batch insert |
| **Redis** | **redis/go-redis/v9** | 标准选型，支持 context |
| **NATS** | **nats-io/nats.go** | 与 collector-go / ingest-go 一致 |
| **Telegram** | **go-telegram/bot** | 轻量、context 原生、支持 long polling + webhook |
| **配置** | **环境变量 + struct** | 沿用现有 Go 项目模式（`mustString` / `mustInt` helper） |
| **日志** | **log/slog** | Go 1.22 标准库结构化日志，与现有项目 `log.Printf` 风格兼容，支持 key=value |
| **Decimal** | **shopspring/decimal** | 与 ingest-go 一致 |
| **JSON** | **bytedance/sonic** | 高性能 JSON 编解码，Gin 可直接集成 |
| **缓存** | **singleflight + sync.Map + TTL** | 轻量实现，覆盖 pairs/OI/funding 等缓存场景 |

---

## 3. 项目结构

```
apps/api-go/
├── cmd/
│   └── api/
│       └── main.go                  # 入口：启动 HTTP/WS + 后台任务
├── internal/
│   ├── config/
│   │   └── config.go                # 环境变量加载 + 校验（API_* 前缀）
│   ├── model/
│   │   └── model.go                 # 所有数据结构（对应 Python models.py）
│   ├── repo/
│   │   ├── ch/
│   │   │   └── ch.go                # ClickHouse 查询（对应 ch.py）
│   │   ├── sqlite/
│   │   │   ├── sqlite.go            # SQLite 连接 + 迁移
│   │   │   ├── anomaly.go           # anomaly_events CRUD
│   │   │   ├── absorption.go        # absorption_signal_snapshots CRUD
│   │   │   ├── heatmap.go           # orderbook_heatmap CRUD
│   │   │   ├── sr_level.go          # sr_levels CRUD
│   │   │   ├── favorite.go          # favorites CRUD
│   │   │   └── coin_info.go         # coin_info CRUD
│   │   └── redis/
│   │       └── redis.go             # Redis 缓存操作
│   ├── binance/
│   │   └── rest.go                  # Binance REST 客户端 + TTL 缓存
│   ├── service/
│   │   ├── anomaly.go               # 异常检测（breakout/volume/amplitude spike）
│   │   ├── absorption.go            # 吸筹信号（4h/1d/3d 窗口）
│   │   ├── signal_lab.go            # 信号实验室（persistent buy/single large/climax）
│   │   ├── depth_scan.go            # Depth fullscan + heatmap 生成
│   │   ├── market_supply.go         # 市值/流通量
│   │   ├── bot_funding.go           # 资金费率排行
│   │   ├── bot_ls_volume.go         # 多空量比排行
│   │   └── bot_oi_mcap.go           # OI/市值排行
│   ├── hub/
│   │   ├── manager.go               # WebSocket 连接管理 + 订阅过滤
│   │   ├── publisher.go             # 限速 + 去重广播
│   │   ├── event.go                 # HubEvent 定义
│   │   ├── anomaly_stream.go        # 轮询异常事件 → 广播
│   │   └── runtime.go               # 后台任务编排（heartbeat/cleanup/scan）
│   ├── handler/
│   │   ├── health.go                # GET /healthz
│   │   ├── symbol.go                # GET /api/symbol/getpairs
│   │   ├── aggregate.go             # /api/aggregate/* + /api/kline/*
│   │   ├── coin.go                  # /api/coin/* （最大模块）
│   │   ├── anomaly.go               # /api/anomaly/*
│   │   ├── user.go                  # /api/user/favorites
│   │   ├── bot.go                   # /api/bot/*
│   │   ├── signal_lab.go            # /api/signal-lab/*
│   │   └── hub.go                   # WS /api/hub/market
│   ├── telegram/
│   │   └── bot.go                   # Telegram 查询/通知 bot
│   ├── migration/
│   │   └── migrate.go               # SQLite schema 迁移
│   └── pkg/
│       ├── ttlcache.go              # 通用 TTL 缓存
│       └── mathutil.go              # 数学辅助（线性回归、ZigZag pivot 等）
├── go.mod
├── go.sum
└── Dockerfile
```

**设计原则**：
- 单一 `cmd/api/main.go` 入口（HTTP + WS + 后台任务 + Telegram 全在一个进程），与 Python 版本保持一致
- `internal/` 下按职责分层：`model` → `repo` → `service` → `handler` → `hub`
- 与 collector-go / ingest-go 保持一致的命名和模式

---

## 4. 模块对应关系

| Python 文件 | 行数 | Go 模块 | 复杂度 |
|---|---|---|---|
| `config.py` | 114 | `internal/config/config.go` | 低 |
| `models.py` | 226 | `internal/model/model.go` | 低 |
| `db.py` + `db_upsert.py` | 90 | `internal/repo/sqlite/sqlite.go` | 低 |
| `ch.py` | 414 | `internal/repo/ch/ch.go` | 中 |
| `migrations.py` | 372 | `internal/migration/migrate.go` | 低 |
| `services/binance/rest.py` | 268 | `internal/binance/rest.go` | 中 |
| `services/binance/backfill.py` | 129 | `internal/binance/rest.go`（合并） | 低 |
| `services/anomaly.py` | 481 | `internal/service/anomaly.go` | 高 |
| `services/absorption_signal.py` | 382 | `internal/service/absorption.go` | 高 |
| `services/signal_lab.py` | 1082 | `internal/service/signal_lab.go` | 高 |
| `services/market_supply.py` | 137 | `internal/service/market_supply.go` | 低 |
| `services/bot/*.py` | 218 | `internal/service/bot_*.go` | 低 |
| `services/symbol_filter.py` | 55 | `internal/binance/rest.go`（合并） | 低 |
| `hub/manager.py` | 162 | `internal/hub/manager.go` | 中 |
| `hub/publisher.py` | 55 | `internal/hub/publisher.go` | 低 |
| `hub/schemas.py` | 31 | `internal/hub/event.go` | 低 |
| `hub/anomaly_stream.py` | 80 | `internal/hub/anomaly_stream.go` | 低 |
| `hub/runtime.py` | 144 | `internal/hub/runtime.go` | 中 |
| `hub/depth_fullscan.py` | 357 | `internal/service/depth_scan.go` | 中 |
| `api/router.py` | 20 | `cmd/api/main.go`（路由注册） | 低 |
| `api/routes/coin.py` | 2596 | `internal/handler/coin.go` | 高（体量大） |
| `api/routes/aggregate.py` | 196 | `internal/handler/aggregate.go` | 中 |
| `api/routes/anomaly.py` | 378 | `internal/handler/anomaly.go` | 中 |
| `api/routes/signal_lab.py` | 133 | `internal/handler/signal_lab.go` | 中 |
| `api/routes/user.py` | 89 | `internal/handler/user.go` | 低 |
| `api/routes/bot.py` | 39 | `internal/handler/bot.go` | 低 |
| `api/routes/hub.py` | 101 | `internal/handler/hub.go` | 中 |
| `api/routes/symbols.py` | 18 | `internal/handler/symbol.go` | 低 |
| `api/routes/health.py` | 8 | `internal/handler/health.go` | 低 |
| `telegram/run.py` | 2057 | `internal/telegram/bot.go` | 高 |

---

## 5. 关键模式转换

### 5.1 asyncio → goroutine + errgroup

**Python**:
```python
async def start_hub_runtime():
    tasks.append(asyncio.create_task(_heartbeat_loop(stop)))
    tasks.append(asyncio.create_task(_anomaly_poll(stop)))
```

**Go**:
```go
func (r *Runtime) Start(ctx context.Context) error {
    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return r.heartbeatLoop(gctx) })
    g.Go(func() error { return r.anomalyPoll(gctx) })
    return g.Wait()
}
```

### 5.2 SQLAlchemy ORM → sqlx 原生 SQL

**Python**:
```python
stmt = insert(OrderbookHeatmapSnapshot).values(rows)
stmt = stmt.on_conflict_do_update(...)
```

**Go**:
```go
const upsertHeatmap = `INSERT INTO orderbook_heatmap_1m (...) VALUES (...)
    ON CONFLICT (market,symbol,bucket_start_ms,side,price_bin)
    DO UPDATE SET intensity=excluded.intensity, level_count=excluded.level_count`
_, err := db.NamedExecContext(ctx, upsertHeatmap, row)
```

### 5.3 httpx + 内存缓存 → http.Client + singleflight + TTL

**Python**:
```python
_pairs_cache: dict[str, tuple[float, list[str]]] = {}
```

**Go**:
```go
type TTLCache[K comparable, V any] struct {
    mu      sync.RWMutex
    items   map[K]ttlItem[V]
    ttl     time.Duration
}
// + singleflight.Group 防止缓存击穿
```

### 5.4 pydantic BaseSettings → env struct

**Python**:
```python
class Settings(BaseSettings):
    api_host: str = "0.0.0.0"
    api_port: int = 8000
```

**Go**:
```go
type Config struct {
    Host string // API_HOST, default "0.0.0.0"
    Port int    // API_PORT, default 8000
}
func Load() (*Config, error) {
    return &Config{
        Host: getenv("API_HOST", "0.0.0.0"),
        Port: getenvInt("API_PORT", 8000),
    }, nil
}
```

### 5.5 WebSocket Hub 广播

**Python**:
```python
async def broadcast_event(self, payload: dict):
    for conn in self._connections.values():
        if self._matches(conn, payload):
            await conn.websocket.send_json(payload)
```

**Go**:
```go
func (m *Manager) Broadcast(event *HubEvent) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    for _, conn := range m.conns {
        if conn.Matches(event) {
            select {
            case conn.sendCh <- event: // 非阻塞发送
            default: // 队列满则丢弃，避免慢消费者阻塞全局
            }
        }
    }
}
// 每个连接有独立的 writePump goroutine 消费 sendCh
```

### 5.6 SQLite 写锁

**Python**:
```python
_sqlite_write_lock = asyncio.Lock()
async with _sqlite_write_lock:
    await session.execute(stmt)
```

**Go**:
```go
// 方案：单一 writer goroutine + channel
type SQLiteStore struct {
    db      *sqlx.DB
    writeCh chan writeOp
}
// 所有写操作发送到 writeCh，由单一 goroutine 串行执行
// 避免 "database is locked" 错误
```

---

## 6. 迁移顺序（分 6 个阶段）

### Phase 1：基础设施层
- `internal/config/` — 环境变量加载
- `internal/model/` — 数据结构定义
- `internal/repo/sqlite/` — SQLite 连接 + 迁移 + 基础 CRUD
- `internal/repo/ch/` — ClickHouse 客户端 + 核心查询
- `internal/repo/redis/` — Redis 封装
- `internal/pkg/` — TTL 缓存、数学工具
- `go.mod` + `Dockerfile`

### Phase 2：外部客户端
- `internal/binance/rest.go` — Binance REST + 缓存 + symbol filter
- 此阶段可独立运行单元测试验证对 Binance API 的调用

### Phase 3：核心服务
- `internal/service/anomaly.go` — 异常检测（ZigZag、breakout、volume/amplitude spike）
- `internal/service/absorption.go` — 吸筹信号（4h/1d/3d 窗口评估）
- `internal/service/depth_scan.go` — Depth fullscan + heatmap
- `internal/service/market_supply.go` — 市值数据
- `internal/service/bot_*.go` — Bot 排行服务

### Phase 4：Hub + WebSocket
- `internal/hub/` — 连接管理、发布器、异常流、运行时编排
- `internal/handler/hub.go` — WebSocket 端点

### Phase 5：HTTP 路由
- `internal/handler/*.go` — 所有 REST 端点
- `cmd/api/main.go` — Gin 路由注册、启动/关闭流程

### Phase 6：Telegram
- `internal/telegram/bot.go` — 查询 bot + 通知 bot
- 这是最独立的模块，可最后迁移

---

## 7. Docker Compose 集成

```yaml
# infra/docker-compose.yml 中替换 api 服务
api:
  build:
    context: ../apps/api-go
    dockerfile: Dockerfile
  mem_limit: 256m          # 从 768m 降至 256m
  env_file:
    - ../.env
  volumes:
    - coinmark_appdata:/data
  depends_on:
    redis:
      condition: service_healthy
    clickhouse:
      condition: service_healthy
  ports:
    - "8000:8000"
```

### Dockerfile 模板
```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /api ./cmd/api

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /api /api
EXPOSE 8000
ENTRYPOINT ["/api"]
```

---

## 8. 验证策略

| 阶段 | 验证方式 |
|---|---|
| Phase 1-2 | 单元测试：config 加载、CH/SQLite CRUD、Binance 客户端 mock |
| Phase 3 | 对比测试：同一输入下 Python 和 Go 服务输出一致 |
| Phase 4 | WebSocket 集成测试：连接、订阅、事件推送、心跳超时 |
| Phase 5 | API 对比测试：逐接口比对 Python 和 Go 的 JSON 响应 |
| Phase 6 | Telegram bot 端到端测试 |
| 全量 | Shadow mode 并行运行：Go 服务监听 8001，前端指向 Python 8000，日志比对 |

---

## 9. 风险与注意事项

1. **signal_lab.py（1082 行）** 包含复杂统计算法（线性回归、Z-score、climax 检测），翻译时需逐函数对齐数值精度
2. **coin.py（2596 行）** 是最大的路由文件，建议拆分时保持接口不变，内部按功能分组
3. **telegram/run.py（2057 行）** 高度依赖 aiogram 框架特性（FSM、middleware），Go 版需重新设计状态管理
4. **SQLite WAL 模式** 在 Go 中使用 modernc.org/sqlite（纯 Go）需确认 WAL 支持和并发性能
5. **ClickHouse 动态查询** — `ch.py` 中有大量字符串拼接 SQL，Go 版需统一使用参数化或 builder 模式防注入
6. **时区处理** — Python 用 `ZoneInfo`，Go 用 `time.LoadLocation`，需确保 Docker 镜像包含 tzdata

---

## 10. 预期收益

| 指标 | Python（当前） | Go（预期） |
|---|---|---|
| 内存 | 407 MB | 60–100 MB |
| 冷启动 | ~3s | <1s |
| Docker 镜像 | ~450 MB (python:3.10-slim + deps) | ~25 MB (alpine + static binary) |
| 并发能力 | asyncio 单线程 | goroutine 多核 |
| GC 压力 | 高（Python 对象碎片） | 低（Go GC 高效） |
