# collector-go (NATS JetStream)

当前目标：
- 采集 Binance Spot/Swap 的 `aggTrade` 和 `depth5`
- 发布到 NATS JetStream

## 环境变量

- `COLLECTOR_MARKET`：`spot` 或 `swap`
- `COLLECTOR_BINANCE_WS_BASE_URL`：WS 基地址
- `COLLECTOR_BINANCE_REST_BASE`：REST 基地址
- `COLLECTOR_NATS_URL`：默认 `nats://nats:4222`
- `COLLECTOR_NATS_STREAM_RAW`：默认 `COINMARK_RAW`
- `COLLECTOR_NATS_SUBJECT_TRADE`：默认 `coinmark.raw.trade`
- `COLLECTOR_NATS_SUBJECT_DEPTH`：默认 `coinmark.raw.depth`
- `COLLECTOR_NATS_CLIENT_NAME`：默认 `collector-go`
- `COLLECTOR_ENABLE_DEPTH`：是否采 depth，默认 `true`
- `COLLECTOR_DEPTH_UPDATE_MS`：深度更新频率，默认 `100`
- `COLLECTOR_STREAMS_PER_CONN`：每连接流数，默认 `200`

## 运行

```bash
docker compose -f infra/docker-compose.yml up -d --build nats collector-go collector-go-spot
docker compose -f infra/docker-compose.yml logs -f collector-go
```

