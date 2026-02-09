from __future__ import annotations

from typing import Literal

from fastapi import APIRouter, HTTPException, Query
from pydantic import BaseModel, Field

from coinmark_api.services.signal_lab import (
    SignalLabParams,
    get_backtest_run,
    get_realtime_signals,
    start_backtest_run,
)
from coinmark_api.services.price_impact_wall import list_latest_price_impact_walls, refresh_price_impact_walls


router = APIRouter()

MarketScope = Literal["spot", "swap", "both"]


class SignalLabParamBody(BaseModel):
    bucket: str = Field(default="1h", pattern="^(1m|15m|1h|4h)$")
    z_threshold: float = Field(default=2.8, ge=1.0, le=6.0)
    lookback_minutes: int = Field(default=4320, ge=60, le=30 * 24 * 60)
    detection_window_minutes: int = Field(default=1440, ge=15, le=7 * 24 * 60)
    min_large_count: int = Field(default=3, ge=1, le=20)
    buy_ratio_threshold: float = Field(default=0.8, ge=0.5, le=1.0)
    min_persistent_span_minutes: int = Field(default=180, ge=10, le=7 * 24 * 60)
    min_avg_interval_minutes: int = Field(default=60, ge=1, le=1440)
    min_distinct_time_buckets: int = Field(default=3, ge=2, le=24)
    forecast_horizon_minutes: int = Field(default=240, ge=5, le=7 * 24 * 60)
    cooldown_minutes: int = Field(default=720, ge=1, le=48 * 60)
    single_large_z_threshold: float = Field(default=3.5, ge=2.0, le=8.0)
    single_large_min_notional: float = Field(default=10000.0, ge=0, le=1000000)
    single_large_cooldown_minutes: int = Field(default=240, ge=1, le=48 * 60)
    slope_window_minutes: int = Field(default=720, ge=10, le=7 * 24 * 60)
    slope_r2_threshold: float = Field(default=0.7, ge=0.3, le=1.0)
    symbol_limit: int = Field(default=200, ge=20, le=400)

    def to_service(self) -> SignalLabParams:
        return SignalLabParams(
            bucket=str(self.bucket),
            z_threshold=float(self.z_threshold),
            lookback_minutes=int(self.lookback_minutes),
            detection_window_minutes=int(self.detection_window_minutes),
            min_large_count=int(self.min_large_count),
            buy_ratio_threshold=float(self.buy_ratio_threshold),
            min_persistent_span_minutes=int(self.min_persistent_span_minutes),
            min_avg_interval_minutes=int(self.min_avg_interval_minutes),
            min_distinct_time_buckets=int(self.min_distinct_time_buckets),
            forecast_horizon_minutes=int(self.forecast_horizon_minutes),
            cooldown_minutes=int(self.cooldown_minutes),
            single_large_z_threshold=float(self.single_large_z_threshold),
            single_large_min_notional=float(self.single_large_min_notional),
            single_large_cooldown_minutes=int(self.single_large_cooldown_minutes),
            slope_window_minutes=int(self.slope_window_minutes),
            slope_r2_threshold=float(self.slope_r2_threshold),
            symbol_limit=int(self.symbol_limit),
        )


class SignalLabBacktestStartBody(BaseModel):
    market: MarketScope = "both"
    days: int = Field(default=7, ge=1, le=30)
    params: SignalLabParamBody = Field(default_factory=SignalLabParamBody)


@router.post("/signal-lab/backtest")
async def start_backtest(payload: SignalLabBacktestStartBody) -> dict:
    run_id = await start_backtest_run(
        market_scope=payload.market,
        days=int(payload.days),
        params=payload.params.to_service(),
    )
    return {"runId": run_id, "status": "running"}


@router.get("/signal-lab/runs/{run_id}")
async def get_backtest(run_id: str) -> dict:
    row = await get_backtest_run(run_id)
    if row is None:
        raise HTTPException(status_code=404, detail="run not found")
    return row


