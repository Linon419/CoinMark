from __future__ import annotations

from sqlalchemy.ext.asyncio import AsyncEngine

from coinmark_api.db import dialect_name


async def migrate(engine: AsyncEngine) -> None:
    """
    轻量级迁移（仅用于本项目 MVP 阶段）。
    - 目标：在不引入 Alembic 的情况下，让 docker volume 中的 Postgres 表结构可演进。
    - 原则：只做“加字段/建表”这类向前兼容迁移；不做危险的删字段/改类型。
    """
    if dialect_name() == "sqlite":
        # SQLite：仅做必要的兼容迁移（避免旧库缺 side 字段导致运行时报错）
        async with engine.begin() as conn:
            table_exists = (
                await conn.exec_driver_sql(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name='orderbook_heatmap_1m';"
                )
            ).first()
            if not table_exists:
                return

            col_rows = (await conn.exec_driver_sql("PRAGMA table_info(orderbook_heatmap_1m);"))
            cols = [str(row[1]).lower() for row in col_rows.fetchall()]
            if "side" in cols:
                return

            await conn.exec_driver_sql(
                """
CREATE TABLE IF NOT EXISTS orderbook_heatmap_1m_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,
  side VARCHAR(8) NOT NULL DEFAULT 'unknown',
  price_bin NUMERIC(38, 18) NOT NULL,
  price_step NUMERIC(38, 18) NOT NULL,
  intensity NUMERIC(38, 18) NOT NULL DEFAULT 0,
  level_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (market, symbol, bucket_start_ms, side, price_bin)
);
"""
            )
            await conn.exec_driver_sql(
                """
INSERT INTO orderbook_heatmap_1m_new
  (id, market, symbol, bucket_start_ms, side, price_bin, price_step, intensity, level_count, created_at, updated_at)
SELECT
  id, market, symbol, bucket_start_ms, 'unknown', price_bin, price_step, intensity, level_count, created_at, updated_at
FROM orderbook_heatmap_1m;
"""
            )
            await conn.exec_driver_sql("DROP TABLE orderbook_heatmap_1m;")
            await conn.exec_driver_sql("ALTER TABLE orderbook_heatmap_1m_new RENAME TO orderbook_heatmap_1m;")
            await conn.exec_driver_sql(
                "CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_time ON orderbook_heatmap_1m (market, bucket_start_ms);"
            )
            await conn.exec_driver_sql(
                "CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_symbol ON orderbook_heatmap_1m (market, symbol, bucket_start_ms);"
            )
        return

    if dialect_name() != "postgresql":
        return

    async with engine.begin() as conn:
        # trade_buckets：补齐 OHLC 与首末成交时间，支持 15m/1h/4h/1d 的 return/amplitude 计算
        await conn.exec_driver_sql(
            """
ALTER TABLE trade_buckets
  ADD COLUMN IF NOT EXISTS first_trade_ms BIGINT,
  ADD COLUMN IF NOT EXISTS last_trade_ms BIGINT,
  ADD COLUMN IF NOT EXISTS open_price NUMERIC(38, 18),
  ADD COLUMN IF NOT EXISTS close_price NUMERIC(38, 18),
  ADD COLUMN IF NOT EXISTS high_price NUMERIC(38, 18),
  ADD COLUMN IF NOT EXISTS low_price NUMERIC(38, 18);
"""
        )

        # favorites：匿名收藏（client_id + market + symbol）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS favorites (
  id SERIAL PRIMARY KEY,
  client_id VARCHAR(64) NOT NULL,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (client_id, market, symbol)
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_favorites_client ON favorites (client_id);"
        )

        # sr_levels：支撑/阻力水平位（4h 周期）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS sr_levels (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  level_price NUMERIC(38, 18) NOT NULL,
  timeframe VARCHAR(8) NOT NULL, -- 4h
  touches INTEGER NOT NULL,
  strength_score NUMERIC(18, 6) NOT NULL,
  last_touch_ms BIGINT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (market, symbol, timeframe, level_price)
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_sr_levels_symbol ON sr_levels (market, symbol, timeframe);"
        )

        # anomaly_events：异动快讯/事件（突破/振幅/量能）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS anomaly_events (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  event_type VARCHAR(32) NOT NULL, -- breakout_up/breakout_down/amplitude_spike/volume_spike
  tf_signal VARCHAR(8) NOT NULL,   -- 15m
  tf_level VARCHAR(8) NULL,        -- 4h（breakout 才有）
  event_time_ms BIGINT NOT NULL,
  title VARCHAR(256) NOT NULL,
  details JSONB NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_anomaly_events_time ON anomaly_events (event_time_ms DESC);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_anomaly_events_symbol ON anomaly_events (market, symbol, event_time_ms DESC);"
        )
        await conn.exec_driver_sql(
            "CREATE UNIQUE INDEX IF NOT EXISTS uq_anomaly_event_dedup ON anomaly_events (market, symbol, event_type, tf_signal, event_time_ms);"
        )

        # orderbook_feature_buckets：盘口分钟特征（spread/imbalance/microprice/aggr_buy/replenish）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS orderbook_feature_buckets (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket VARCHAR(8) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,

  spread_bps_sum NUMERIC(38, 18) NOT NULL DEFAULT 0,
  microprice_shift_bps_sum NUMERIC(38, 18) NOT NULL DEFAULT 0,
  depth_imbalance_l20_sum NUMERIC(38, 18) NOT NULL DEFAULT 0,
  wall_pressure_l20_sum NUMERIC(38, 18) NOT NULL DEFAULT 0,
  sample_count INTEGER NOT NULL DEFAULT 0,

  taker_buy_notional NUMERIC(38, 18) NOT NULL DEFAULT 0,
  taker_sell_notional NUMERIC(38, 18) NOT NULL DEFAULT 0,

  depletion_events INTEGER NOT NULL DEFAULT 0,
  replenishment_events INTEGER NOT NULL DEFAULT 0,

  updated_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (market, symbol, bucket, bucket_start_ms)
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_feature_symbol ON orderbook_feature_buckets (market, symbol, bucket, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_feature_query ON orderbook_feature_buckets (market, bucket, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_feature_buckets ADD COLUMN IF NOT EXISTS depth_imbalance_l20_sum NUMERIC(38, 18) NOT NULL DEFAULT 0;"
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_feature_buckets ADD COLUMN IF NOT EXISTS wall_pressure_l20_sum NUMERIC(38, 18) NOT NULL DEFAULT 0;"
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_feature_buckets DROP COLUMN IF EXISTS depth_imbalance_l5_sum;"
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_feature_buckets DROP COLUMN IF EXISTS wall_pressure_l5_sum;"
        )

        # absorption_signal_snapshots：盘口吸筹信号快照（全币扫描结果落库）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS absorption_signal_snapshots (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,
  direction VARCHAR(16) NOT NULL,
  signal_state VARCHAR(16) NOT NULL,
  score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  net_flow_strength NUMERIC(18, 8) NULL,
  impact_per_notional NUMERIC(18, 12) NULL,
  window_4h_passed BOOLEAN NOT NULL DEFAULT FALSE,
  window_1d_passed BOOLEAN NOT NULL DEFAULT FALSE,
  window_3d_passed BOOLEAN NOT NULL DEFAULT FALSE,
  windows JSONB NOT NULL DEFAULT '{}'::jsonb,
  reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (market, symbol, bucket_start_ms, direction)
);
"""
        )
        await conn.exec_driver_sql(
            """
