from __future__ import annotations

from typing import Any, Literal

import time

from fastapi import APIRouter, Query
from sqlalchemy import and_, desc, func, select

from coinmark_api.db import SessionLocal
from coinmark_api.models import AnomalyEvent, SRLevel
from coinmark_api.services.absorption_signal import list_latest_absorption_signals
from coinmark_api.services.symbol_filter import is_excluded_symbol


router = APIRouter()

Market = Literal["spot", "swap"]


def _to_float(v) -> float | None:
    if v is None:
        return None
    try:
        return float(v)
    except Exception:
        return None


def _safe_mean(values: list[float]) -> float | None:
    if not values:
        return None
    return sum(values) / len(values)


def _clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


def _percentile_rank(values: list[float], value: float) -> float | None:
    if not values:
        return None
    sorted_vals = sorted(values)
    n = len(sorted_vals)
    le_count = 0
    for item in sorted_vals:
        if item <= value:
            le_count += 1
        else:
            break
    return le_count / n


def _signal_rank(state: str) -> int:
    if state == "STRONG":
        return 3
    if state == "CONFIRM":
        return 2
    if state == "WATCH":
        return 1
    return 0


def _shape_absorption_windows(windows: dict | None) -> dict:
    raw = windows or {}
    return {
        "4h": raw.get("4h") or {"passed": False},
        "1d": raw.get("1d") or {"passed": False},
        "3d": raw.get("3d") or {"passed": False},
    }


@router.get("/aggregate/orderbookAbsorptionSignals")
async def orderbook_absorption_signals(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    limit: int = Query(100, ge=10, le=500),
    onlySignals: bool = Query(True),
    signalLookbackMinutes: int = Query(3 * 24 * 60, ge=60, le=7 * 24 * 60),
    direction: str = Query("all", pattern="^(all|long|short)$"),
) -> dict:
    rows = await list_latest_absorption_signals(
        market=market,
        only_signals=onlySignals,
        limit=limit,
        signal_lookback_minutes=signalLookbackMinutes,
        direction=direction,
    )
    items = [
        {
            "symbol": r.symbol,
            "signalState": r.signal_state,
            "direction": r.direction,
            "score": int(round(float(r.score))),
            "netFlowStrength": _to_float(r.net_flow_strength),
            "impactPerNotional": _to_float(r.impact_per_notional),
            "windows": _shape_absorption_windows(r.windows),
            "reasons": r.reasons or [],
            "ts": int(r.bucket_start_ms),
        }
        for r in rows
        if not is_excluded_symbol(r.symbol)
    ]

    items.sort(
        key=lambda x: (
            _signal_rank(str(x.get("signalState") or "NONE")),
            float(x.get("score") or 0.0),
            int(x.get("ts") or 0),
        ),
        reverse=True,
    )
    return {
        "market": market,
        "onlySignals": onlySignals,
        "signalLookbackMinutes": signalLookbackMinutes,
        "direction": direction,
        "items": items[: int(limit)],
    }


def _event_base_score(event_type: str) -> float:
    t = (event_type or "").lower()
    if t in {"breakout_up", "breakout_down"}:
        return 60.0
    if t == "signal_lab_persistent_buy":
        return 65.0
    if t in {"signal_lab_bid_wall", "signal_lab_ask_wall"}:
        return 62.0
    if t in {"signal_lab_climax_short", "signal_lab_climax_long"}:
        return 68.0
    if t == "volume_spike":
        return 45.0
    if t == "amplitude_spike":
        return 40.0
    return 35.0


def _event_severity_score(event_type: str, details: dict[str, Any] | None) -> float:
    data = details or {}
    score = _event_base_score(event_type)

    volume_factor = _to_float(data.get("volumeFactor"))
    if volume_factor is not None and volume_factor > 1:
        score += min(20.0, (volume_factor - 1.0) * 3.0)

    amplitude = _to_float(data.get("amplitude"))
    if amplitude is not None and amplitude > 0:
        score += min(20.0, amplitude * 100.0 * 2.0)

    strength = _to_float(data.get("strengthScore"))
    if strength is not None and strength > 0:
        score += min(20.0, strength / 5.0)

    touches = _to_float(data.get("touches"))
    if touches is not None and touches >= 3:
        score += min(8.0, touches)

    return round(_clamp(score, 0.0, 100.0), 1)


