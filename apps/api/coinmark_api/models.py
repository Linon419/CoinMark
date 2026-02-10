from __future__ import annotations

import datetime as dt

from sqlalchemy import BigInteger, DateTime, Index, Integer, JSON, Numeric, String, UniqueConstraint, func
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    pass


class TradeBucket(Base):
    __tablename__ = "trade_buckets"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)  # spot|swap
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)  # BTCUSDT
    bucket: Mapped[str] = mapped_column(String(8), nullable=False)  # 15m|1h|1d
    bucket_start_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)  # UTC ms

    taker_buy_notional: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    taker_sell_notional: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    quote_notional: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    trade_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)

    first_trade_ms: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    last_trade_ms: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    open_price: Mapped[float | None] = mapped_column(Numeric(38, 18), nullable=True)
    close_price: Mapped[float | None] = mapped_column(Numeric(38, 18), nullable=True)
    high_price: Mapped[float | None] = mapped_column(Numeric(38, 18), nullable=True)
    low_price: Mapped[float | None] = mapped_column(Numeric(38, 18), nullable=True)

    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "bucket", "bucket_start_ms", name="uq_trade_bucket"),
        Index("ix_trade_bucket_query", "market", "bucket", "bucket_start_ms"),
        Index("ix_trade_bucket_symbol", "market", "symbol", "bucket", "bucket_start_ms"),
    )


class OrderbookFeatureBucket(Base):
    __tablename__ = "orderbook_feature_buckets"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)  # spot|swap
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)  # BTCUSDT
    bucket: Mapped[str] = mapped_column(String(8), nullable=False)  # 1m
    bucket_start_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)  # UTC ms

    spread_bps_sum: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    microprice_shift_bps_sum: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    depth_imbalance_l20_sum: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    wall_pressure_l20_sum: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    sample_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)

    taker_buy_notional: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)
    taker_sell_notional: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False, default=0)

    depletion_events: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    replenishment_events: Mapped[int] = mapped_column(Integer, nullable=False, default=0)

    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "bucket", "bucket_start_ms", name="uq_orderbook_feature_bucket"),
        Index("ix_orderbook_feature_symbol", "market", "symbol", "bucket", "bucket_start_ms"),
        Index("ix_orderbook_feature_query", "market", "bucket", "bucket_start_ms"),
    )


class FundingRateSnapshot(Base):
    __tablename__ = "funding_rate_snapshots"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False, unique=True)
    last_funding_rate: Mapped[float] = mapped_column(Numeric(18, 10), nullable=False)
    mark_price: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    event_time_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)  # 取抓取时间

    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())


class OpenInterestSnapshot(Base):
    __tablename__ = "open_interest_snapshots"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False, unique=True)
    open_interest: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)  # base 数量
    mark_price: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    oi_notional_usd: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    event_time_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)

    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())


class AssetMarketCap(Base):
    __tablename__ = "asset_market_caps"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    asset: Mapped[str] = mapped_column(String(32), nullable=False, unique=True)  # BTC
    price_usd: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    circulating_supply: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    market_cap_usd: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    source: Mapped[str] = mapped_column(String(64), nullable=False)
    event_time_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)

    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())


Index("ix_asset_market_caps_cap", AssetMarketCap.market_cap_usd)


class Favorite(Base):
    __tablename__ = "favorites"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    client_id: Mapped[str] = mapped_column(String(64), nullable=False)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())

    __table_args__ = (
        UniqueConstraint("client_id", "market", "symbol", name="uq_favorites"),
        Index("ix_favorites_client", "client_id"),
    )


class SRLevel(Base):
    __tablename__ = "sr_levels"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    level_price: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    timeframe: Mapped[str] = mapped_column(String(8), nullable=False)  # 4h
    touches: Mapped[int] = mapped_column(Integer, nullable=False)
    strength_score: Mapped[float] = mapped_column(Numeric(18, 6), nullable=False)
    last_touch_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "timeframe", "level_price", name="uq_sr_levels"),
        Index("ix_sr_levels_symbol", "market", "symbol", "timeframe"),
    )


