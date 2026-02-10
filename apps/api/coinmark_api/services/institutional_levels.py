from __future__ import annotations

import time
from typing import Any, Literal

from sqlalchemy import and_, desc, func, select

from coinmark_api.ch import OBFeatureRow, TradeBucketRow, query_market_caps, query_orderbook_features, query_trade_buckets
from sqlalchemy.ext.asyncio import AsyncSession

from coinmark_api.db import SessionLocal, write_session
from coinmark_api.db_upsert import insert
from coinmark_api.models import InstitutionalLevelSnapshot
from coinmark_api.services.binance.rest import get_ticker_24h_all
from coinmark_api.services.symbol_filter import filter_excluded_symbols, is_excluded_symbol


SignalState = Literal["NONE", "WATCH", "CONFIRM", "STRONG"]
ZoneType = Literal["bid", "ask"]


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


def _quantile(values: list[float], q: float) -> float | None:
    if not values:
        return None
    vals = sorted(values)
    if len(vals) == 1:
        return vals[0]
    pos = _clamp(q, 0.0, 1.0) * (len(vals) - 1)
    lo = int(pos)
    hi = min(len(vals) - 1, lo + 1)
    if lo == hi:
        return vals[lo]
    ratio = pos - lo
    return vals[lo] * (1.0 - ratio) + vals[hi] * ratio


def _signal_rank(state: str) -> int:
    if state == "STRONG":
        return 3
    if state == "CONFIRM":
        return 2
    if state == "WATCH":
        return 1
    return 0


def _build_merged_rows(
    ob_map: dict[int, OBFeatureRow],
    tr_map: dict[int, TradeBucketRow],
) -> list[dict[str, Any]]:
    timeline = sorted(set(ob_map.keys()) | set(tr_map.keys()))
    if not timeline:
        return []

    rows: list[dict[str, Any]] = []
    for ts in timeline:
        ob = ob_map.get(ts)
        tr = tr_map.get(ts)

        buy = _to_float(ob.taker_buy_notional if ob else (tr.taker_buy_notional if tr else 0)) or 0.0
        sell = _to_float(ob.taker_sell_notional if ob else (tr.taker_sell_notional if tr else 0)) or 0.0
        net = buy - sell
        denom = buy + sell
        aggr_buy_ratio = (buy / denom) if denom > 0 else None

        sample_count = int(ob.sample_count or 0) if ob else 0
        spread_bps = (_to_float(ob.spread_bps_sum) / sample_count) if (ob and sample_count > 0 and _to_float(ob.spread_bps_sum) is not None) else None

        replenish_score = None
        dep = 0
        rep = 0
        if ob:
            dep = int(ob.depletion_events or 0)
            rep = int(ob.replenishment_events or 0)
            replenish_score = 50.0 if dep <= 0 else _clamp(rep / dep * 100.0, 0.0, 100.0)

        wall_pressure_l20 = None
        depth_imbalance_l20 = None
        if ob and sample_count > 0:
            wall_pressure_l20 = _to_float(ob.wall_pressure_l20_sum) / sample_count if _to_float(ob.wall_pressure_l20_sum) is not None else None
            depth_imbalance_l20 = _to_float(ob.depth_imbalance_l20_sum) / sample_count if _to_float(ob.depth_imbalance_l20_sum) is not None else None

        rows.append(
            {
                "ts": ts,
                "netBuyNotional": net,
                "aggrBuyRatio": aggr_buy_ratio,
                "spreadBps": spread_bps,
                "replenishScore": replenish_score,
                "depletionEvents": dep,
                "replenishmentEvents": rep,
                "quoteNotional": _to_float(tr.quote_notional if tr else 0) or 0.0,
                "closePrice": _to_float(tr.close_price) if tr else None,
                "highPrice": _to_float(tr.high_price) if tr else None,
                "lowPrice": _to_float(tr.low_price) if tr else None,
                "wallPressureL20": wall_pressure_l20,
                "depthImbalanceL20": depth_imbalance_l20,
            }
        )

    for i in range(len(rows)):
        if i == 0:
            rows[i]["ret1m"] = 0.0
            continue
        prev_close = rows[i - 1].get("closePrice")
        cur_close = rows[i].get("closePrice")
        if prev_close and cur_close and prev_close > 0:
            rows[i]["ret1m"] = cur_close / prev_close - 1.0
        else:
            rows[i]["ret1m"] = 0.0
    return rows


