from __future__ import annotations

import time
from typing import Any

from sqlalchemy import and_, delete, desc, func, select

from coinmark_api.ch import query_orderbook_features, query_trade_buckets
from coinmark_api.db import SessionLocal
from coinmark_api.db_upsert import insert
from coinmark_api.models import AbsorptionSignalSnapshot
from coinmark_api.services.binance.rest import get_ticker_24h_all
from coinmark_api.services.symbol_filter import filter_excluded_symbols, is_excluded_symbol


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


def _window_eval(
    rows: list[dict],
    spread_values: list[float],
    minutes: int,
    side: str,
    persistence_threshold: float = 0.60,
    impact_threshold_pct: float = 0.25,
    thin_threshold: float = 0.60,
    replenish_threshold: float = 55.0,
) -> dict[str, Any]:
    win = rows[-minutes:] if len(rows) >= minutes else rows[:]
    if not win:
        return {
            "passed": False,
            "buyPersistenceRatio": 0.0,
            "sellPersistenceRatio": 0.0,
            "buyPersistenceThreshold": persistence_threshold,
            "netRetAbsPct": 0.0,
            "netRetAbsThresholdPct": impact_threshold_pct,
            "spreadThinRank": None,
            "thinThreshold": thin_threshold,
            "replenishAvg": None,
            "replenishThreshold": replenish_threshold,
        }

    buy_flags = [
        1
        for r in win
        if (r.get("aggrBuyRatio") is not None and float(r.get("aggrBuyRatio")) >= 0.58 and float(r.get("netBuyNotional") or 0.0) > 0)
    ]
    sell_flags = [
        1
        for r in win
        if (r.get("aggrBuyRatio") is not None and float(r.get("aggrBuyRatio")) <= 0.42 and float(r.get("netBuyNotional") or 0.0) < 0)
    ]
    buy_persistence_ratio = len(buy_flags) / len(win)
    sell_persistence_ratio = len(sell_flags) / len(win)
    persistence_ratio = buy_persistence_ratio if side == "LONG_BIAS" else sell_persistence_ratio

    net_ret_abs_pct = abs(sum(float(r.get("ret1m") or 0.0) for r in win)) * 100.0
    last_spread = next((r.get("spreadBps") for r in reversed(win) if r.get("spreadBps") is not None), None)
    spread_rank = _percentile_rank(spread_values, float(last_spread)) if last_spread is not None else None
    replenish_vals = [float(r["replenishScore"]) for r in win if r.get("replenishScore") is not None]
    replenish_avg = _safe_mean(replenish_vals)

    thin_ok = spread_rank is not None and spread_rank >= thin_threshold
    replenish_ok = replenish_avg is not None and replenish_avg >= replenish_threshold
    passed = persistence_ratio >= persistence_threshold and net_ret_abs_pct <= impact_threshold_pct and (thin_ok or replenish_ok)

    return {
        "passed": passed,
        "buyPersistenceRatio": round(buy_persistence_ratio, 4),
        "sellPersistenceRatio": round(sell_persistence_ratio, 4),
        "buyPersistenceThreshold": persistence_threshold,
        "netRetAbsPct": round(net_ret_abs_pct, 4),
        "netRetAbsThresholdPct": impact_threshold_pct,
        "spreadThinRank": None if spread_rank is None else round(spread_rank, 4),
        "thinThreshold": thin_threshold,
        "replenishAvg": None if replenish_avg is None else round(replenish_avg, 2),
        "replenishThreshold": replenish_threshold,
    }


