# ClickHouse 按天分区迁移（trade/orderbook）

## 目标
- 把高频写入表改为按天分区，降低 merge/TTL 的长期成本。
- 适用表：
  - `trade_buckets`
  - `orderbook_feature_buckets`

## 代码侧改动
- 新部署环境：`ingest-go` 建表默认带按天分区。
  - 文件：`apps/ingest-go/internal/store/store.go`
  - 分区键：`toDate(toDateTime(bucket_start_ms / 1000))`

## 现网迁移脚本
- 脚本：`scripts/clickhouse-partition-by-day.sql`
- 包含步骤：
  1. 创建分区影子表
  2. 按主键去重回灌（`argMax(..., version)`）
  3. 原子 `RENAME TABLE` 切换
  4. 验证后删除备份表

## 执行注意
- 迁移前要先停写（至少停 `ingest`），避免 copy+rename 期间漏数据。
- 如果备份表名已存在，先改脚本里的 `_old_nopartition` 后缀。
- 切换后重启 `ingest/api`，并观察 `system.merges` 与查询耗时。