def _event_severity_level(score: float) -> str:
    if score >= 80:
        return "critical"
    if score >= 55:
        return "warning"
    return "info"


def _format_factor(value: float | None, suffix: str = "x") -> str | None:
    if value is None:
        return None
    return f"{value:.1f}{suffix}"


def _event_narrative(
    *,
    symbol: str,
    event_type: str,
    title: str,
    details: dict[str, Any] | None,
    tf_signal: str,
    tf_level: str | None,
) -> str:
    data = details or {}
    t = (event_type or "").lower()

    if t == "breakout_up":
        level_price = _to_float(data.get("levelPrice"))
        volume_factor = _to_float(data.get("volumeFactor"))
        if level_price is not None:
            return f"{symbol} 向上突破{tf_level or '关键'}位 {level_price:.6f}，量能 {_format_factor(volume_factor) or '-'}"
        return f"{symbol} 出现向上突破，信号周期 {tf_signal}"

    if t == "breakout_down":
        level_price = _to_float(data.get("levelPrice"))
        amplitude = _to_float(data.get("amplitude"))
        if level_price is not None:
            amp_text = f"，振幅 {amplitude * 100.0:.2f}%" if amplitude is not None else ""
            return f"{symbol} 跌破{tf_level or '关键'}支撑 {level_price:.6f}{amp_text}"
        return f"{symbol} 出现向下跌破，信号周期 {tf_signal}"

    if t == "volume_spike":
        volume_factor = _to_float(data.get("volumeFactor"))
        return f"{symbol} {tf_signal} 量能异常放大 {_format_factor(volume_factor) or '-'}"

    if t == "amplitude_spike":
        amplitude = _to_float(data.get("amplitude"))
        if amplitude is not None:
            return f"{symbol} {tf_signal} 振幅显著扩大至 {amplitude * 100.0:.2f}%"
        return f"{symbol} {tf_signal} 振幅显著扩大"

    if t == "signal_lab_persistent_buy":
        score = _to_float(data.get("score"))
        buy_ratio = _to_float(data.get("buyRatio"))
        large_count = _to_float(data.get("largeBuyCount"))
        parts: list[str] = [f"{symbol} 出现持续吸筹信号"]
        if score is not None:
            parts.append(f"评分 {score:.1f}")
        if buy_ratio is not None:
            parts.append(f"买入占比 {buy_ratio * 100.0:.1f}%")
        if large_count is not None:
            parts.append(f"大单次数 {int(large_count)}")
        return "，".join(parts)

    if t in {"signal_lab_climax_short", "signal_lab_climax_long"}:
        direction = str(data.get("direction") or "short")
        dir_cn = "看空" if direction == "short" else "看多"
        vol_ratio = _to_float(data.get("volumeRatio"))
        cascade_ratio = _to_float(data.get("cascadeBuyRatio"))
        ob_imb = _to_float(data.get("obImbalance"))
        score = _to_float(data.get("score"))
        parts: list[str] = [f"{symbol} 天量反转{dir_cn}"]
        if vol_ratio is not None:
            parts.append(f"量能 {vol_ratio:.1f}x")
        if cascade_ratio is not None:
            if direction == "short":
                parts.append(f"砸盘买比 {cascade_ratio * 100:.0f}%")
            else:
                parts.append(f"抢筹买比 {cascade_ratio * 100:.0f}%")
        if ob_imb is not None:
            parts.append(f"深度偏移 {ob_imb:+.2f}")
        if score is not None:
            parts.append(f"评分 {score:.1f}")
        return "，".join(parts)

    if t in {"signal_lab_bid_wall", "signal_lab_ask_wall"}:
        impact_ratio = _to_float(data.get("impactRatio"))
        survive_count = _to_float(data.get("surviveCount"))
        confidence = str(data.get("confidence") or "MEDIUM")
        zone_type = "买盘" if t == "signal_lab_bid_wall" else "卖盘"
        parts: list[str] = [f"{symbol} 出现可影响价格的{zone_type}挂单墙"]
        if impact_ratio is not None:
            parts.append(f"影响比 {impact_ratio:.2f}")
        if survive_count is not None:
            parts.append(f"存活 {int(survive_count)} 分钟")
        parts.append(f"置信度 {confidence}")
        return "，".join(parts)

    return title or f"{symbol} 出现市场异动"