def _score_window(w: dict[str, Any], side: str) -> float:
    persistence = float(w.get("buyPersistenceRatio") or 0.0) if side == "LONG_BIAS" else float(w.get("sellPersistenceRatio") or 0.0)
    persistence_threshold = max(1e-9, float(w.get("buyPersistenceThreshold") or 0.6))
    impact_threshold_pct = max(1e-9, float(w.get("netRetAbsThresholdPct") or 0.25))
    thin_threshold = max(1e-9, float(w.get("thinThreshold") or 0.6))
    replenish_threshold = max(1e-9, float(w.get("replenishThreshold") or 55.0))
    s1 = _clamp((persistence / persistence_threshold) * 40.0, 0.0, 40.0)
    s2 = _clamp(((impact_threshold_pct - float(w.get("netRetAbsPct") or 0.0)) / impact_threshold_pct) * 35.0, 0.0, 35.0)
    s3 = max(
        0.0 if w.get("spreadThinRank") is None else _clamp((float(w["spreadThinRank"]) / thin_threshold) * 25.0, 0.0, 25.0),
        0.0 if w.get("replenishAvg") is None else _clamp((float(w["replenishAvg"]) / replenish_threshold) * 25.0, 0.0, 25.0),
    )
    return round(_clamp(s1 + s2 + s3, 0.0, 100.0), 1)


