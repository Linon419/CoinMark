from __future__ import annotations

import time
from typing import Any, Literal

from sqlalchemy import and_, desc, func, select

from coinmark_api.db import SessionLocal, write_session
from coinmark_api.db_upsert import insert
from coinmark_api.ch import query_trade_flow_agg
from coinmark_api.models import AnomalyEvent, InstitutionalLevelSnapshot, PriceImpactWallCandidate
from coinmark_api.services.binance.rest import get_orderbook_depth
from coinmark_api.services.institutional_levels import refresh_institutional_level_snapshots

Market = Literal["spot", "swap"]
MarketScope = Literal["spot", "swap", "both"]

_EVENT_TYPE_BID_WALL = "signal_lab_bid_wall"
_EVENT_TYPE_ASK_WALL = "signal_lab_ask_wall"


def _to_float(v: Any) -> float:
    try:
        return float(v or 0.0)
    except Exception:
        return 0.0


def _clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


def _markets(scope: MarketScope) -> list[Market]:
    if scope == "both":
        return ["spot", "swap"]
    return [scope]


def _event_type_for_zone(zone_type: str) -> str:
    return _EVENT_TYPE_BID_WALL if str(zone_type).lower() == "bid" else _EVENT_TYPE_ASK_WALL


def _wall_confidence(score: int) -> str:
    if score >= 6:
        return "HIGH"
    if score >= 3:
        return "MEDIUM"
    return "LOW"


def _estimate_wall_size_from_depth(
    *,
    depth: dict[str, Any] | None,
    zone_type: str,
    zone_low: float,
    zone_high: float,
) -> dict[str, float | int] | None:
    if not depth or zone_low <= 0 or zone_high <= 0 or zone_high < zone_low:
        return None

    side = "bids" if str(zone_type).lower() == "bid" else "asks"
    levels = depth.get(side)
    if not isinstance(levels, list):
        return None

    qty_sum = 0.0
    notional_sum = 0.0
    level_count = 0
    for lv in levels:
        if not isinstance(lv, (list, tuple)) or len(lv) < 2:
            continue
        price = _to_float(lv[0])
        qty = _to_float(lv[1])
        if price <= 0 or qty <= 0:
            continue
        if zone_low <= price <= zone_high:
            qty_sum += qty
            notional_sum += price * qty
            level_count += 1

    if level_count <= 0:
        return None

    return {
        "wallQty": round(qty_sum, 8),
        "wallNotional": round(notional_sum, 4),
        "wallLevels": int(level_count),
    }


def _score_wall_candidate(
    row: InstitutionalLevelSnapshot,
    *,
    survive_count: int,
    agg_flow_confirm: bool,
) -> dict[str, Any]:
    real_score = _to_float(row.real_score)
    absorb_score = _to_float(row.absorb_score)
    cancel_penalty = _to_float(row.cancel_penalty)
    impact_ratio = round(_clamp(absorb_score / 15.0, 0.0, 7.0), 4)
    cancel_ratio = round(_clamp(cancel_penalty / 100.0, 0.0, 1.0), 4)

    strength = (
        0.35 * _clamp(real_score, 0, 100)
        + 0.25 * _clamp(survive_count / 15.0 * 100.0, 0, 100)
        + 0.20 * _clamp(absorb_score, 0, 100)
        + 0.20 * (100.0 if agg_flow_confirm else 0.0)
        - 0.10 * _clamp(cancel_penalty, 0, 100)
    )
    strength = _clamp(strength, 0, 100)

    if strength >= 60:
        confidence = "HIGH"
    elif strength >= 35:
        confidence = "MEDIUM"
    else:
        confidence = "LOW"

    return {
        "score": round(strength, 2),
        "confidence": confidence,
        "realScore": round(real_score, 4),
        "impactRatio": impact_ratio,
        "surviveCount": survive_count,
        "cancelRatio": cancel_ratio,
    }


