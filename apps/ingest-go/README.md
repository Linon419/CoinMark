# CoinMark Ingest-Go

Go 版 `ingest` 服务，负责：

- 从 NATS JetStream 消费 `trade/depth`
- 聚合并落库 `trade_buckets` / `orderbook_feature_buckets`
- 定时刷新 `funding` / `marketcap` / `open interest`
- 启动时可选回补 K 线

## 当前存储架构

- 主写：`SQLite`（`DATABASE_URL`）
- 镜像写：`ClickHouse`（`INGEST_CLICKHOUSE_URL` 或 `CLICKHOUSE_URL`）

说明：

- ingest 先写 SQLite 成功，再异步镜像写 ClickHouse
- ClickHouse 写失败不会中断主流程，只会打日志
- ClickHouse 表使用 `ReplacingMergeTree(version)`，查询时建议 `FINAL`

## 关键环境变量

- `DATABASE_URL`，示例：`sqlite+aiosqlite:///data/app.db`
- `INGEST_CLICKHOUSE_URL`，示例：`http://clickhouse:8123`
- `INGEST_ENABLE_SPOT` / `INGEST_ENABLE_SWAP` / `INGEST_ENABLE_DEPTH`
- `INGEST_NATS_URL` / `INGEST_NATS_STREAM_RAW`
- `INGEST_NATS_SUBJECT_TRADE` / `INGEST_NATS_SUBJECT_DEPTH`
- `INGEST_FLUSH_INTERVAL_SEC` / `INGEST_DB_BATCH_SIZE`

## 本地运行

```bash
cd apps/ingest-go
go run ./cmd/ingest
```

## Docker

`infra/docker-compose.yml` 里 `ingest` 已指向 `apps/ingest-go`。

