# collector-go (POC)

当前阶段目标：
- 仅支持 `swap aggTrade` 的最小采集 PoC
- 输出到 Kafka/Redpanda topic（默认 `coinmark.raw_trade.poc`）

## 环境变量

- `COLLECTOR_MARKET`：当前仅支持 `swap`
- `COLLECTOR_BINANCE_WS_BASE_URL`：默认 `wss://fstream.binance.com/stream`
- `COLLECTOR_KAFKA_BROKERS`：默认 `redpanda:9092`
- `COLLECTOR_KAFKA_TOPIC`：默认 `coinmark.raw_trade.poc`
- `COLLECTOR_KAFKA_CLIENT_ID`：默认 `collector-go-poc`
- `COLLECTOR_LOG_INTERVAL_SEC`：默认 `15`

## 运行

通过 compose（已接入）：

```bash
docker compose -f infra/docker-compose.yml up -d --build redpanda collector-go
docker compose -f infra/docker-compose.yml logs -f collector-go
```

## 当前进度

- [x] 项目骨架与容器化
- [ ] Binance `swap aggTrade` 连接
- [ ] Kafka 生产写入
- [ ] 消息格式与联调验收