async def _compute_survive_counts(
    session,
    *,
    market: str,
    keys: list[tuple[str, str]],
    lookback_ms: int,
) -> dict[tuple[str, str], int]:
    """Count consecutive recent snapshots with signal_state >= WATCH per (symbol, zone_type).

    Rows are ordered newest-first. We count from the newest snapshot backwards,
    stopping at the first NONE.
    """
    if not keys:
        return {}
    now_ms = int(time.time() * 1000)
    since_ms = now_ms - lookback_ms
    symbols = sorted({s for s, _ in keys})
    stmt = (
        select(
            InstitutionalLevelSnapshot.symbol,
            InstitutionalLevelSnapshot.zone_type,
            InstitutionalLevelSnapshot.signal_state,
        )
        .where(
            and_(
                InstitutionalLevelSnapshot.market == market,
                InstitutionalLevelSnapshot.symbol.in_(symbols),
                InstitutionalLevelSnapshot.bucket_start_ms >= since_ms,
            )
        )
        .order_by(
            InstitutionalLevelSnapshot.symbol,
            InstitutionalLevelSnapshot.zone_type,
            desc(InstitutionalLevelSnapshot.bucket_start_ms),
        )
    )
    rows = (await session.execute(stmt)).all()

    from collections import defaultdict
    counts: dict[tuple[str, str], int] = defaultdict(int)
    stopped: set[tuple[str, str]] = set()
    for symbol, zone_type, signal_state in rows:
        k = (str(symbol), str(zone_type))
        if k in stopped:
            continue
        if str(signal_state) in ("WATCH", "CONFIRM", "STRONG"):
            counts[k] += 1
        else:
            stopped.add(k)

    return dict(counts)


async def _load_symbol_flow_bias(
    *,
    market: Market,
    symbols: list[str],
    lookback_minutes: int,
) -> dict[str, dict[str, float]]:
    if not symbols:
        return {}
    since_ms = int(time.time() * 1000) - max(15, int(lookback_minutes)) * 60 * 1000
    agg = await query_trade_flow_agg(market=market, symbols=symbols, bucket="1m", start_ms=since_ms)
    out: dict[str, dict[str, float]] = {}
    for symbol, buy, sell in agg:
        total = buy + sell
        out[symbol] = {"buy": buy, "sell": sell, "net": buy - sell, "buyRatio": (buy / total) if total > 0 else 0.0}
    return out


async def _sync_wall_anomaly_events(
    session,
    *,
    market: str,
    rows: list[dict[str, Any]],
    cooldown_minutes: int,
) -> int:
    if not rows:
        return 0

    cutoff_ms = int(time.time() * 1000) - max(1, int(cooldown_minutes)) * 60 * 1000

    symbols = sorted({str(x.get("symbol") or "") for x in rows if x.get("symbol")})
    if not symbols:
        return 0

    event_types = [_EVENT_TYPE_BID_WALL, _EVENT_TYPE_ASK_WALL]
    stmt = (
        select(
            AnomalyEvent.symbol,
            AnomalyEvent.event_type,
            func.max(AnomalyEvent.event_time_ms).label("latest"),
        )
        .where(
            and_(
                AnomalyEvent.market == market,
                AnomalyEvent.event_type.in_(event_types),
                AnomalyEvent.symbol.in_(symbols),
                AnomalyEvent.event_time_ms >= cutoff_ms,
            )
        )
        .group_by(AnomalyEvent.symbol, AnomalyEvent.event_type)
    )
    latest_map = {
        (str(symbol), str(event_type)): int(latest or 0)
        for symbol, event_type, latest in (await session.execute(stmt)).all()
    }

    values: list[dict[str, Any]] = []
    depth_cache: dict[str, dict[str, Any] | None] = {}
    cooldown_ms = max(1, int(cooldown_minutes)) * 60 * 1000
    for item in rows:
        symbol = str(item.get("symbol") or "")
        zone_type = str(item.get("zoneType") or "")
        event_type = _event_type_for_zone(zone_type)
        ts = int(item.get("ts") or 0)
        if not symbol or ts <= 0:
            continue

        prev = latest_map.get((symbol, event_type), 0)
        if prev > 0 and ts - prev < cooldown_ms:
            continue

        impact_ratio = _to_float(item.get("impactRatio"))
        survive_count = int(item.get("surviveCount") or 0)
        confidence = str(item.get("confidence") or "MEDIUM")
        real_score = _to_float(item.get("realScore"))

        depth = depth_cache.get(symbol)
        if symbol not in depth_cache:
            try:
                depth = await get_orderbook_depth(market=market, symbol=symbol, limit=1000)
            except Exception:
                depth = None
            depth_cache[symbol] = depth
        wall_size = _estimate_wall_size_from_depth(
            depth=depth,
            zone_type=zone_type,
            zone_low=_to_float(item.get("zoneLow")),
            zone_high=_to_float(item.get("zoneHigh")),
        )

        side_text = "买盘" if zone_type == "bid" else "卖盘"
        values.append(
            {
                "market": market,
                "symbol": symbol,
                "event_type": event_type,
                "tf_signal": "1m",
                "tf_level": "1h",
                "event_time_ms": ts,
                "title": f"{symbol} 出现可影响价格的{side_text}挂单墙",
                "details": {
                    "zoneType": zone_type,
                    "zoneLow": item.get("zoneLow"),
                    "zoneHigh": item.get("zoneHigh"),
                    "confidence": confidence,
                    "realScore": real_score,
                    "strengthScore": real_score,
                    "impactRatio": impact_ratio,
                    "surviveCount": survive_count,
                    "cancelRatio": item.get("cancelRatio"),
                    "wallQty": wall_size.get("wallQty") if wall_size else None,
                    "wallNotional": wall_size.get("wallNotional") if wall_size else None,
                    "wallLevels": wall_size.get("wallLevels") if wall_size else None,
                    "signalState": item.get("signalState"),
                    "score": real_score,
                    "reasons": item.get("reasons") or [],
                },
            }
        )
        latest_map[(symbol, event_type)] = ts

    if not values:
        return 0

    await session.execute(insert(AnomalyEvent).values(values))
    return len(values)


