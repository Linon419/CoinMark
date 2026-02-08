# CoinMark：ClickHouse + SQLite 迁移执行说明（2026-02-08）

## 目标

- API/TG 小事务与配置：SQLite
- 行情聚合与分析：ClickHouse
- ingest-go：主写 SQLite，镜像写 ClickHouse

## 当前状态

- `infra/docker-compose.yml` 已替换为 `clickhouse + nats + redis`
- `DATABASE_URL` 默认是 `sqlite+aiosqlite:///data/app.db`
- ingest-go 已支持双写（SQLite 主写 + ClickHouse 镜像）

## 迁移原则

1. 先保证新链路持续写入
2. 再做历史回补
3. 最后把 API 读路径逐步切到 ClickHouse

## 历史回补（推荐步骤）

### 1) 停止 ingest 写入（避免迁移时有并发）

```bash
docker compose -f infra/docker-compose.yml stop ingest
```

### 2) 导出 SQLite 历史数据（CSV）

示例（容器内）：

```bash
docker compose -f infra/docker-compose.yml exec api sh -lc "sqlite3 /data/app.db -header -csv 'select market,symbol,bucket,bucket_start_ms,taker_buy_notional,taker_sell_notional,quote_notional,trade_count,first_trade_ms,last_trade_ms,open_price,close_price,high_price,low_price from trade_buckets' > /data/trade_buckets.csv"
```

其余表同理：

- `orderbook_feature_buckets`
- `funding_rate_snapshots`
- `open_interest_snapshots`
- `asset_market_caps`

### 3) 导入 ClickHouse

示例（trade_buckets）：

```bash
docker compose -f infra/docker-compose.yml exec clickhouse sh -lc "clickhouse-client --query=\"INSERT INTO trade_buckets FORMAT CSVWithNames\" < /var/lib/clickhouse/user_files/trade_buckets.csv"
```

如果 CSV 在 `/data`，先复制到 `clickhouse user_files` 再导入。

### 4) 对账

至少做这三项：

- 总行数对账：SQLite vs ClickHouse
- 最近 24h 分钟桶对账：`BTCUSDT` spot/swap
- 抽样字段对账：`bucket_start_ms/open/close/high/low`

### 5) 恢复 ingest

```bash
docker compose -f infra/docker-compose.yml start ingest
```

## 注意事项

- ClickHouse `ReplacingMergeTree` 最终一致，刚写入后做严格对账建议带 `FINAL`
- 如需极致一致性，迁移窗口内建议暂停 ingest
- API 目前默认读 SQLite，后续可按接口逐步改读 ClickHouse