def _score_zone(
    rows: list[dict[str, Any]],
    zone_type: ZoneType,
    *,
    liquidity_24h_quote: float | None = None,
    market_cap_usd: float | None = None,
) -> dict[str, Any] | None:
    if len(rows) < 20:
        return None

    side_sign = 1.0 if zone_type == "bid" else -1.0
    recent20 = rows[-20:] if len(rows) >= 20 else rows
    recent60 = rows[-60:] if len(rows) >= 60 else rows
    recent120 = rows[-120:] if len(rows) >= 120 else rows

    liquidity_value = max(1_000_000.0, float(liquidity_24h_quote or 0.0))
    market_cap_value = float(market_cap_usd or 0.0)
    liquidity_scale = _clamp((50_000_000.0 / liquidity_value) ** 0.25, 0.75, 1.50)
    cap_scale = _clamp((2_000_000_000.0 / max(50_000_000.0, market_cap_value)) ** 0.20, 0.80, 1.40) if market_cap_value > 0 else 1.0
    impact_scale = _clamp(0.70 * liquidity_scale + 0.30 * cap_scale, 0.75, 1.60)

    persistence_hits = 0
    for row in recent60:
        net = float(row.get("netBuyNotional") or 0.0)
        ratio = row.get("aggrBuyRatio")
        replenish = float(row.get("replenishScore") or 0.0)
        if zone_type == "bid":
            aligned = net > 0 and (ratio is None or float(ratio) >= 0.52)
        else:
            aligned = net < 0 and (ratio is None or float(ratio) <= 0.48)
        if aligned and replenish >= 45.0:
            persistence_hits += 1
    persistence_score = _clamp((persistence_hits / max(1, len(recent60))) * 100.0, 0.0, 100.0)

    # 影响型大单分：只统计“足够大且方向一致”的流，检验其后续 5m/15m 是否推动 K 线同向运行。
    flow_participation = [
        abs(float(r.get("netBuyNotional") or 0.0)) / max(1.0, float(r.get("quoteNotional") or 0.0)) * 100.0
        for r in recent60
    ]
    base_large_flow_threshold = max(0.08, float(_quantile(flow_participation, 0.75) or 0.08))
    large_flow_threshold = _clamp(base_large_flow_threshold / max(0.75, impact_scale), 0.04, 0.30)
    market_cap_flow_threshold_bps = _clamp(0.25 / max(0.85, impact_scale), 0.08, 0.40)
    forward_5m_threshold_pct = _clamp(0.05 / max(0.85, impact_scale), 0.02, 0.08)
    forward_15m_threshold_pct = _clamp(0.15 / max(0.85, impact_scale), 0.06, 0.24)

    impact_candidates = 0
    impact_hits = 0
    impact_strength_values: list[float] = []
    for idx in range(max(0, len(recent60) - 15)):
        row = recent60[idx]
        quote = float(row.get("quoteNotional") or 0.0)
        net = float(row.get("netBuyNotional") or 0.0)
        if quote <= 0:
            continue
        if side_sign * net <= 0:
            continue

        flow_pct = abs(net) / quote * 100.0
        cap_flow_bps = (abs(net) / market_cap_value * 10_000.0) if market_cap_value > 0 else 0.0
        if flow_pct < large_flow_threshold and cap_flow_bps < market_cap_flow_threshold_bps:
            continue

        impact_candidates += 1
        fwd5_pct = side_sign * sum(float(recent60[j].get("ret1m") or 0.0) for j in range(idx + 1, min(len(recent60), idx + 6))) * 100.0
        fwd15_pct = side_sign * sum(float(recent60[j].get("ret1m") or 0.0) for j in range(idx + 1, min(len(recent60), idx + 16))) * 100.0

        if fwd5_pct >= forward_5m_threshold_pct and fwd15_pct >= forward_15m_threshold_pct:
            impact_hits += 1

        strength = _clamp(
            (fwd5_pct / max(0.03, forward_5m_threshold_pct * 1.6)) * 0.35
            + (fwd15_pct / max(0.08, forward_15m_threshold_pct * 1.6)) * 0.65,
            0.0,
            1.0,
        )
        impact_strength_values.append(strength)

    hit_ratio = impact_hits / max(1, impact_candidates)
    strength_avg = _safe_mean(impact_strength_values) or 0.0
    sample_coverage = min(1.0, impact_candidates / 6.0)
    absorb_score = _clamp(((hit_ratio * 0.70 + strength_avg * 0.30) * sample_coverage) * 100.0, 0.0, 100.0)

    replenish_score = _clamp((_safe_mean([float(r.get("replenishScore") or 0.0) for r in recent60]) or 0.0), 0.0, 100.0)

    lows = [float(r.get("lowPrice")) for r in recent120 if r.get("lowPrice") is not None]
    highs = [float(r.get("highPrice")) for r in recent120 if r.get("highPrice") is not None]
    closes = [float(r.get("closePrice")) for r in recent120 if r.get("closePrice") is not None]
    spreads = [float(r.get("spreadBps")) for r in recent60 if r.get("spreadBps") is not None]

    if zone_type == "bid":
        center = _quantile(lows, 0.30) if lows else _quantile(closes, 0.45)
    else:
        center = _quantile(highs, 0.70) if highs else _quantile(closes, 0.55)
    if center is None or center <= 0:
        return None

    spread_ref = _quantile(spreads, 0.50) if spreads else 8.0
    half_width_bps = _clamp(max(3.0, float(spread_ref) * 2.5), 3.0, 30.0)
    zone_low = center * (1.0 - half_width_bps / 10000.0)
    zone_high = center * (1.0 + half_width_bps / 10000.0)

    latest_close = next((float(r.get("closePrice")) for r in reversed(recent20) if r.get("closePrice") is not None), None)
    zone_mid = (zone_low + zone_high) / 2.0
    distance_pct = (abs(zone_mid - latest_close) / latest_close * 100.0) if latest_close and latest_close > 0 else None

    abs_ret_pct = [abs(float(r.get("ret1m") or 0.0)) * 100.0 for r in recent60]
    vol_p75 = float(_quantile(abs_ret_pct, 0.75) or 0.0)
    swing_low = _quantile(lows, 0.10) if lows else None
    swing_high = _quantile(highs, 0.90) if highs else None
    swing_range_pct = (
        (float(swing_high) - float(swing_low)) / latest_close * 100.0
        if (latest_close and latest_close > 0 and swing_low is not None and swing_high is not None and swing_high > swing_low)
        else 0.0
    )
    spread_pct = float(spread_ref) / 100.0
    min_distance_pct = _clamp(max(swing_range_pct * 0.35, vol_p75 * 6.0 + spread_pct * 8.0), 5.0, 40.0)
    max_distance_pct = _clamp(min_distance_pct * 2.2, min_distance_pct + 8.0, 70.0)
    distance_ok = distance_pct is not None and min_distance_pct <= distance_pct <= max_distance_pct

    touches = 0
    defended = 0
    for idx in range(max(0, len(recent120) - 1)):
        cur = recent120[idx]
        nxt = recent120[idx + 1]
        cur_low = cur.get("lowPrice")
        cur_high = cur.get("highPrice")
        if cur_low is None or cur_high is None:
            continue
        touch = float(cur_low) <= zone_high if zone_type == "bid" else float(cur_high) >= zone_low
        if not touch:
            continue
        touches += 1
        next_ret = float(nxt.get("ret1m") or 0.0)
        if zone_type == "bid":
            if next_ret >= -0.0008:
                defended += 1
        else:
            if next_ret <= 0.0008:
                defended += 1
    defend_score = _clamp(((defended / touches) * 100.0) if touches > 0 else 0.0, 0.0, 100.0)

    recent_touch_count = 0
    recent_reversal_hits = 0
    touch_scan_start = max(0, len(recent60) - 45)
    touch_scan_end = max(touch_scan_start, len(recent60) - 5)
    for idx in range(touch_scan_start, touch_scan_end):
        cur = recent60[idx]
        cur_low = cur.get("lowPrice")
        cur_high = cur.get("highPrice")
        if cur_low is None or cur_high is None:
            continue
        touch = float(cur_low) <= zone_high if zone_type == "bid" else float(cur_high) >= zone_low
        if not touch:
            continue
        recent_touch_count += 1
        fwd5_pct = side_sign * sum(float(recent60[j].get("ret1m") or 0.0) for j in range(idx + 1, min(len(recent60), idx + 6))) * 100.0
        if fwd5_pct >= forward_5m_threshold_pct:
            recent_reversal_hits += 1
    reversal_hit_ratio = (recent_reversal_hits / recent_touch_count) if recent_touch_count > 0 else 0.0

    aligned_notional = 0.0
    total_notional = 0.0
    for row in recent120:
        net = float(row.get("netBuyNotional") or 0.0)
        total_notional += abs(net)
        aligned_notional += max(side_sign * net, 0.0)
    flow_align_score = _clamp((aligned_notional / total_notional * 100.0) if total_notional > 0 else 0.0, 0.0, 100.0)

    participation = [abs(float(r.get("netBuyNotional") or 0.0)) / max(1.0, float(r.get("quoteNotional") or 0.0)) for r in recent120]
    quote_vals = [float(r.get("quoteNotional") or 0.0) for r in recent120]
    recent_participation = _safe_mean(participation[-20:]) or 0.0
    recent_quote = _safe_mean(quote_vals[-20:]) or 0.0
    part_rank = _percentile_rank(participation, recent_participation) or 0.0
    quote_rank = _percentile_rank(quote_vals, recent_quote) or 0.0
    base_size_score = ((part_rank + quote_rank) / 2.0) * 100.0

    l20_vals = [float(r.get("wallPressureL20") or 0.0) for r in recent60 if r.get("wallPressureL20") is not None]
    l20_mean = _safe_mean(l20_vals) or 0.0
    l20_aligned = side_sign * l20_mean
    deep_wall_bonus = _clamp(l20_aligned * 25.0, -10.0, 15.0)

    size_score = _clamp(base_size_score + deep_wall_bonus, 0.0, 100.0)

    dep_sum = sum(int(r.get("depletionEvents") or 0) for r in recent60)
    rep_sum = sum(int(r.get("replenishmentEvents") or 0) for r in recent60)
    if dep_sum <= 0:
        cancel_penalty = 5.0
    else:
        cancel_penalty = _clamp(((dep_sum - rep_sum) / dep_sum) * 100.0, 0.0, 100.0)

    real_score = _clamp(
        0.18 * persistence_score
        + 0.30 * absorb_score
        + 0.12 * replenish_score
        + 0.10 * defend_score
        + 0.15 * flow_align_score
        + 0.15 * size_score
        - 0.05 * cancel_penalty,
        0.0,
        100.0,
    )

    reversal_confirmed = (
        distance_ok
        and recent_touch_count >= 1
        and reversal_hit_ratio >= 0.5
        and defend_score >= 60.0
        and absorb_score >= 55.0
        and flow_align_score >= 55.0
        and size_score >= 55.0
    )

    state: SignalState = "NONE"
    if reversal_confirmed:
        if real_score >= 75.0:
            state = "STRONG"
        else:
            state = "CONFIRM"
            real_score = max(real_score, 60.0)

    reasons: list[str] = []
    if distance_pct is not None:
        if distance_ok:
            reasons.append(f"DISTANCE_OK_{distance_pct:.1f}%[{min_distance_pct:.1f},{max_distance_pct:.1f}]")
        else:
            reasons.append(f"DISTANCE_FILTERED_{distance_pct:.1f}%[{min_distance_pct:.1f},{max_distance_pct:.1f}]")
    if persistence_score >= 60:
        reasons.append("PERSISTENCE_OK")
    if absorb_score >= 60:
        reasons.append("TREND_IMPACT_OK")
    if replenish_score >= 55:
        reasons.append("REPLENISH_OK")
    if defend_score >= 55 and touches >= 3:
        reasons.append("DEFEND_OK")
    if flow_align_score >= 60:
        reasons.append("FLOW_ALIGN_OK")
    if size_score >= 65:
        reasons.append("SIZE_CLUSTER_OK")
    if cancel_penalty >= 60:
        reasons.append("HIGH_CANCEL_PENALTY")
    if recent_touch_count > 0:
        reasons.append(f"TOUCH_COUNT_{recent_touch_count}")
    if reversal_hit_ratio >= 0.5 and recent_touch_count > 0:
        reasons.append(f"REVERSAL_RATIO_{reversal_hit_ratio:.2f}")
    if not reasons:
        reasons.append("NO_CLEAR_INSTITUTIONAL_PATTERN")

    return {
        "zone_low": zone_low,
        "zone_high": zone_high,
        "real_score": round(real_score, 2),
        "signal_state": state,
        "persistence_score": round(persistence_score, 2),
        "absorb_score": round(absorb_score, 2),
        "replenish_score": round(replenish_score, 2),
        "defend_score": round(defend_score, 2),
        "flow_align_score": round(flow_align_score, 2),
        "size_score": round(size_score, 2),
        "cancel_penalty": round(cancel_penalty, 2),
        "reasons": reasons,
    }