async def refresh_price_impact_walls(
    *,
    market_scope: MarketScope = "both",
    symbol_limit: int = 200,
    lookback_minutes: int = 120,
    flow_window_minutes: int = 240,
    cooldown_minutes: int = 30,
    min_survive_count: int = 5,
    min_impact_ratio: float = 1.5,
    sync_to_score_flow: bool = True,
) -> dict[str, Any]:
    now_ms = int(time.time() * 1000)
    since_ms = now_ms - max(15, int(lookback_minutes)) * 60 * 1000

    market_stats: dict[str, Any] = {}
    total_candidates = 0
    total_inserted_events = 0

    async with write_session() as session:
        for market in _markets(market_scope):
            await refresh_institutional_level_snapshots(market=market, top_n=symbol_limit, session=session)

            stmt = (
                select(InstitutionalLevelSnapshot)
                .where(
                    and_(
                        InstitutionalLevelSnapshot.market == market,
                        InstitutionalLevelSnapshot.bucket_start_ms >= since_ms,
                        InstitutionalLevelSnapshot.signal_state.in_(["WATCH", "CONFIRM", "STRONG"]),
                    )
                )
                .order_by(desc(InstitutionalLevelSnapshot.bucket_start_ms), desc(InstitutionalLevelSnapshot.real_score))
            )
            snapshots = (await session.execute(stmt)).scalars().all()

            latest_by_key: dict[tuple[str, str], InstitutionalLevelSnapshot] = {}
            for row in snapshots:
                key = (str(row.symbol), str(row.zone_type))
                if key in latest_by_key:
                    continue
                latest_by_key[key] = row

            survive_map = await _compute_survive_counts(
                session,
                market=market,
                keys=list(latest_by_key.keys()),
                lookback_ms=6 * 60 * 60 * 1000,
            )

            symbols = sorted({symbol for symbol, _ in latest_by_key.keys()})
            flow_map = await _load_symbol_flow_bias(
                market=market,
                symbols=symbols,
                lookback_minutes=flow_window_minutes,
            )

            candidates: list[dict[str, Any]] = []
            values: list[dict[str, Any]] = []
            for (sym_key, zone_type), row in latest_by_key.items():
                flow = flow_map.get(str(row.symbol), {})
                buy_ratio = _to_float(flow.get("buyRatio"))
                net_flow = _to_float(flow.get("net"))
                agg_flow_confirm = (
                    (zone_type == "bid" and net_flow > 0 and buy_ratio >= 0.60)
                    or (zone_type == "ask" and net_flow < 0 and buy_ratio <= 0.40)
                )

                real_survive = survive_map.get((sym_key, zone_type), 0)
                scored = _score_wall_candidate(row, survive_count=real_survive, agg_flow_confirm=agg_flow_confirm)
                confidence = str(scored["confidence"])
                if confidence == "LOW":
                    continue
                if int(scored["surviveCount"]) < int(min_survive_count):
                    continue
                if float(scored["impactRatio"]) < float(min_impact_ratio):
                    continue

                reasons = list(row.reasons or [])
                if agg_flow_confirm and "AGG_FLOW_CONFIRM" not in reasons:
                    reasons.append("AGG_FLOW_CONFIRM")

                item = {
                    "market": market,
                    "symbol": str(row.symbol),
                    "ts": int(row.bucket_start_ms),
                    "zoneType": str(zone_type),
                    "zoneLow": _to_float(row.zone_low),
                    "zoneHigh": _to_float(row.zone_high),
                    "signalState": str(row.signal_state),
                    "confidence": confidence,
                    "realScore": scored["realScore"],
                    "impactRatio": scored["impactRatio"],
                    "surviveCount": scored["surviveCount"],
                    "cancelRatio": scored["cancelRatio"],
                    "reasons": reasons,
                    "flow": {
                        "buyRatio": round(buy_ratio, 4),
                        "net": net_flow,
                    },
                }
                candidates.append(item)

                values.append(
                    {
                        "market": market,
                        "symbol": item["symbol"],
                        "bucket_start_ms": item["ts"],
                        "zone_type": item["zoneType"],
                        "zone_low": item["zoneLow"],
                        "zone_high": item["zoneHigh"],
                        "signal_state": item["signalState"],
                        "confidence": confidence,
                        "real_score": item["realScore"],
                        "impact_ratio": item["impactRatio"],
                        "survive_count": item["surviveCount"],
                        "cancel_ratio": item["cancelRatio"],
                        "reasons": reasons,
                        "details": {
                            "flow": item["flow"],
                            "source": "orderbook_real_levels_1m",
                            "scoreRule": "doc_v1_proxy",
                        },
                    }
                )

            if values:
                stmt_upsert = insert(PriceImpactWallCandidate).values(values)
                stmt_upsert = stmt_upsert.on_conflict_do_update(
                    index_elements=["market", "symbol", "bucket_start_ms", "zone_type"],
                    set_={
                        "zone_low": stmt_upsert.excluded.zone_low,
                        "zone_high": stmt_upsert.excluded.zone_high,
                        "signal_state": stmt_upsert.excluded.signal_state,
                        "confidence": stmt_upsert.excluded.confidence,
                        "real_score": stmt_upsert.excluded.real_score,
                        "impact_ratio": stmt_upsert.excluded.impact_ratio,
                        "survive_count": stmt_upsert.excluded.survive_count,
                        "cancel_ratio": stmt_upsert.excluded.cancel_ratio,
                        "reasons": stmt_upsert.excluded.reasons,
                        "details": stmt_upsert.excluded.details,
                    },
                )
                await session.execute(stmt_upsert)

            inserted_events = 0
            if sync_to_score_flow and candidates:
                inserted_events = await _sync_wall_anomaly_events(
                    session,
                    market=market,
                    rows=candidates,
                    cooldown_minutes=cooldown_minutes,
                )

            market_stats[market] = {
                "rawSnapshots": len(snapshots),
                "latestPairs": len(latest_by_key),
                "candidates": len(candidates),
                "insertedEvents": int(inserted_events),
            }
            total_candidates += len(candidates)
            total_inserted_events += int(inserted_events)

        await session.execute(
            PriceImpactWallCandidate.__table__.delete().where(
                PriceImpactWallCandidate.bucket_start_ms < (now_ms - 7 * 24 * 60 * 60 * 1000)
            )
        )
        await session.commit()

    return {
        "market": market_scope,
        "stats": market_stats,
        "candidates": total_candidates,
        "insertedEvents": total_inserted_events,
        "eventTypes": [_EVENT_TYPE_BID_WALL, _EVENT_TYPE_ASK_WALL],
        "ts": now_ms,
    }


