-- ClickHouse migration: add daily partition for hot high-write tables.
-- Target tables:
--   1) trade_buckets
--   2) orderbook_feature_buckets
--
-- IMPORTANT:
-- 1) Stop writers first (collector/ingest/api write path) to avoid missing writes during copy+swap.
-- 2) Run in the target database (default in this repo).
-- 3) If backup table names already exist, change suffix before running RENAME.

-- ---------------------------------------------------------------------------
-- 0) Create partitioned shadow tables
-- ---------------------------------------------------------------------------

DROP TABLE IF EXISTS trade_buckets_pbyday;
CREATE TABLE trade_buckets_pbyday (
    market String,
    symbol String,
    bucket String,
    bucket_start_ms Int64,
    taker_buy_notional Float64,
    taker_sell_notional Float64,
    quote_notional Float64,
    trade_count Int64,
    first_trade_ms Nullable(Int64),
    last_trade_ms Nullable(Int64),
    open_price Nullable(Float64),
    close_price Nullable(Float64),
    high_price Nullable(Float64),
    low_price Nullable(Float64),
    version UInt64
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toDate(toDateTime(bucket_start_ms / 1000))
ORDER BY (market, symbol, bucket, bucket_start_ms)
TTL toDateTime(bucket_start_ms / 1000) + INTERVAL 31 DAY;

DROP TABLE IF EXISTS orderbook_feature_buckets_pbyday;
CREATE TABLE orderbook_feature_buckets_pbyday (
    market String,
    symbol String,
    bucket String,
    bucket_start_ms Int64,
    spread_bps_sum Float64,
    microprice_shift_bps_sum Float64,
    depth_imbalance_l20_sum Float64 DEFAULT 0,
    wall_pressure_l20_sum Float64 DEFAULT 0,
    sample_count Int64,
    taker_buy_notional Float64,
    taker_sell_notional Float64,
    depletion_events Int64,
    replenishment_events Int64,
    version UInt64
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toDate(toDateTime(bucket_start_ms / 1000))
ORDER BY (market, symbol, bucket, bucket_start_ms)
TTL toDateTime(bucket_start_ms / 1000) + INTERVAL 7 DAY;

-- ---------------------------------------------------------------------------
-- 1) Backfill with key-level dedup (keep latest version)
--    不写别名：别名 version 会遮住原列，argMax(..., version) 会变成聚合套聚合；INSERT SELECT 按位置对应列。
--    GROUP BY 与排序键一致，按序聚合，避免超出 ClickHouse 内存上限。
-- ---------------------------------------------------------------------------

INSERT INTO trade_buckets_pbyday
SELECT
    market,
    symbol,
    bucket,
    bucket_start_ms,
    argMax(taker_buy_notional, version),
    argMax(taker_sell_notional, version),
    argMax(quote_notional, version),
    toInt64(argMax(trade_count, version)),
    argMax(first_trade_ms, version),
    argMax(last_trade_ms, version),
    argMax(open_price, version),
    argMax(close_price, version),
    argMax(high_price, version),
    argMax(low_price, version),
    max(version)
FROM trade_buckets
GROUP BY market, symbol, bucket, bucket_start_ms
SETTINGS optimize_aggregation_in_order = 1, max_bytes_before_external_group_by = 1000000000;

INSERT INTO orderbook_feature_buckets_pbyday
SELECT
    market,
    symbol,
    bucket,
    bucket_start_ms,
    argMax(spread_bps_sum, version),
    argMax(microprice_shift_bps_sum, version),
    argMax(depth_imbalance_l20_sum, version),
    argMax(wall_pressure_l20_sum, version),
    toInt64(argMax(sample_count, version)),
    argMax(taker_buy_notional, version),
    argMax(taker_sell_notional, version),
    toInt64(argMax(depletion_events, version)),
    toInt64(argMax(replenishment_events, version)),
    max(version)
FROM orderbook_feature_buckets
GROUP BY market, symbol, bucket, bucket_start_ms
SETTINGS optimize_aggregation_in_order = 1, max_bytes_before_external_group_by = 1000000000;

-- Optional quick sanity check:
-- SELECT 'trade_old' AS t, count() FROM trade_buckets;
-- SELECT 'trade_new' AS t, count() FROM trade_buckets_pbyday;
-- SELECT 'ob_old'    AS t, count() FROM orderbook_feature_buckets;
-- SELECT 'ob_new'    AS t, count() FROM orderbook_feature_buckets_pbyday;

-- ---------------------------------------------------------------------------
-- 2) Atomic swap (change backup suffix if name already exists)
-- ---------------------------------------------------------------------------

RENAME TABLE trade_buckets TO trade_buckets_old_nopartition,
             trade_buckets_pbyday TO trade_buckets;

RENAME TABLE orderbook_feature_buckets TO orderbook_feature_buckets_old_nopartition,
             orderbook_feature_buckets_pbyday TO orderbook_feature_buckets;

-- ---------------------------------------------------------------------------
-- 3) Verify and cleanup
-- ---------------------------------------------------------------------------
-- Verify partition key:
-- SELECT name, partition_key
-- FROM system.tables
-- WHERE database = currentDatabase()
--   AND name IN ('trade_buckets', 'orderbook_feature_buckets');
--
-- After verification, drop backup tables manually:
-- DROP TABLE trade_buckets_old_nopartition;
-- DROP TABLE orderbook_feature_buckets_old_nopartition;
