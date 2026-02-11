from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    tz: str = "Australia/Sydney"

    database_url: str
    redis_url: str
    clickhouse_url: str = ""
    clickhouse_db: str = "default"
    clickhouse_user: str = ""
    clickhouse_password: str = ""

    api_host: str = "0.0.0.0"
    api_port: int = 8000
    api_log_level: str = "info"

    hub_enabled: bool = True
    hub_allowed_origins: str = "*"
    hub_max_connections: int = 1000
    hub_heartbeat_timeout_sec: int = 45
    hub_heartbeat_interval_sec: int = 15
    hub_dedupe_window_sec: int = 60
    hub_broadcast_max_events_per_sec: int = 200
    hub_anomaly_scan_interval_sec: int = 2
    hub_anomaly_scan_batch_size: int = 200
    hub_wall_refresh_interval_sec: int = 300
    hub_climax_scan_interval_sec: int = 60
    depth_fullscan_enabled: bool = False
    depth_fullscan_market: str = "swap"
    depth_fullscan_limit_swap: int = 1000
    depth_fullscan_limit_spot: int = 5000
    depth_fullscan_symbols: str = (
        "BTC,ETH,BNB,SOL,DOGE,LTC,LDO,CRV,LINK,ADA,UNI,ONDO,AAVE,AVAX,1000PEPE,SUI,SEI,WLD,HYPE,TRUMP,PUMP,ZEC"
    )
    depth_fullscan_fast_symbols: str = "BTC,ETH,BNB,SOL"
    depth_fullscan_fast_interval_sec: int = 300
    depth_fullscan_slow_interval_sec: int = 900
    depth_fullscan_concurrency: int = 4
    depth_fullscan_jitter_sec: int = 45
    depth_heatmap_enabled: bool = True
    depth_heatmap_force_spot: bool = True
    depth_heatmap_band_pct: float = 0.05
    depth_heatmap_step_bps: float = 8.0
    depth_heatmap_min_intensity_usd: float = 10000.0
    depth_heatmap_step_overrides: str = ""

    ingest_enable_spot: bool = True
    ingest_enable_swap: bool = True
    ingest_enable_depth: bool = True
    ingest_nats_url: str = "nats://nats:4222"
    ingest_nats_stream_raw: str = "COINMARK_RAW"
    ingest_nats_subject_trade: str = "coinmark.raw.trade"
    ingest_nats_subject_depth: str = "coinmark.raw.depth"
    ingest_nats_consumer_prefix: str = "coinmark-ingest"
    ingest_symbol_universe: str = "all_usdt"
    ingest_symbol_limit: int = 0
    ingest_streams_per_conn: int = 200
    ingest_depth_update_ms: int = 100
    ingest_flush_interval_sec: int = 2
    ingest_db_batch_size: int = 2000
    ingest_symbol_refresh_interval_hours: int = 6
    ingest_runtime_report_interval_sec: int = 30

    backfill_enable: bool = True
    backfill_top_n: int = 120
    backfill_concurrency: int = 8
    backfill_1m_limit: int = 0
    backfill_15m_limit: int = 200
    backfill_1h_limit: int = 200
    backfill_4h_limit: int = 180
    backfill_1d_limit: int = 60

    oi_refresh_top_n: int = 300
    oi_refresh_interval_sec: int = 5 * 60

    sr_refresh_top_n: int = 200
    sr_refresh_interval_sec: int = 30 * 60

    anomaly_scan_top_n: int = 200
    anomaly_scan_interval_sec: int = 60
    anomaly_history_15m: int = 96
    anomaly_breakout_margin_pct: float = 0.001
    anomaly_volume_spike_factor: float = 3.0
    anomaly_amplitude_spike_factor: float = 2.5

    absorption_snapshot_retention_hours: int = 24
    absorption_snapshot_cleanup_interval_sec: int = 900

    rank_bucket: str = "15m"
    rank_history_buckets: int = 96
    rank_min_avg_notional: float = 1000.0

    market_cap_source: str = "binance_bapi_get_products"

    tg_enabled: bool = False
    tg_notify_bot_token: str = ""
    tg_query_bot_token: str = ""
    tg_notify_chat_id: str = ""
    tg_notify_market: str = "swap"
    tg_notify_poll_interval_sec: int = 5
    tg_notify_batch_window_sec: int = 30
    tg_notify_batch_max_items: int = 5
    tg_notify_min_level: str = "warning"
    tg_query_poll_timeout_sec: int = 25
    tg_state_redis_prefix: str = "coinmark:tg"


settings = Settings()  # noqa: S105锛堟湰椤圭洰涓嶅湪浠ｇ爜涓繚瀛樺瘑閽ワ級