@router.get("/signal-lab/realtime")
async def realtime(
    market: MarketScope = Query("both", pattern="^(spot|swap|both)$"),
    limit: int = Query(100, ge=10, le=300),
    min_signal_state: str = Query("CONFIRM", alias="minSignalState", pattern="^(WATCH|CONFIRM|STRONG|HIGH)$"),
    sync_score_flow: bool = Query(True, alias="syncScoreFlow"),
    bucket: str = Query("1h", pattern="^(1m|15m|1h|4h)$"),
    z_threshold: float = Query(2.8, alias="zThreshold", ge=1.0, le=6.0),
    lookback_minutes: int = Query(4320, alias="lookbackMinutes", ge=60, le=30 * 24 * 60),
    detection_window_minutes: int = Query(1440, alias="detectionWindowMinutes", ge=15, le=7 * 24 * 60),
    min_large_count: int = Query(3, alias="minLargeCount", ge=1, le=20),
    buy_ratio_threshold: float = Query(0.8, alias="buyRatioThreshold", ge=0.5, le=1.0),
    min_persistent_span_minutes: int = Query(180, alias="minPersistentSpanMinutes", ge=10, le=7 * 24 * 60),
    min_avg_interval_minutes: int = Query(60, alias="minAvgIntervalMinutes", ge=1, le=1440),
    min_distinct_time_buckets: int = Query(3, alias="minDistinctTimeBuckets", ge=2, le=24),
    cooldown_minutes: int = Query(720, alias="cooldownMinutes", ge=1, le=48 * 60),
    single_large_z_threshold: float = Query(3.5, alias="singleLargeZThreshold", ge=2.0, le=8.0),
    single_large_min_notional: float = Query(10000.0, alias="singleLargeMinNotional", ge=0, le=1000000),
    single_large_cooldown_minutes: int = Query(240, alias="singleLargeCooldownMinutes", ge=1, le=48 * 60),
    slope_window_minutes: int = Query(720, alias="slopeWindowMinutes", ge=10, le=7 * 24 * 60),
    slope_r2_threshold: float = Query(0.7, alias="slopeR2Threshold", ge=0.3, le=1.0),
    symbol_limit: int = Query(200, alias="symbolLimit", ge=20, le=400),
) -> dict:
    params = SignalLabParams(
        bucket=str(bucket),
        z_threshold=float(z_threshold),
        lookback_minutes=int(lookback_minutes),
        detection_window_minutes=int(detection_window_minutes),
        min_large_count=int(min_large_count),
        buy_ratio_threshold=float(buy_ratio_threshold),
        min_persistent_span_minutes=int(min_persistent_span_minutes),
        min_avg_interval_minutes=int(min_avg_interval_minutes),
        min_distinct_time_buckets=int(min_distinct_time_buckets),
        cooldown_minutes=int(cooldown_minutes),
        single_large_z_threshold=float(single_large_z_threshold),
        single_large_min_notional=float(single_large_min_notional),
        single_large_cooldown_minutes=int(single_large_cooldown_minutes),
        slope_window_minutes=int(slope_window_minutes),
        slope_r2_threshold=float(slope_r2_threshold),
        symbol_limit=int(symbol_limit),
    )
    return await get_realtime_signals(
        market_scope=market,
        params=params,
        limit=int(limit),
        min_signal_state=min_signal_state,
        sync_to_score_flow=bool(sync_score_flow),
    )


@router.get("/signal-lab/walls/realtime")
async def realtime_walls(
    market: MarketScope = Query("both", pattern="^(spot|swap|both)$"),
    symbol_limit: int = Query(200, alias="symbolLimit", ge=20, le=400),
    lookback_minutes: int = Query(120, alias="lookbackMinutes", ge=15, le=24 * 60),
    flow_window_minutes: int = Query(240, alias="flowWindowMinutes", ge=30, le=24 * 60),
    cooldown_minutes: int = Query(30, alias="cooldownMinutes", ge=1, le=12 * 60),
    min_survive_count: int = Query(5, alias="minSurviveCount", ge=1, le=120),
    min_impact_ratio: float = Query(1.5, alias="minImpactRatio", ge=0.5, le=8.0),
    sync_score_flow: bool = Query(True, alias="syncScoreFlow"),
) -> dict:
    return await refresh_price_impact_walls(
        market_scope=market,
        symbol_limit=int(symbol_limit),
        lookback_minutes=int(lookback_minutes),
        flow_window_minutes=int(flow_window_minutes),
        cooldown_minutes=int(cooldown_minutes),
        min_survive_count=int(min_survive_count),
        min_impact_ratio=float(min_impact_ratio),
        sync_to_score_flow=bool(sync_score_flow),
    )


@router.get("/signal-lab/walls")
async def list_walls(
    market: Literal["spot", "swap"] = Query("swap", pattern="^(spot|swap)$"),
    limit: int = Query(100, ge=10, le=300),
    lookback_minutes: int = Query(360, alias="lookbackMinutes", ge=15, le=24 * 60),
    zone_type: str = Query("all", alias="zoneType", pattern="^(all|bid|ask)$"),
    min_confidence: str = Query("MEDIUM", alias="minConfidence", pattern="^(LOW|MEDIUM|HIGH)$"),
) -> dict:
    rows = await list_latest_price_impact_walls(
        market=market,
        limit=int(limit),
        lookback_minutes=int(lookback_minutes),
        zone_type=zone_type,
        min_confidence=min_confidence,
    )
    items = [
        {
            "market": r.market,
            "symbol": r.symbol,
            "zoneType": r.zone_type,
            "zoneLow": float(r.zone_low),
            "zoneHigh": float(r.zone_high),
            "signalState": r.signal_state,
            "confidence": r.confidence,
            "realScore": float(r.real_score),
            "impactRatio": float(r.impact_ratio),
            "surviveCount": int(r.survive_count),
            "cancelRatio": float(r.cancel_ratio),
            "reasons": r.reasons or [],
            "details": r.details or {},
            "ts": int(r.bucket_start_ms),
        }
        for r in rows
    ]
    return {
        "market": market,
        "limit": int(limit),
        "lookbackMinutes": int(lookback_minutes),
        "zoneType": zone_type,
        "minConfidence": min_confidence,
        "items": items,
    }