ALTER TABLE absorption_signal_snapshots
  ADD COLUMN IF NOT EXISTS window_4h_passed BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS window_1d_passed BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS window_3d_passed BOOLEAN NOT NULL DEFAULT FALSE;
"""
        )
        await conn.exec_driver_sql(
            """
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_name = 'absorption_signal_snapshots' AND column_name = 'window_20m_passed'
  ) THEN
    UPDATE absorption_signal_snapshots
    SET
      window_4h_passed = CASE WHEN window_20m_passed IS TRUE THEN TRUE ELSE window_4h_passed END,
      window_1d_passed = CASE WHEN window_60m_passed IS TRUE THEN TRUE ELSE window_1d_passed END,
      window_3d_passed = CASE WHEN window_180m_passed IS TRUE THEN TRUE ELSE window_3d_passed END
    WHERE window_20m_passed IS TRUE OR window_60m_passed IS TRUE OR window_180m_passed IS TRUE;
  END IF;
END $$;
"""
        )
        await conn.exec_driver_sql(
            """
UPDATE absorption_signal_snapshots
SET windows = (
  (COALESCE(windows, '{}'::jsonb) - '20m' - '60m' - '180m')
  || jsonb_build_object(
    '4h', COALESCE(windows->'4h', windows->'20m', jsonb_build_object('passed', FALSE)),
    '1d', COALESCE(windows->'1d', windows->'60m', jsonb_build_object('passed', FALSE)),
    '3d', COALESCE(windows->'3d', windows->'180m', jsonb_build_object('passed', FALSE))
  )
)
WHERE windows IS NULL
   OR windows ? '20m'
   OR windows ? '60m'
   OR windows ? '180m'
   OR NOT (windows ? '4h' AND windows ? '1d' AND windows ? '3d');
"""
        )
        await conn.exec_driver_sql(
            """
UPDATE absorption_signal_snapshots
SET reasons = COALESCE(
  (
    SELECT jsonb_agg(
      to_jsonb(
        replace(
          replace(
            replace(
              replace(v.value, '180m', '3d'),
              '60m', '1d'
            ),
            '20m', '4h'
          ),
          '120M', '12H'
        )
      )
    )
    FROM jsonb_array_elements_text(COALESCE(reasons, '[]'::jsonb)) AS v(value)
  ),
  '[]'::jsonb
)
WHERE reasons::text LIKE '%20m%'
   OR reasons::text LIKE '%60m%'
   OR reasons::text LIKE '%180m%'
   OR reasons::text LIKE '%120M%';