async def refresh_absorption_signal_snapshots(market: str = "swap", top_n: int = 200) -> None:
    now_ms = int(time.time() * 1000)
    full_start_ms = now_ms - 3 * 24 * 60 * 60 * 1000
    bucket_start_ms = (now_ms // 60_000) * 60_000

    tickers = await get_ticker_24h_all(market)
    ranked: list[tuple[float, str]] = []
    for row in tickers:
        sym = row.get("symbol")
        if not sym or not isinstance(sym, str) or not sym.endswith("USDT"):
            continue
        try:
            qv = float(row.get("quoteVolume") or 0.0)
        except Exception:
            qv = 0.0
        ranked.append((qv, sym))
    ranked.sort(key=lambda x: x[0], reverse=True)
    symbols = [s for _, s in ranked[: max(20, int(top_n))]]
    symbols = filter_excluded_symbols(symbols)
    if not symbols:
        return

    ob_rows = await query_orderbook_features(market=market, symbols=symbols, bucket="1m", start_ms=full_start_ms)
    tr_rows = await query_trade_buckets(market=market, symbols=symbols, bucket="1m", start_ms=full_start_ms)

    ob_by_symbol: dict[str, dict[int, Any]] = {}
    tr_by_symbol: dict[str, dict[int, Any]] = {}
    for row in ob_rows:
        ob_by_symbol.setdefault(row.symbol, {})[int(row.bucket_start_ms)] = row
    for row in tr_rows:
        tr_by_symbol.setdefault(row.symbol, {})[int(row.bucket_start_ms)] = row

    values: list[dict[str, Any]] = []
    for sym in symbols:
        ob_map = ob_by_symbol.get(sym, {})
        tr_map = tr_by_symbol.get(sym, {})
        timeline = sorted(set(ob_map.keys()) | set(tr_map.keys()))
        if len(timeline) < 240:
            continue

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
            if ob:
                dep = int(ob.depletion_events or 0)
                rep = int(ob.replenishment_events or 0)
                replenish_score = 50.0 if dep <= 0 else _clamp(rep / dep * 100.0, 0.0, 100.0)

            close_price = _to_float(tr.close_price) if tr else None
            rows.append(
                {
                    "ts": ts,
                    "netBuyNotional": net,
                    "aggrBuyRatio": aggr_buy_ratio,
                    "spreadBps": spread_bps,
                    "replenishScore": replenish_score,
                    "closePrice": close_price,
                }
            )

        for i in range(len(rows)):
            prev = rows[i - 1] if i > 0 else None
            cur_close = rows[i].get("closePrice")
            prev_close = prev.get("closePrice") if prev else None
            if cur_close is not None and prev_close is not None and prev_close > 0:
                rows[i]["ret1m"] = cur_close / prev_close - 1.0
            else:
                rows[i]["ret1m"] = 0.0

        spread_values = [float(r["spreadBps"]) for r in rows if r.get("spreadBps") is not None]

        for side in ("LONG_BIAS", "SHORT_BIAS"):
            w4h = _window_eval(rows, spread_values, 240, side, persistence_threshold=0.60, impact_threshold_pct=0.35)
            w1d = _window_eval(rows, spread_values, 1440, side, persistence_threshold=0.55, impact_threshold_pct=0.80)
            w3d = _window_eval(rows, spread_values, 4320, side, persistence_threshold=0.50, impact_threshold_pct=1.60)
            s4h = _score_window(w4h, side)
            s1d = _score_window(w1d, side)
            s3d = _score_window(w3d, side)

            state = "NONE"
            if w3d["passed"] and s3d >= 78:
                state = "STRONG"
            elif w1d["passed"] and s1d >= 65:
                state = "CONFIRM"
            elif w4h["passed"] and s4h >= 55:
                state = "WATCH"

            score = 0.0
            continuation_reason: str | None = None
            if state == "NONE":
                recent4h = rows[-240:] if rows else []
                recent12h = rows[-720:] if len(rows) >= 720 else rows
                flow4h = _safe_mean([float(r.get("netBuyNotional") or 0.0) for r in recent4h]) or 0.0
                flow12h = _safe_mean([float(r.get("netBuyNotional") or 0.0) for r in recent12h]) or 0.0
                persistence_1d = float(w1d.get("buyPersistenceRatio") or 0.0) if side == "LONG_BIAS" else float(w1d.get("sellPersistenceRatio") or 0.0)
                continuation_ok = (
                    flow4h > 0 and flow12h > 0 if side == "LONG_BIAS" else flow4h < 0 and flow12h < 0
                ) and persistence_1d >= 0.50 and float(w1d.get("netRetAbsPct") or 99.0) <= 1.20
                if continuation_ok and max(s4h, s1d) >= 52:
                    state = "WATCH"
                    score = max(52.0, min(68.0, max(s4h, s1d)))
                    continuation_reason = "FLOW_CONTINUATION_12H"

            if state == "WATCH":
                score = max(score, s4h)
            elif state == "CONFIRM":
                score = max(s4h, s1d)
            elif state == "STRONG":
                score = max(s4h, s1d, s3d)

            impact_vals: list[float] = []
            for r in rows[-20:]:
                net = float(r.get("netBuyNotional") or 0.0)
                ret = float(r.get("ret1m") or 0.0)
                if abs(net) > 1e-9:
                    impact_vals.append(abs(ret) / abs(net))
            impact_avg = _safe_mean(impact_vals)

            net_strength = _safe_mean([float(r.get("netBuyNotional") or 0.0) for r in rows[-20:]])

            reasons: list[str] = []
            if w4h["passed"]:
                reasons.append("4h通过")
            if w1d["passed"]:
                reasons.append("1d通过")
            if w3d["passed"]:
                reasons.append("3d通过")
            if not reasons:
                reasons.append("未触发")

            if continuation_reason:
                reasons.append(continuation_reason)

            values.append(
                {
                    "market": market,
                    "symbol": sym,
                    "bucket_start_ms": bucket_start_ms,
                    "direction": side,
                    "signal_state": state,
                    "score": score,
                    "net_flow_strength": net_strength,
                    "impact_per_notional": impact_avg,
                    "window_4h_passed": bool(w4h["passed"]),
                    "window_1d_passed": bool(w1d["passed"]),
                    "window_3d_passed": bool(w3d["passed"]),
                    "windows": {
                        "4h": {**w4h, "score": s4h},
                        "1d": {**w1d, "score": s1d},
                        "3d": {**w3d, "score": s3d},
                    },
                    "reasons": reasons,
                }
            )

    if values:
        async with SessionLocal() as session:
            stmt = insert(AbsorptionSignalSnapshot).values(values)
            stmt = stmt.on_conflict_do_update(
                index_elements=["market", "symbol", "bucket_start_ms", "direction"],
                set_={
                    "signal_state": stmt.excluded.signal_state,
                    "score": stmt.excluded.score,
                    "net_flow_strength": stmt.excluded.net_flow_strength,
                    "impact_per_notional": stmt.excluded.impact_per_notional,
                    "window_4h_passed": stmt.excluded.window_4h_passed,
                    "window_1d_passed": stmt.excluded.window_1d_passed,
                    "window_3d_passed": stmt.excluded.window_3d_passed,
                    "windows": stmt.excluded.windows,
                    "reasons": stmt.excluded.reasons,
                },
            )
            await session.execute(stmt)
            await session.commit()


async def cleanup_absorption_signal_snapshots(retention_hours: int = 24) -> int:
    now_ms = int(time.time() * 1000)
    cutoff_ms = now_ms - max(1, int(retention_hours)) * 60 * 60 * 1000
    async with SessionLocal() as session:
        stmt = delete(AbsorptionSignalSnapshot).where(AbsorptionSignalSnapshot.bucket_start_ms < cutoff_ms)
        res = await session.execute(stmt)
        await session.commit()
        try:
            return int(res.rowcount or 0)
        except Exception:
            return 0


async def list_latest_absorption_signals(
    market: str = "swap",
    only_signals: bool = True,
    limit: int = 100,
    signal_lookback_minutes: int = 3 * 24 * 60,
    direction: str = "all",
) -> list[AbsorptionSignalSnapshot]:
    async with SessionLocal() as session:
        now_ms = int(time.time() * 1000)
        lookback_start_ms = now_ms - max(15, int(signal_lookback_minutes)) * 60 * 1000
        latest_where = [AbsorptionSignalSnapshot.market == market]
        normalized_direction = (direction or "all").lower()
        if normalized_direction == "long":
            latest_where.append(AbsorptionSignalSnapshot.direction == "LONG_BIAS")
        elif normalized_direction == "short":
            latest_where.append(AbsorptionSignalSnapshot.direction == "SHORT_BIAS")
        if only_signals:
            # 历史触发模式：在回看窗口内先筛出非 NONE，再取每个 symbol/direction 的最近一条触发记录。
            latest_where.append(AbsorptionSignalSnapshot.signal_state != "NONE")
            latest_where.append(AbsorptionSignalSnapshot.bucket_start_ms >= lookback_start_ms)

        latest_bucket_subq = (
            select(
                AbsorptionSignalSnapshot.symbol.label("symbol"),
                AbsorptionSignalSnapshot.direction.label("direction"),
                func.max(AbsorptionSignalSnapshot.bucket_start_ms).label("bucket_start_ms"),
            )
            .where(and_(*latest_where))
            .group_by(AbsorptionSignalSnapshot.symbol, AbsorptionSignalSnapshot.direction)
            .subquery()
        )

        stmt = (
            select(AbsorptionSignalSnapshot)
            .join(
                latest_bucket_subq,
                and_(
                    AbsorptionSignalSnapshot.symbol == latest_bucket_subq.c.symbol,
                    AbsorptionSignalSnapshot.direction == latest_bucket_subq.c.direction,
                    AbsorptionSignalSnapshot.bucket_start_ms == latest_bucket_subq.c.bucket_start_ms,
                ),
            )
            .where(AbsorptionSignalSnapshot.market == market)
        )
        if normalized_direction == "long":
            stmt = stmt.where(AbsorptionSignalSnapshot.direction == "LONG_BIAS")
        elif normalized_direction == "short":
            stmt = stmt.where(AbsorptionSignalSnapshot.direction == "SHORT_BIAS")
        stmt = stmt.order_by(
            desc(AbsorptionSignalSnapshot.bucket_start_ms),
            desc(AbsorptionSignalSnapshot.score),
        ).limit(limit)

        rows = (await session.execute(stmt)).scalars().all()
    return [r for r in rows if not is_excluded_symbol(getattr(r, "symbol", None))]