async def refresh_institutional_level_snapshots(market: str = "swap", top_n: int = 200, *, session: AsyncSession | None = None) -> None:
    now_ms = int(time.time() * 1000)
    full_start_ms = now_ms - 24 * 60 * 60 * 1000
    bucket_start_ms = (now_ms // 60_000) * 60_000

    tickers = await get_ticker_24h_all(market)
    quote_volume_24h_by_symbol: dict[str, float] = {}
    ranked: list[tuple[float, str]] = []
    for row in tickers:
        sym = row.get("symbol")
        if not sym or not isinstance(sym, str) or not sym.endswith("USDT"):
            continue
        try:
            qv = float(row.get("quoteVolume") or 0.0)
        except Exception:
            qv = 0.0
        quote_volume_24h_by_symbol[sym] = qv
        ranked.append((qv, sym))
    ranked.sort(key=lambda x: x[0], reverse=True)
    symbols = [s for _, s in ranked[: max(20, int(top_n))]]
    symbols = filter_excluded_symbols(symbols)
    if not symbols:
        return

    assets = sorted({s[:-4] for s in symbols if s.endswith("USDT")})
    market_cap_by_symbol: dict[str, float] = {}
    if assets:
        cap_rows = await query_market_caps(assets=assets)
        for r in cap_rows:
            cap_value = _to_float(r.market_cap_usd)
            if cap_value is not None and cap_value > 0:
                market_cap_by_symbol[f"{str(r.asset).upper()}USDT"] = cap_value

    ob_rows = await query_orderbook_features(market=market, symbols=symbols, bucket="1m", start_ms=full_start_ms)
    tr_rows = await query_trade_buckets(market=market, symbols=symbols, bucket="1m", start_ms=full_start_ms)

    ob_by_symbol: dict[str, dict[int, OBFeatureRow]] = {}
    tr_by_symbol: dict[str, dict[int, TradeBucketRow]] = {}
    for row in ob_rows:
        ob_by_symbol.setdefault(row.symbol, {})[int(row.bucket_start_ms)] = row
    for row in tr_rows:
        tr_by_symbol.setdefault(row.symbol, {})[int(row.bucket_start_ms)] = row

    values: list[dict[str, Any]] = []
    for sym in symbols:
        merged_rows = _build_merged_rows(ob_by_symbol.get(sym, {}), tr_by_symbol.get(sym, {}))
        if len(merged_rows) < 20:
            continue
        for zone_type in ("bid", "ask"):
            snap = _score_zone(
                merged_rows,
                zone_type,
                liquidity_24h_quote=quote_volume_24h_by_symbol.get(sym),
                market_cap_usd=market_cap_by_symbol.get(sym),
            )
            if not snap:
                continue
            values.append(
                {
                    "market": market,
                    "symbol": sym,
                    "bucket_start_ms": bucket_start_ms,
                    "zone_type": zone_type,
                    "zone_low": snap["zone_low"],
                    "zone_high": snap["zone_high"],
                    "real_score": snap["real_score"],
                    "signal_state": snap["signal_state"],
                    "persistence_score": snap["persistence_score"],
                    "absorb_score": snap["absorb_score"],
                    "replenish_score": snap["replenish_score"],
                    "defend_score": snap["defend_score"],
                    "flow_align_score": snap["flow_align_score"],
                    "size_score": snap["size_score"],
                    "cancel_penalty": snap["cancel_penalty"],
                    "reasons": snap["reasons"],
                }
            )

    if values:
        stmt = insert(InstitutionalLevelSnapshot).values(values)
        stmt = stmt.on_conflict_do_update(
            index_elements=["market", "symbol", "bucket_start_ms", "zone_type"],
            set_={
                "zone_low": stmt.excluded.zone_low,
                "zone_high": stmt.excluded.zone_high,
                "real_score": stmt.excluded.real_score,
                "signal_state": stmt.excluded.signal_state,
                "persistence_score": stmt.excluded.persistence_score,
                "absorb_score": stmt.excluded.absorb_score,
                "replenish_score": stmt.excluded.replenish_score,
                "defend_score": stmt.excluded.defend_score,
                "flow_align_score": stmt.excluded.flow_align_score,
                "size_score": stmt.excluded.size_score,
                "cancel_penalty": stmt.excluded.cancel_penalty,
                "reasons": stmt.excluded.reasons,
            },
        )
        if session is not None:
            await session.execute(stmt)
        else:
            async with write_session() as s:
                await s.execute(stmt)
                await s.commit()


def _apply_state_filter(state: str):
    state = (state or "CONFIRM").upper()
    if state == "ALL":
        return None
    if state in {"WATCH", "CONFIRM"}:
        return InstitutionalLevelSnapshot.signal_state.in_(["CONFIRM", "STRONG"])
    if state == "STRONG":
        return InstitutionalLevelSnapshot.signal_state == "STRONG"
    return InstitutionalLevelSnapshot.signal_state.in_(["CONFIRM", "STRONG"])


async def list_latest_institutional_levels(
    market: str = "both",
    state: str = "CONFIRM",
    limit: int = 100,
    lookback_minutes: int = 360,
) -> list[InstitutionalLevelSnapshot]:
    now_ms = int(time.time() * 1000)
    lookback_start_ms = now_ms - max(15, int(lookback_minutes)) * 60 * 1000
    markets = ["spot", "swap"] if market == "both" else [market]
    rows: list[InstitutionalLevelSnapshot] = []
    state_filter = _apply_state_filter(state)

    async with SessionLocal() as session:
        for one_market in markets:
            latest_bucket_subq = (
                select(
                    InstitutionalLevelSnapshot.symbol.label("symbol"),
                    InstitutionalLevelSnapshot.zone_type.label("zone_type"),
                    func.max(InstitutionalLevelSnapshot.bucket_start_ms).label("bucket_start_ms"),
                )
                .where(
                    and_(
                        InstitutionalLevelSnapshot.market == one_market,
                        InstitutionalLevelSnapshot.bucket_start_ms >= lookback_start_ms,
                    )
                )
                .group_by(InstitutionalLevelSnapshot.symbol, InstitutionalLevelSnapshot.zone_type)
                .subquery()
            )

            stmt = (
                select(InstitutionalLevelSnapshot)
                .join(
                    latest_bucket_subq,
                    and_(
                        InstitutionalLevelSnapshot.symbol == latest_bucket_subq.c.symbol,
                        InstitutionalLevelSnapshot.zone_type == latest_bucket_subq.c.zone_type,
                        InstitutionalLevelSnapshot.bucket_start_ms == latest_bucket_subq.c.bucket_start_ms,
                    ),
                )
                .where(InstitutionalLevelSnapshot.market == one_market)
            )
            if state_filter is not None:
                stmt = stmt.where(state_filter)
            stmt = stmt.order_by(
                desc(InstitutionalLevelSnapshot.bucket_start_ms),
                desc(InstitutionalLevelSnapshot.real_score),
            ).limit(limit)
            rows.extend((await session.execute(stmt)).scalars().all())

    rows.sort(
        key=lambda x: (
            _signal_rank(str(x.signal_state or "NONE")),
            float(x.real_score or 0.0),
            int(x.bucket_start_ms or 0),
        ),
        reverse=True,
    )
    rows = [r for r in rows if not is_excluded_symbol(getattr(r, "symbol", None))]
    return rows[: int(limit)]


async def list_recent_triggered_institutional_levels(
    market: str = "both",
    limit: int = 100,
    lookback_minutes: int = 24 * 60,
) -> list[InstitutionalLevelSnapshot]:
    now_ms = int(time.time() * 1000)
    lookback_start_ms = now_ms - max(30, int(lookback_minutes)) * 60 * 1000
    markets = ["spot", "swap"] if market == "both" else [market]
    rows: list[InstitutionalLevelSnapshot] = []

    async with SessionLocal() as session:
        for one_market in markets:
            latest_signal_subq = (
                select(
                    InstitutionalLevelSnapshot.symbol.label("symbol"),
                    InstitutionalLevelSnapshot.zone_type.label("zone_type"),
                    func.max(InstitutionalLevelSnapshot.bucket_start_ms).label("bucket_start_ms"),
                )
                .where(
                    and_(
                        InstitutionalLevelSnapshot.market == one_market,
                        InstitutionalLevelSnapshot.bucket_start_ms >= lookback_start_ms,
                        InstitutionalLevelSnapshot.signal_state.in_(["CONFIRM", "STRONG"]),
                    )
                )
                .group_by(InstitutionalLevelSnapshot.symbol, InstitutionalLevelSnapshot.zone_type)
                .subquery()
            )

            stmt = (
                select(InstitutionalLevelSnapshot)
                .join(
                    latest_signal_subq,
                    and_(
                        InstitutionalLevelSnapshot.symbol == latest_signal_subq.c.symbol,
                        InstitutionalLevelSnapshot.zone_type == latest_signal_subq.c.zone_type,
                        InstitutionalLevelSnapshot.bucket_start_ms == latest_signal_subq.c.bucket_start_ms,
                    ),
                )
                .where(InstitutionalLevelSnapshot.market == one_market)
                .order_by(
                    desc(InstitutionalLevelSnapshot.bucket_start_ms),
                    desc(InstitutionalLevelSnapshot.real_score),
                )
                .limit(limit)
            )
            rows.extend((await session.execute(stmt)).scalars().all())

    rows.sort(
        key=lambda x: (
            _signal_rank(str(x.signal_state or "NONE")),
            float(x.real_score or 0.0),
            int(x.bucket_start_ms or 0),
        ),
        reverse=True,
    )
    rows = [r for r in rows if not is_excluded_symbol(getattr(r, "symbol", None))]
    return rows[: int(limit)]


async def get_symbol_latest_institutional_levels(
    *,
    market: str,
    symbol: str,
    lookback_minutes: int = 24 * 60,
    top_k: int = 3,
) -> dict[str, Any]:
    if is_excluded_symbol(symbol):
        return {
            "topBidZones": [],
            "topAskZones": [],
            "continuationState": {"active": False, "state": "NONE"},
            "riskFlags": ["EXCLUDED_SYMBOL"],
            "ts": int(time.time() * 1000),
        }

    now_ms = int(time.time() * 1000)
    lookback_start_ms = now_ms - max(30, int(lookback_minutes)) * 60 * 1000
    async with SessionLocal() as session:
        rows = (
            (
                await session.execute(
                    select(InstitutionalLevelSnapshot)
                    .where(
                        and_(
                            InstitutionalLevelSnapshot.market == market,
                            InstitutionalLevelSnapshot.symbol == symbol,
                            InstitutionalLevelSnapshot.bucket_start_ms >= lookback_start_ms,
                        )
                    )
                    .order_by(desc(InstitutionalLevelSnapshot.bucket_start_ms), desc(InstitutionalLevelSnapshot.real_score))
                )
            )
            .scalars()
            .all()
        )

    if not rows:
        return {
            "topBidZones": [],
            "topAskZones": [],
            "continuationState": {"active": False, "state": "NONE"},
            "riskFlags": ["NO_DATA"],
            "ts": now_ms,
        }

    latest_ts = int(max(int(r.bucket_start_ms) for r in rows))
    latest_rows = [r for r in rows if int(r.bucket_start_ms) == latest_ts]

    def _serialize(row: InstitutionalLevelSnapshot) -> dict[str, Any]:
        return {
            "zoneType": row.zone_type,
            "zoneLow": _to_float(row.zone_low),
            "zoneHigh": _to_float(row.zone_high),
            "realScore": round(float(row.real_score or 0.0), 1),
            "state": row.signal_state,
            "reasons": row.reasons or [],
            "scores": {
                "persistence": round(float(row.persistence_score or 0.0), 1),
                "absorb": round(float(row.absorb_score or 0.0), 1),
                "replenish": round(float(row.replenish_score or 0.0), 1),
                "defend": round(float(row.defend_score or 0.0), 1),
                "flowAlign": round(float(row.flow_align_score or 0.0), 1),
                "size": round(float(row.size_score or 0.0), 1),
                "cancelPenalty": round(float(row.cancel_penalty or 0.0), 1),
            },
            "ts": int(row.bucket_start_ms),
        }

    bid_rows = [r for r in latest_rows if str(r.zone_type) == "bid"]
    ask_rows = [r for r in latest_rows if str(r.zone_type) == "ask"]
    bid_rows.sort(key=lambda x: float(x.real_score or 0.0), reverse=True)
    ask_rows.sort(key=lambda x: float(x.real_score or 0.0), reverse=True)

    recent120 = rows[:240]  # bid+ask 双边，约 120 分钟
    continuation_state: SignalState = "NONE"
    for expected in ("STRONG", "CONFIRM", "WATCH"):
        if any(str(r.signal_state) == expected for r in recent120):
            continuation_state = expected
            break

    risk_flags: list[str] = []
    if continuation_state == "NONE":
        risk_flags.append("NO_ACTIVE_SIGNAL")

    for candidate in [*(bid_rows[:1]), *(ask_rows[:1])]:
        penalty = float(candidate.cancel_penalty or 0.0)
        zone_low = float(candidate.zone_low or 0.0)
        zone_high = float(candidate.zone_high or 0.0)
        mid = (zone_low + zone_high) / 2.0 if zone_low > 0 and zone_high > 0 else 0.0
        width_bps = ((zone_high - zone_low) / mid * 10000.0) if mid > 0 else 0.0
        if penalty >= 60.0 and "HIGH_CANCEL_PENALTY" not in risk_flags:
            risk_flags.append("HIGH_CANCEL_PENALTY")
        if width_bps >= 45.0 and "WIDE_ZONE" not in risk_flags:
            risk_flags.append("WIDE_ZONE")

    return {
        "topBidZones": [_serialize(r) for r in bid_rows[: max(1, int(top_k))]],
        "topAskZones": [_serialize(r) for r in ask_rows[: max(1, int(top_k))]],
        "continuationState": {
            "active": continuation_state != "NONE",
            "state": continuation_state,
        },
        "riskFlags": risk_flags,
        "ts": latest_ts,
    }