"""
        )
        await conn.exec_driver_sql(
            """
ALTER TABLE absorption_signal_snapshots
  DROP COLUMN IF EXISTS window_20m_passed,
  DROP COLUMN IF EXISTS window_60m_passed,
  DROP COLUMN IF EXISTS window_180m_passed;
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_absorption_signal_market_time ON absorption_signal_snapshots (market, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_absorption_signal_market_symbol ON absorption_signal_snapshots (market, symbol, bucket_start_ms);"
        )

        # orderbook_heatmap_1m：盘口热力图分钟分桶快照（时间 x 价格桶）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS orderbook_heatmap_1m (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,
  side VARCHAR(8) NOT NULL DEFAULT 'unknown',
  price_bin NUMERIC(38, 18) NOT NULL,
  price_step NUMERIC(38, 18) NOT NULL,
  intensity NUMERIC(38, 18) NOT NULL DEFAULT 0,
  level_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (market, symbol, bucket_start_ms, side, price_bin)
);
"""
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_heatmap_1m ADD COLUMN IF NOT EXISTS side VARCHAR(8) NOT NULL DEFAULT 'unknown';"
        )
        await conn.exec_driver_sql(
            "ALTER TABLE orderbook_heatmap_1m DROP CONSTRAINT IF EXISTS uq_orderbook_heatmap_1m;"
        )
        await conn.exec_driver_sql(
            """
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'uq_orderbook_heatmap_1m_side'
  ) THEN
    ALTER TABLE orderbook_heatmap_1m
      ADD CONSTRAINT uq_orderbook_heatmap_1m_side UNIQUE (market, symbol, bucket_start_ms, side, price_bin);
  END IF;
END
$$;
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_time ON orderbook_heatmap_1m (market, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_symbol ON orderbook_heatmap_1m (market, symbol, bucket_start_ms);"
        )

        # orderbook_real_levels_1m：机构/大户真实挂单位置（1m 快照）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS orderbook_real_levels_1m (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,
  zone_type VARCHAR(8) NOT NULL,

  zone_low NUMERIC(38, 18) NOT NULL,
  zone_high NUMERIC(38, 18) NOT NULL,

  real_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  signal_state VARCHAR(16) NOT NULL DEFAULT 'NONE',

  persistence_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  absorb_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  replenish_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  defend_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  flow_align_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  size_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  cancel_penalty NUMERIC(10, 4) NOT NULL DEFAULT 0,

  reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),

  UNIQUE (market, symbol, bucket_start_ms, zone_type)
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_real_level_market_time ON orderbook_real_levels_1m (market, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_real_level_market_symbol ON orderbook_real_levels_1m (market, symbol, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_orderbook_real_level_market_state ON orderbook_real_levels_1m (market, signal_state, bucket_start_ms);"
        )

        # price_impact_wall_candidates：仅保留通过规则的“有效挂单墙候选”
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS price_impact_wall_candidates (
  id SERIAL PRIMARY KEY,
  market VARCHAR(8) NOT NULL,
  symbol VARCHAR(32) NOT NULL,
  bucket_start_ms BIGINT NOT NULL,
  zone_type VARCHAR(8) NOT NULL,

  zone_low NUMERIC(38, 18) NOT NULL,
  zone_high NUMERIC(38, 18) NOT NULL,

  signal_state VARCHAR(16) NOT NULL,
  confidence VARCHAR(8) NOT NULL,

  real_score NUMERIC(10, 4) NOT NULL DEFAULT 0,
  impact_ratio NUMERIC(10, 4) NOT NULL DEFAULT 0,
  survive_count INTEGER NOT NULL DEFAULT 0,
  cancel_ratio NUMERIC(10, 4) NOT NULL DEFAULT 0,

  reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
  details JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),

  UNIQUE (market, symbol, bucket_start_ms, zone_type)
);
"""
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_price_impact_wall_time ON price_impact_wall_candidates (market, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_price_impact_wall_symbol ON price_impact_wall_candidates (market, symbol, bucket_start_ms);"
        )
        await conn.exec_driver_sql(
            "CREATE INDEX IF NOT EXISTS ix_price_impact_wall_state ON price_impact_wall_candidates (market, confidence, bucket_start_ms);"
        )

        # coin_info：币种扩展配置（包含 whale_min_val）
        await conn.exec_driver_sql(
            """
CREATE TABLE IF NOT EXISTS coin_info (
  id SERIAL PRIMARY KEY,
  symbol VARCHAR(32) NOT NULL UNIQUE,
  whale_min_val NUMERIC(38, 18) NULL,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);
"""
        )