async def list_latest_price_impact_walls(
    *,
    market: Market,
    limit: int = 100,
    lookback_minutes: int = 360,
    zone_type: str = "all",
    min_confidence: str = "MEDIUM",
) -> list[PriceImpactWallCandidate]:
    now_ms = int(time.time() * 1000)
    since_ms = now_ms - max(15, int(lookback_minutes)) * 60 * 1000

    conf = (min_confidence or "MEDIUM").upper()
    if conf == "HIGH":
        conf_allow = ["HIGH"]
    elif conf == "LOW":
        conf_allow = ["LOW", "MEDIUM", "HIGH"]
    else:
        conf_allow = ["MEDIUM", "HIGH"]

    async with SessionLocal() as session:
        where = [
            PriceImpactWallCandidate.market == market,
            PriceImpactWallCandidate.bucket_start_ms >= since_ms,
            PriceImpactWallCandidate.confidence.in_(conf_allow),
        ]
        z = (zone_type or "all").lower()
        if z in {"bid", "ask"}:
            where.append(PriceImpactWallCandidate.zone_type == z)

        rows = (
            (
                await session.execute(
                    select(PriceImpactWallCandidate)
                    .where(and_(*where))
                    .order_by(desc(PriceImpactWallCandidate.bucket_start_ms), desc(PriceImpactWallCandidate.real_score))
                )
            )
            .scalars()
            .all()
        )

    latest: dict[tuple[str, str], PriceImpactWallCandidate] = {}
    for row in rows:
        key = (str(row.symbol), str(row.zone_type))
        if key in latest:
            continue
        latest[key] = row

    out = sorted(
        list(latest.values()),
        key=lambda x: (
            2 if str(x.confidence) == "HIGH" else 1,
            _to_float(x.real_score),
            int(x.bucket_start_ms or 0),
        ),
        reverse=True,
    )
    return out[: max(1, int(limit))]