@router.get("/aggregate/hotMarkets")
async def hot_markets(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    limit: int = Query(50, ge=1, le=500),
    sinceMinutes: int = Query(6 * 60, ge=5, le=7 * 24 * 60),
    eventType: str | None = Query(None, min_length=3, max_length=32),
) -> dict:
    since_ms = int(time.time() * 1000) - sinceMinutes * 60 * 1000
    async with SessionLocal() as session:
        stmt = select(AnomalyEvent).where(and_(AnomalyEvent.market == market, AnomalyEvent.event_time_ms >= since_ms))
        if eventType:
            stmt = stmt.where(AnomalyEvent.event_type == eventType)
        stmt = stmt.order_by(desc(AnomalyEvent.event_time_ms)).limit(limit)
        rows = (await session.execute(stmt)).scalars().all()

    first_seen_map: dict[int, bool] = {}
    seen_pairs: set[tuple[str, str]] = set()
    for row in sorted(rows, key=lambda x: int(x.event_time_ms or 0)):
        pair = (str(row.symbol), str(row.event_type))
        is_first = pair not in seen_pairs
        first_seen_map[int(row.id)] = is_first
        if is_first:
            seen_pairs.add(pair)

    items: list[dict[str, Any]] = []
    for row in rows:
        if is_excluded_symbol(row.symbol):
            continue
        details = row.details if isinstance(row.details, dict) else {}
        severity_score = _event_severity_score(str(row.event_type), details)
        items.append(
            {
                "id": row.id,
                "symbol": row.symbol,
                "eventType": row.event_type,
                "tfSignal": row.tf_signal,
                "tfLevel": row.tf_level,
                "eventTimeMs": int(row.event_time_ms),
                "title": row.title,
                "details": details,
                "severityScore": severity_score,
                "severityLevel": _event_severity_level(severity_score),
                "narrative": _event_narrative(
                    symbol=str(row.symbol),
                    event_type=str(row.event_type),
                    title=str(row.title),
                    details=details,
                    tf_signal=str(row.tf_signal),
                    tf_level=row.tf_level,
                ),
                "firstSeenInWindow": bool(first_seen_map.get(int(row.id), False)),
            }
        )

    return {
        "market": market,
        "sinceMinutes": sinceMinutes,
        "eventType": eventType,
        "items": items,
    }


@router.get("/aggregate/anomalyStats")
async def anomaly_stats(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    sinceMinutes: int = Query(24 * 60, ge=15, le=14 * 24 * 60),
) -> dict:
    since_ms = int(time.time() * 1000) - sinceMinutes * 60 * 1000
    async with SessionLocal() as session:
        stmt = (
            select(AnomalyEvent.event_type, func.count().label("cnt"))
            .where(and_(AnomalyEvent.market == market, AnomalyEvent.event_time_ms >= since_ms))
            .group_by(AnomalyEvent.event_type)
        )
        rows = (await session.execute(stmt)).all()
    return {"market": market, "sinceMinutes": sinceMinutes, "counts": {k: int(v) for k, v in rows}}


@router.get("/aggregate/srLevels")
async def sr_levels(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    timeframe: str = Query("4h", pattern="^(4h)$"),
    limit: int = Query(30, ge=1, le=200),
) -> dict:
    sym = symbol.strip().upper()
    if is_excluded_symbol(sym):
        return {
            "market": market,
            "symbol": sym,
            "timeframe": timeframe,
            "items": [],
        }
    async with SessionLocal() as session:
        stmt = (
            select(SRLevel)
            .where(and_(SRLevel.market == market, SRLevel.symbol == sym, SRLevel.timeframe == timeframe))
            .order_by(SRLevel.strength_score.desc())
            .limit(limit)
        )
        rows = (await session.execute(stmt)).scalars().all()

    return {
        "market": market,
        "symbol": sym,
        "timeframe": timeframe,
        "items": [
            {
                "levelPrice": float(r.level_price),
                "touches": int(r.touches),
                "strengthScore": float(r.strength_score),
                "lastTouchMs": int(r.last_touch_ms),
                "updatedAt": r.updated_at.isoformat(),
            }
            for r in rows
        ],
    }