class AnomalyEvent(Base):
    __tablename__ = "anomaly_events"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    event_type: Mapped[str] = mapped_column(String(32), nullable=False)
    tf_signal: Mapped[str] = mapped_column(String(8), nullable=False)  # 15m
    tf_level: Mapped[str | None] = mapped_column(String(8), nullable=True)  # 4h
    event_time_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)
    title: Mapped[str] = mapped_column(String(256), nullable=False)
    details: Mapped[dict] = mapped_column(JSON, nullable=False)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())


Index("ix_anomaly_events_time", AnomalyEvent.event_time_ms.desc())
Index("ix_anomaly_events_symbol", AnomalyEvent.market, AnomalyEvent.symbol, AnomalyEvent.event_time_ms.desc())


class PriceImpactWallCandidate(Base):
    __tablename__ = "price_impact_wall_candidates"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    bucket_start_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)
    zone_type: Mapped[str] = mapped_column(String(8), nullable=False)  # bid|ask

    zone_low: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    zone_high: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)

    signal_state: Mapped[str] = mapped_column(String(16), nullable=False)  # WATCH|CONFIRM|STRONG
    confidence: Mapped[str] = mapped_column(String(8), nullable=False)  # LOW|MEDIUM|HIGH

    real_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    impact_ratio: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    survive_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    cancel_ratio: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)

    reasons: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    details: Mapped[dict] = mapped_column(JSON, nullable=False, default=dict)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "bucket_start_ms", "zone_type", name="uq_price_impact_wall_candidate"),
        Index("ix_price_impact_wall_time", "market", "bucket_start_ms"),
        Index("ix_price_impact_wall_symbol", "market", "symbol", "bucket_start_ms"),
        Index("ix_price_impact_wall_state", "market", "confidence", "bucket_start_ms"),
    )


class AbsorptionSignalSnapshot(Base):
    __tablename__ = "absorption_signal_snapshots"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    bucket_start_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)
    direction: Mapped[str] = mapped_column(String(16), nullable=False)  # LONG_BIAS|SHORT_BIAS
    signal_state: Mapped[str] = mapped_column(String(16), nullable=False)  # NONE|WATCH|CONFIRM|STRONG
    score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    net_flow_strength: Mapped[float | None] = mapped_column(Numeric(18, 8), nullable=True)
    impact_per_notional: Mapped[float | None] = mapped_column(Numeric(18, 12), nullable=True)
    window_4h_passed: Mapped[bool] = mapped_column(nullable=False, default=False)
    window_1d_passed: Mapped[bool] = mapped_column(nullable=False, default=False)
    window_3d_passed: Mapped[bool] = mapped_column(nullable=False, default=False)
    windows: Mapped[dict] = mapped_column(JSON, nullable=False, default=dict)
    reasons: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "bucket_start_ms", "direction", name="uq_absorption_signal_snapshot"),
        Index("ix_absorption_signal_market_time", "market", "bucket_start_ms"),
        Index("ix_absorption_signal_market_symbol", "market", "symbol", "bucket_start_ms"),
    )


class InstitutionalLevelSnapshot(Base):
    __tablename__ = "orderbook_real_levels_1m"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    market: Mapped[str] = mapped_column(String(8), nullable=False)
    symbol: Mapped[str] = mapped_column(String(32), nullable=False)
    bucket_start_ms: Mapped[int] = mapped_column(BigInteger, nullable=False)
    zone_type: Mapped[str] = mapped_column(String(8), nullable=False)  # bid|ask

    zone_low: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)
    zone_high: Mapped[float] = mapped_column(Numeric(38, 18), nullable=False)

    real_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    signal_state: Mapped[str] = mapped_column(String(16), nullable=False, default="NONE")  # NONE|WATCH|CONFIRM|STRONG

    persistence_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    absorb_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    replenish_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    defend_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    flow_align_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    size_score: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)
    cancel_penalty: Mapped[float] = mapped_column(Numeric(10, 4), nullable=False, default=0)

    reasons: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("market", "symbol", "bucket_start_ms", "zone_type", name="uq_orderbook_real_level_1m"),
        Index("ix_orderbook_real_level_market_time", "market", "bucket_start_ms"),
        Index("ix_orderbook_real_level_market_symbol", "market", "symbol", "bucket_start_ms"),
        Index("ix_orderbook_real_level_market_state", "market", "signal_state", "bucket_start_ms"),
    )
