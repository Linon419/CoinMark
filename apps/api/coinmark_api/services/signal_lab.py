from __future__ import annotations

import asyncio
import math
import time
import uuid
from collections import defaultdict, deque
from dataclasses import dataclass
from typing import Any, Literal

from sqlalchemy import and_, func, select

from coinmark_api.ch import (
    query_market_caps,
    query_orderbook_features,
    query_trade_agg_volume,
    query_trade_buckets,
)

from coinmark_api.db import SessionLocal, write_session
from coinmark_api.models import AnomalyEvent

Market = Literal["spot", "swap"]
MarketScope = Literal["spot", "swap", "both"]

_SIGNAL_EVENT_TYPE = "signal_lab_persistent_buy"
_SIGNAL_EVENT_TYPE_SINGLE = "signal_lab_single_large"
_SIGNAL_EVENT_TYPE_CLIMAX_SHORT = "signal_lab_climax_short"
_SIGNAL_EVENT_TYPE_CLIMAX_LONG = "signal_lab_climax_long"


@dataclass(slots=True)
class SignalLabParams:
    bucket: str = "1h"
    z_threshold: float = 2.8
    lookback_minutes: int = 4320
    detection_window_minutes: int = 1440
    min_large_count: int = 3
    buy_ratio_threshold: float = 0.8
    min_persistent_span_minutes: int = 180
    min_avg_interval_minutes: int = 60
    min_distinct_time_buckets: int = 3
    forecast_horizon_minutes: int = 240
    cooldown_minutes: int = 720
    single_large_z_threshold: float = 3.5
    single_large_min_notional: float = 10000.0
    single_large_cooldown_minutes: int = 240
    slope_window_minutes: int = 720
    slope_r2_threshold: float = 0.7
    symbol_limit: int = 200


@dataclass(slots=True)
class BucketPoint:
    ts: int
    close: float
    buy: float
    sell: float

    @property
    def net(self) -> float:
        return self.buy - self.sell


def _bucket_minutes(bucket: str) -> int:
    b = bucket.strip().lower()
    if b.endswith("h"):
        return int(b[:-1]) * 60
    if b.endswith("d"):
        return int(b[:-1]) * 1440
    return int(b.rstrip("m") or "1")


def _clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


def _to_float(v: Any) -> float:
    try:
        return float(v or 0.0)
    except Exception:
        return 0.0


def _cumsum(vals: list[float]) -> list[float]:
    out: list[float] = []
    s = 0.0
    for v in vals:
        s += v
        out.append(s)
    return out


def _linreg(ys: list[float]) -> tuple[float, float]:
    """Least-squares on y values at x=0,1,...,n-1. Returns (slope, r²)."""
    n = len(ys)
    if n < 3:
        return 0.0, 0.0
    sum_x = n * (n - 1) / 2.0
    sum_x2 = n * (n - 1) * (2 * n - 1) / 6.0
    sum_y = 0.0
    sum_xy = 0.0
    for i, y in enumerate(ys):
        sum_y += y
        sum_xy += i * y
    denom = n * sum_x2 - sum_x * sum_x
    if abs(denom) < 1e-15:
        return 0.0, 0.0
    k = (n * sum_xy - sum_x * sum_y) / denom
    b = (sum_y - k * sum_x) / n
    y_mean = sum_y / n
    ss_tot = 0.0
    ss_res = 0.0
    for i, y in enumerate(ys):
        ss_tot += (y - y_mean) ** 2
        ss_res += (y - (k * i + b)) ** 2
    if ss_tot < 1e-15:
        return 0.0, 0.0
    r2 = 1.0 - ss_res / ss_tot
    return k, max(0.0, r2)


def _markets(scope: MarketScope) -> list[Market]:
    if scope == "both":
        return ["spot", "swap"]
    return [scope]


def _signal_state(score: float) -> str:
    if score >= 85:
        return "STRONG"
    if score >= 70:
        return "CONFIRM"
    if score >= 55:
        return "WATCH"
    return "NONE"


def _signal_state_rank(state: str) -> int:
    s = (state or "").upper()
    if s in {"STRONG", "HIGH"}:
        return 3
    if s == "CONFIRM":
        return 2
    if s == "WATCH":
        return 1
    return 0


def _mcap_impact_adj(flow_usdt: float, mcap_usd: float) -> float:
    """Score adjustment based on flow / market_cap ratio. Range: -10 .. +15."""
    if mcap_usd <= 0 or flow_usdt <= 0:
        return 0.0
    ratio = flow_usdt / mcap_usd
    if ratio < 1e-9:
        return -10.0
    log_r = math.log10(ratio)
    # log_r  -6 (0.0001%) → adj -6.25
    # log_r  -5 (0.001%)  → adj  0      (neutral baseline)
    # log_r  -4 (0.01%)   → adj  6.25
    # log_r  -3 (0.1%)    → adj 12.5
    # log_r  -2 (1%)      → adj 15 (cap)
    return _clamp((log_r + 5.0) * 6.25, -10.0, 15.0)


def _score_signal(z_score: float, buy_ratio: float, large_buy_count: int) -> float:
    z_part = _clamp((z_score - 2.0) * 15.0, 0.0, 30.0)
    ratio_part = _clamp((buy_ratio - 0.5) * 100.0, 0.0, 30.0)
    count_part = _clamp(float(large_buy_count) * 4.0, 0.0, 20.0)
    base = 35.0
    return round(_clamp(base + z_part + ratio_part + count_part, 0.0, 100.0), 2)


def _score_single_large(z_score: float, slope_confirms: bool) -> float:
    z_part = _clamp((z_score - 2.0) * 20.0, 0.0, 50.0)
    slope_part = 10.0 if slope_confirms else 0.0
    base = 40.0
    return round(_clamp(base + z_part + slope_part, 0.0, 100.0), 2)


def _scan_symbol_signals(symbol: str, market: Market, rows: list[BucketPoint], params: SignalLabParams, *, mcap_usd: float = 0.0) -> list[dict[str, Any]]:
    if not rows:
        return []

    lookback = max(60, int(params.lookback_minutes))
    detect_window_ms = max(15, int(params.detection_window_minutes)) * 60 * 1000
    min_persistent_span_ms = max(1, int(params.min_persistent_span_minutes)) * 60 * 1000
    min_avg_interval_ms = max(1, int(params.min_avg_interval_minutes)) * 60 * 1000
    bkt_min = _bucket_minutes(params.bucket)
    distinct_bucket_ms = max(bkt_min * 4, 10) * 60 * 1000
    cooldown_ms = max(1, int(params.cooldown_minutes)) * 60 * 1000
    single_cooldown_ms = max(1, int(params.single_large_cooldown_minutes)) * 60 * 1000
    single_z_thr = float(params.single_large_z_threshold)
    single_min_notional = float(params.single_large_min_notional)
    slope_window = max(10, int(params.slope_window_minutes))
    slope_r2_thr = float(params.slope_r2_threshold)

    hist = deque()
    hist_sum = 0.0
    hist_sumsq = 0.0

    large_events = deque()  # (ts, side, abs_net)
    net_flow_buf: deque[float] = deque()  # rolling net flows for slope
    signals: list[dict[str, Any]] = []
    last_persistent_ts = 0
    last_single_ts = 0

    for row in rows:
        x = abs(row.net)

        net_flow_buf.append(row.net)
        if len(net_flow_buf) > slope_window:
            net_flow_buf.popleft()

        n = len(hist)
        z_score = 0.0
        if n >= 20:
            mean = hist_sum / n
            var = max(0.0, (hist_sumsq / n) - (mean * mean))
            std = math.sqrt(var)
            if std > 1e-9:
                z_score = (x - mean) / std

        min_ts = int(row.ts) - detect_window_ms
        while large_events and large_events[0][0] < min_ts:
            large_events.popleft()

        row_slope_k, row_slope_r2 = 0.0, 0.0

        if z_score >= float(params.z_threshold):
            side = "buy" if row.net > 0 else "sell"
            large_events.append((int(row.ts), side, x))

            if len(net_flow_buf) >= 10:
                row_slope_k, row_slope_r2 = _linreg(_cumsum(list(net_flow_buf)))

            # --- SINGLE_LARGE_BUY / SINGLE_LARGE_SELL ---
            if z_score >= single_z_thr and x >= single_min_notional and int(row.ts) - last_single_ts >= single_cooldown_ms:
                slope_confirms_sl = (
                    row_slope_r2 >= slope_r2_thr
                    and ((side == "buy" and row_slope_k > 0) or (side == "sell" and row_slope_k < 0))
                )
                mcap_adj_sl = _mcap_impact_adj(abs(row.net), mcap_usd)
                sl_score = _score_single_large(z_score, slope_confirms_sl)
                sl_score = round(_clamp(sl_score + mcap_adj_sl, 0.0, 100.0), 2)
                flow_mcap_pct = abs(row.net) / mcap_usd * 100.0 if mcap_usd > 0 else 0.0
                signals.append(
                    {
                        "market": market,
                        "symbol": symbol,
                        "ts": int(row.ts),
                        "close": float(row.close),
                        "netFlow": float(row.net),
                        "zScore": round(float(z_score), 4),
                        "direction": side,
                        "slopeK": round(float(row_slope_k), 6),
                        "slopeR2": round(float(row_slope_r2), 4),
                        "slopeConfirms": slope_confirms_sl,
                        "flowMcapPct": round(flow_mcap_pct, 6),
                        "mcapAdj": round(mcap_adj_sl, 2),
                        "score": sl_score,
                        "signalState": _signal_state(sl_score),
                        "eventType": _SIGNAL_EVENT_TYPE_SINGLE,
                    }
                )
                last_single_ts = int(row.ts)

        buy_amt = sum(v for _, s, v in large_events if s == "buy")
        sell_amt = sum(v for _, s, v in large_events if s == "sell")
        total_amt = buy_amt + sell_amt
        buy_ratio = buy_amt / total_amt if total_amt > 0 else 0.0
        large_buy_count = sum(1 for _, s, _ in large_events if s == "buy")
        buy_event_ts = [t for t, s, _ in large_events if s == "buy"]
        persistent_span_ms = (buy_event_ts[-1] - buy_event_ts[0]) if len(buy_event_ts) >= 2 else 0
        avg_interval_ms = (
            sum((buy_event_ts[i] - buy_event_ts[i - 1]) for i in range(1, len(buy_event_ts))) / (len(buy_event_ts) - 1)
            if len(buy_event_ts) >= 2
            else 0.0
        )
        distinct_time_buckets = len({int(t // distinct_bucket_ms) for t in buy_event_ts})

        is_trigger = (
            row.net > 0
            and z_score >= float(params.z_threshold)
            and large_buy_count >= int(params.min_large_count)
            and buy_ratio >= float(params.buy_ratio_threshold)
            and persistent_span_ms >= min_persistent_span_ms
            and avg_interval_ms >= min_avg_interval_ms
            and distinct_time_buckets >= int(params.min_distinct_time_buckets)
        )
        if not is_trigger:
            hist.append(x)
            hist_sum += x
            hist_sumsq += x * x
            if len(hist) > lookback:
                old = hist.popleft()
                hist_sum -= old
                hist_sumsq -= old * old
            continue

        if int(row.ts) - last_persistent_ts < cooldown_ms:
            hist.append(x)
            hist_sum += x
            hist_sumsq += x * x
            if len(hist) > lookback:
                old = hist.popleft()
                hist_sum -= old
                hist_sumsq -= old * old
            continue

        slope_confirms_pb = row_slope_r2 >= slope_r2_thr and row_slope_k > 0
        mcap_adj_pb = _mcap_impact_adj(buy_amt, mcap_usd)
        score = _score_signal(z_score, buy_ratio, large_buy_count)
        score = round(_clamp(score + (10.0 if slope_confirms_pb else 0.0) + mcap_adj_pb, 0.0, 100.0), 2)
        flow_mcap_pct_pb = buy_amt / mcap_usd * 100.0 if mcap_usd > 0 else 0.0
        signals.append(
            {
                "market": market,
                "symbol": symbol,
                "ts": int(row.ts),
                "close": float(row.close),
                "netFlow": float(row.net),
                "zScore": round(float(z_score), 4),
                "largeBuyCount": int(large_buy_count),
                "buyRatio": round(float(buy_ratio), 4),
                "persistentSpanMinutes": round(float(persistent_span_ms) / 60000.0, 2),
                "avgIntervalMinutes": round(float(avg_interval_ms) / 60000.0, 2),
                "distinctTimeBuckets": int(distinct_time_buckets),
                "slopeK": round(float(row_slope_k), 6),
                "slopeR2": round(float(row_slope_r2), 4),
                "slopeConfirms": slope_confirms_pb,
                "flowMcapPct": round(flow_mcap_pct_pb, 6),
                "mcapAdj": round(mcap_adj_pb, 2),
                "score": score,
                "signalState": _signal_state(score),
                "eventType": _SIGNAL_EVENT_TYPE,
            }
        )
        last_persistent_ts = int(row.ts)

        hist.append(x)
        hist_sum += x
        hist_sumsq += x * x
        if len(hist) > lookback:
            old = hist.popleft()
            hist_sum -= old
            hist_sumsq -= old * old

    return signals


async def _load_market_caps(symbols: list[str]) -> dict[str, float]:
    """Map trading symbol (e.g. BTCUSDT) → market_cap_usd."""
    asset_to_sym: dict[str, str] = {}
    for sym in symbols:
        for suffix in ("USDT", "USDC", "BUSD"):
            if sym.upper().endswith(suffix):
                asset_to_sym[sym[: -len(suffix)]] = sym
                break
    if not asset_to_sym:
        return {}
    rows = await query_market_caps(assets=list(asset_to_sym.keys()))
    return {asset_to_sym[str(r.asset)]: float(r.market_cap_usd) for r in rows if str(r.asset) in asset_to_sym and r.market_cap_usd}


async def _top_symbols(market: Market, symbol_limit: int, bucket: str = "1h") -> list[str]:
    since_ms = int(time.time() * 1000) - 24 * 60 * 60 * 1000
    rows = await query_trade_agg_volume(market=market, bucket="1m", start_ms=since_ms, limit=max(20, int(symbol_limit)))
    return [sym for sym, _ in rows if sym]


async def _load_rows(
    *,
    market: Market,
    symbols: list[str],
    start_ms: int,
    end_ms: int,
    bucket: str = "1h",
) -> dict[str, list[BucketPoint]]:
    if not symbols:
        return {}

    rows = await query_trade_buckets(market=market, symbols=symbols, bucket="1m", start_ms=int(start_ms), end_ms=int(end_ms))

    bkt_ms = _bucket_minutes(bucket) * 60 * 1000
    raw: dict[str, list] = defaultdict(list)
    for r in rows:
        raw[str(r.symbol)].append(r)

    grouped: dict[str, list[BucketPoint]] = {}
    for symbol, sym_rows in raw.items():
        if bkt_ms <= 60_000:
            grouped[symbol] = [
                BucketPoint(
                    ts=int(r.bucket_start_ms),
                    close=_to_float(r.close_price),
                    buy=_to_float(r.taker_buy_notional),
                    sell=_to_float(r.taker_sell_notional),
                )
                for r in sym_rows
            ]
        else:
            agg: dict[int, list[float]] = defaultdict(lambda: [0.0, 0.0, 0.0])
            for r in sym_rows:
                key = int(r.bucket_start_ms) // bkt_ms * bkt_ms
                b = agg[key]
                b[0] += _to_float(r.taker_buy_notional)
                b[1] += _to_float(r.taker_sell_notional)
                b[2] = _to_float(r.close_price)
            grouped[symbol] = sorted(
                [BucketPoint(ts=k, close=v[2], buy=v[0], sell=v[1]) for k, v in agg.items()],
                key=lambda p: p.ts,
            )
    return grouped


async def get_realtime_signals(
    *,
    market_scope: MarketScope,
    params: SignalLabParams,
    limit: int = 100,
    min_signal_state: str = "CONFIRM",
    sync_to_score_flow: bool = True,
) -> dict[str, Any]:
    now_ms = int(time.time() * 1000)
    start_ms = now_ms - max(24, params.lookback_minutes + params.detection_window_minutes) * 60 * 1000

    all_signals: list[dict[str, Any]] = []
    market_stats: dict[str, Any] = {}
    min_state_rank = _signal_state_rank(min_signal_state)

    for market in _markets(market_scope):
        symbols = await _top_symbols(market, params.symbol_limit, bucket=params.bucket)
        grouped = await _load_rows(market=market, symbols=symbols, start_ms=start_ms, end_ms=now_ms, bucket=params.bucket)
        mcap_map = await _load_market_caps(symbols)

        market_signals: list[dict[str, Any]] = []
        min_rows = max(20, 90 // max(1, _bucket_minutes(params.bucket)))
        for symbol, rows in grouped.items():
            if len(rows) < min_rows:
                continue
            items = _scan_symbol_signals(symbol, market, rows, params, mcap_usd=mcap_map.get(symbol, 0.0))
            if items:
                by_type: dict[str, dict[str, Any]] = {}
                for item in items:
                    by_type[str(item.get("eventType") or "")] = item
                for sig in by_type.values():
                    if _signal_state_rank(str(sig.get("signalState") or "")) >= min_state_rank:
                        market_signals.append(sig)

        market_signals.sort(key=lambda x: (float(x.get("score") or 0.0), int(x.get("ts") or 0)), reverse=True)
        market_signals = market_signals[: int(limit)]
        all_signals.extend(market_signals)

        market_stats[market] = {
            "symbols": len(symbols),
            "activeSignals": len(market_signals),
        }

    all_signals.sort(key=lambda x: (float(x.get("score") or 0.0), int(x.get("ts") or 0)), reverse=True)
    out_signals = all_signals[: int(limit)]

    inserted = 0
    if sync_to_score_flow and out_signals:
        async with write_session() as session:
            inserted = await _sync_anomaly_events(session, out_signals, cooldown_minutes=params.cooldown_minutes)

    return {
        "market": market_scope,
        "limit": int(limit),
        "minSignalState": (min_signal_state or "CONFIRM").upper(),
        "signals": out_signals,
        "stats": market_stats,
        "syncedToScoreFlow": bool(sync_to_score_flow),
        "insertedEvents": int(inserted),
        "eventType": _SIGNAL_EVENT_TYPE,
        "ts": now_ms,
    }


async def _sync_anomaly_events(session, signals: list[dict[str, Any]], cooldown_minutes: int) -> int:
    if not signals:
        return 0

    cutoff_ms = int(time.time() * 1000) - max(1, int(cooldown_minutes)) * 60 * 1000
    inserted = 0

    # 按 market 分批查询已存在事件，避免重复写入
    by_market: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for item in signals:
        by_market[str(item.get("market") or "")].append(item)

    for market, rows in by_market.items():
        symbols = sorted({str(x.get("symbol") or "") for x in rows if x.get("symbol")})
        if not symbols:
            continue

        event_types = sorted({str(x.get("eventType") or _SIGNAL_EVENT_TYPE) for x in rows})
        latest_map: dict[tuple[str, str], int] = {}
        for et in event_types:
            stmt = (
                select(AnomalyEvent.symbol, func.max(AnomalyEvent.event_time_ms).label("latest"))
                .where(
                    and_(
                        AnomalyEvent.market == market,
                        AnomalyEvent.event_type == et,
                        AnomalyEvent.symbol.in_(symbols),
                        AnomalyEvent.event_time_ms >= cutoff_ms,
                    )
                )
                .group_by(AnomalyEvent.symbol)
            )
            for sym, ts in (await session.execute(stmt)).all():
                latest_map[(str(sym), et)] = int(ts or 0)

        values: list[dict[str, Any]] = []
        for item in rows:
            signal_state = str(item.get("signalState") or "").upper()
            if signal_state not in {"CONFIRM", "STRONG", "HIGH"}:
                continue
            sym = str(item.get("symbol") or "")
            ts = int(item.get("ts") or 0)
            et = str(item.get("eventType") or _SIGNAL_EVENT_TYPE)
            if not sym or ts <= 0:
                continue
            prev = latest_map.get((sym, et), 0)
            if prev > 0 and ts - prev < max(1, int(cooldown_minutes)) * 60 * 1000:
                continue

            score = float(item.get("score") or 0.0)
            direction = str(item.get("direction") or "buy")
            if et == _SIGNAL_EVENT_TYPE_SINGLE:
                title = f"{sym} {'大额主动买入' if direction == 'buy' else '大额主动卖出'}信号"
            else:
                title = f"{sym} 资金持续吸筹信号"

            details: dict[str, Any] = {
                "signalState": item.get("signalState"),
                "score": score,
                "strengthScore": score,
                "zScore": item.get("zScore"),
                "netFlow": item.get("netFlow"),
                "slopeK": item.get("slopeK"),
                "slopeR2": item.get("slopeR2"),
                "slopeConfirms": item.get("slopeConfirms"),
            }
            if et == _SIGNAL_EVENT_TYPE_SINGLE:
                details["direction"] = direction
            else:
                details["buyRatio"] = item.get("buyRatio")
                details["largeBuyCount"] = item.get("largeBuyCount")
                details["persistentSpanMinutes"] = item.get("persistentSpanMinutes")
                details["avgIntervalMinutes"] = item.get("avgIntervalMinutes")
                details["distinctTimeBuckets"] = item.get("distinctTimeBuckets")

            values.append(
                {
                    "market": market,
                    "symbol": sym,
                    "event_type": et,
                    "tf_signal": "1m",
                    "tf_level": "1h",
                    "event_time_ms": ts,
                    "title": title,
                    "details": details,
                }
            )
            latest_map[(sym, et)] = ts

        if values:
            await session.execute(insert(AnomalyEvent).values(values))  # type: ignore[name-defined]
            inserted += len(values)

    if inserted > 0:
        await session.commit()
    return inserted


import logging as _logging

_climax_logger = _logging.getLogger("coinmark.climax")


async def scan_climax_reversal(
    *,
    market_scope: MarketScope = "both",
    symbol_limit: int = 200,
    lookback_minutes: int = 60,
    avg_window: int = 30,
    climax_factor: float = 5.0,
    reversal_window_minutes: int = 10,
    sell_cascade_threshold: float = 0.30,
    buy_cascade_threshold: float = 0.70,
    min_cascade_notional: float = 3_000_000.0,
    ob_imbalance_threshold: float = 0.15,
    cooldown_minutes: int = 120,
) -> dict[str, Any]:
    """Detect climax-volume reversals: blow-off top → short, panic bottom → long."""

    now_ms = int(time.time() * 1000)
    start_ms = now_ms - lookback_minutes * 60_000

    all_signals: list[dict[str, Any]] = []

    for market in _markets(market_scope):
        symbols = await _top_symbols(market, symbol_limit)
        if not symbols:
            continue

        # 1m trade buckets + 1m orderbook features
        trade_rows = await query_trade_buckets(
            market=market, symbols=symbols, bucket="1m",
            start_ms=start_ms, end_ms=now_ms,
        )
        ob_rows = await query_orderbook_features(
            market=market, symbols=symbols, bucket="1m",
            start_ms=start_ms, end_ms=now_ms,
        )

        # group by symbol
        trade_by_sym: dict[str, list] = defaultdict(list)
        for r in trade_rows:
            trade_by_sym[str(r.symbol)].append(r)

        ob_by_sym: dict[str, dict[int, Any]] = defaultdict(dict)
        for r in ob_rows:
            ob_by_sym[str(r.symbol)][int(r.bucket_start_ms)] = r

        for sym in symbols:
            candles = sorted(trade_by_sym.get(sym, []), key=lambda r: r.bucket_start_ms)
            if len(candles) < avg_window + 5:
                continue

            ob_map = ob_by_sym.get(sym, {})
            sigs = _detect_climax_for_symbol(
                symbol=sym,
                market=market,
                candles=candles,
                ob_map=ob_map,
                avg_window=avg_window,
                climax_factor=climax_factor,
                reversal_window_minutes=reversal_window_minutes,
                sell_cascade_threshold=sell_cascade_threshold,
                buy_cascade_threshold=buy_cascade_threshold,
                min_cascade_notional=min_cascade_notional,
                ob_imbalance_threshold=ob_imbalance_threshold,
            )
            all_signals.extend(sigs)

    all_signals.sort(key=lambda x: float(x.get("score", 0)), reverse=True)

    inserted = 0
    if all_signals:
        async with write_session() as session:
            inserted = await _sync_climax_events(session, all_signals, cooldown_minutes=cooldown_minutes)

    _climax_logger.info(
        "climax scan done markets=%s candidates=%d inserted=%d",
        market_scope, len(all_signals), inserted,
    )
    return {"candidates": len(all_signals), "insertedEvents": inserted}


def _detect_climax_for_symbol(
    *,
    symbol: str,
    market: str,
    candles: list,
    ob_map: dict[int, Any],
    avg_window: int,
    climax_factor: float,
    reversal_window_minutes: int,
    sell_cascade_threshold: float,
    buy_cascade_threshold: float,
    min_cascade_notional: float,
    ob_imbalance_threshold: float,
) -> list[dict[str, Any]]:
    signals: list[dict[str, Any]] = []
    n = len(candles)
    rev_bars = reversal_window_minutes  # 1m resolution → 1 bar = 1 minute

    for i in range(avg_window, n - rev_bars):
        c = candles[i]
        vol = _to_float(c.quote_notional)
        if vol <= 0:
            continue

        # rolling average over prior avg_window bars
        avg_vol = sum(_to_float(candles[j].quote_notional) for j in range(i - avg_window, i)) / avg_window
        if avg_vol <= 0:
            continue

        vol_ratio = vol / avg_vol
        if vol_ratio < climax_factor:
            continue

        # determine climax direction
        close_p = _to_float(c.close_price)
        open_p = _to_float(c.open_price)
        buy_n = _to_float(c.taker_buy_notional)
        sell_n = _to_float(c.taker_sell_notional)
        total = buy_n + sell_n
        climax_buy_ratio = buy_n / total if total > 0 else 0.5

        bullish_climax = close_p > open_p and climax_buy_ratio > 0.55
        bearish_climax = close_p < open_p and climax_buy_ratio < 0.45

        if not bullish_climax and not bearish_climax:
            continue

        # scan reversal window
        window = candles[i + 1: i + 1 + rev_bars]
        if not window:
            continue

        cascade_found = False
        worst_buy_ratio = 1.0
        worst_sell_notional = 0.0

        ob_imbalance_vals: list[float] = []

        for wc in window:
            wb = _to_float(wc.taker_buy_notional)
            ws = _to_float(wc.taker_sell_notional)
            wt = wb + ws
            w_buy_ratio = wb / wt if wt > 0 else 0.5

            if bullish_climax:
                if w_buy_ratio < sell_cascade_threshold and ws >= min_cascade_notional:
                    cascade_found = True
                if w_buy_ratio < worst_buy_ratio:
                    worst_buy_ratio = w_buy_ratio
                    worst_sell_notional = ws
            elif bearish_climax:
                if w_buy_ratio > buy_cascade_threshold and wb >= min_cascade_notional:
                    cascade_found = True
                if w_buy_ratio > worst_buy_ratio:
                    worst_buy_ratio = w_buy_ratio

            # orderbook imbalance
            ob = ob_map.get(int(wc.bucket_start_ms))
            if ob and ob.sample_count > 0:
                imb = ob.depth_imbalance_l5_sum / ob.sample_count
                ob_imbalance_vals.append(imb)

        if not cascade_found:
            continue

        # orderbook confirmation
        avg_imb = sum(ob_imbalance_vals) / len(ob_imbalance_vals) if ob_imbalance_vals else 0.0

        ob_confirmed = False
        if bullish_climax and avg_imb < -ob_imbalance_threshold:
            ob_confirmed = True
        elif bearish_climax and avg_imb > ob_imbalance_threshold:
            ob_confirmed = True

        if not ob_confirmed:
            continue

        # --- all 3 conditions met → score ---
        vol_score = 40.0 * _clamp(vol_ratio / 10.0, 0.0, 1.0)

        if bullish_climax:
            cascade_score = 30.0 * _clamp((0.50 - worst_buy_ratio) / 0.30, 0.0, 1.0)
        else:
            cascade_score = 30.0 * _clamp((worst_buy_ratio - 0.50) / 0.30, 0.0, 1.0)

        ob_score = 30.0 * _clamp(abs(avg_imb) / 0.40, 0.0, 1.0)

        score = round(vol_score + cascade_score + ob_score, 2)
        state = _signal_state(score)
        if state == "NONE":
            continue

        direction = "short" if bullish_climax else "long"
        event_type = _SIGNAL_EVENT_TYPE_CLIMAX_SHORT if bullish_climax else _SIGNAL_EVENT_TYPE_CLIMAX_LONG

        signals.append({
            "market": market,
            "symbol": symbol,
            "ts": int(c.bucket_start_ms),
            "close": close_p,
            "direction": direction,
            "climaxVolume": round(vol, 2),
            "avgVolume": round(avg_vol, 2),
            "volumeRatio": round(vol_ratio, 2),
            "climaxBuyRatio": round(climax_buy_ratio, 4),
            "cascadeBuyRatio": round(worst_buy_ratio, 4),
            "cascadeSellNotional": round(worst_sell_notional, 2),
            "obImbalance": round(avg_imb, 4),
            "score": score,
            "signalState": state,
            "eventType": event_type,
        })

    # keep only the latest signal per symbol (most recent climax)
    if signals:
        signals.sort(key=lambda x: int(x.get("ts", 0)), reverse=True)
        return [signals[0]]
    return []


async def _sync_climax_events(session, signals: list[dict[str, Any]], cooldown_minutes: int) -> int:
    if not signals:
        return 0

    cutoff_ms = int(time.time() * 1000) - max(1, cooldown_minutes) * 60_000
    inserted = 0

    by_market: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for s in signals:
        by_market[str(s.get("market", ""))].append(s)

    for market, rows in by_market.items():
        symbols = sorted({str(x.get("symbol", "")) for x in rows if x.get("symbol")})
        if not symbols:
            continue

        event_types = sorted({str(x.get("eventType", "")) for x in rows})
        latest_map: dict[tuple[str, str], int] = {}
        for et in event_types:
            stmt = (
                select(AnomalyEvent.symbol, func.max(AnomalyEvent.event_time_ms).label("latest"))
                .where(
                    and_(
                        AnomalyEvent.market == market,
                        AnomalyEvent.event_type == et,
                        AnomalyEvent.symbol.in_(symbols),
                        AnomalyEvent.event_time_ms >= cutoff_ms,
                    )
                )
                .group_by(AnomalyEvent.symbol)
            )
            for sym, ts in (await session.execute(stmt)).all():
                latest_map[(str(sym), et)] = int(ts or 0)

        values: list[dict[str, Any]] = []
        for item in rows:
            state = str(item.get("signalState", "")).upper()
            if state not in {"CONFIRM", "STRONG", "HIGH"}:
                continue
            sym = str(item.get("symbol", ""))
            ts = int(item.get("ts", 0))
            et = str(item.get("eventType", ""))
            if not sym or ts <= 0:
                continue
            prev = latest_map.get((sym, et), 0)
            if prev > 0 and ts - prev < cooldown_minutes * 60_000:
                continue

            direction = str(item.get("direction", ""))
            dir_cn = "看空" if direction == "short" else "看多"
            score = float(item.get("score", 0))
            vol_ratio = float(item.get("volumeRatio", 0))

            title = f"{sym} 天量反转{dir_cn}信号 ({vol_ratio:.1f}x)"

            details = {
                "signalState": item.get("signalState"),
                "score": score,
                "strengthScore": score,
                "direction": direction,
                "climaxVolume": item.get("climaxVolume"),
                "avgVolume": item.get("avgVolume"),
                "volumeRatio": item.get("volumeRatio"),
                "climaxBuyRatio": item.get("climaxBuyRatio"),
                "cascadeBuyRatio": item.get("cascadeBuyRatio"),
                "cascadeSellNotional": item.get("cascadeSellNotional"),
                "obImbalance": item.get("obImbalance"),
            }

            values.append({
                "market": market,
                "symbol": sym,
                "event_type": et,
                "tf_signal": "1m",
                "tf_level": None,
                "event_time_ms": ts,
                "title": title,
                "details": details,
            })
            latest_map[(sym, et)] = ts

        if values:
            await session.execute(insert(AnomalyEvent).values(values))
            inserted += len(values)

    if inserted > 0:
        await session.commit()
    return inserted


def _evaluate_signal_return(rows: list[BucketPoint], signal_idx: int, horizon_min: int) -> tuple[float | None, float | None]:
    horizon = max(1, int(horizon_min))
    end_idx = signal_idx + horizon
    if signal_idx < 0 or signal_idx >= len(rows) or end_idx >= len(rows):
        return None, None

    entry = rows[signal_idx].close
    future = rows[end_idx].close
    if entry <= 0 or future <= 0:
        return None, None

    ret = (future - entry) / entry
    min_close = min((r.close for r in rows[signal_idx + 1 : end_idx + 1] if r.close > 0), default=entry)
    drawdown = (min_close - entry) / entry if entry > 0 else 0.0
    return float(ret), float(drawdown)


async def run_backtest(
    *,
    market_scope: MarketScope,
    days: int,
    params: SignalLabParams,
) -> dict[str, Any]:
    now_ms = int(time.time() * 1000)
    days = max(1, min(30, int(days)))
    start_ms = now_ms - days * 24 * 60 * 60 * 1000

    market_results: dict[str, Any] = {}
    all_samples: list[dict[str, Any]] = []

    for market in _markets(market_scope):
        symbols = await _top_symbols(market, params.symbol_limit, bucket=params.bucket)
        grouped = await _load_rows(market=market, symbols=symbols, start_ms=start_ms - params.lookback_minutes * 60 * 1000, end_ms=now_ms, bucket=params.bucket)
        mcap_map = await _load_market_caps(symbols)

        bkt_min = max(1, _bucket_minutes(params.bucket))
        signal_count = 0
        win_count = 0
        returns: list[float] = []
        drawdowns: list[float] = []
        sample_events: list[dict[str, Any]] = []

        for symbol, rows in grouped.items():
            if len(rows) < max(30, params.lookback_minutes // bkt_min // 3):
                continue
            signals = _scan_symbol_signals(symbol, market, rows, params, mcap_usd=mcap_map.get(symbol, 0.0))
            if not signals:
                continue

            idx_map = {int(r.ts): i for i, r in enumerate(rows)}
            for sig in signals:
                ts = int(sig["ts"])
                if ts < start_ms:
                    continue
                idx = idx_map.get(ts)
                if idx is None:
                    continue

                ret, dd = _evaluate_signal_return(rows, idx, params.forecast_horizon_minutes)
                if ret is None:
                    continue
                signal_count += 1
                returns.append(ret)
                if dd is not None:
                    drawdowns.append(dd)
                if ret > 0:
                    win_count += 1

                if len(sample_events) < 200:
                    sample_events.append(
                        {
                            "market": market,
                            "symbol": symbol,
                            "ts": ts,
                            "eventType": sig.get("eventType"),
                            "direction": sig.get("direction"),
                            "score": sig.get("score"),
                            "signalState": sig.get("signalState"),
                            "retH": round(ret, 6),
                            "maxDrawdown": round(dd or 0.0, 6),
                            "zScore": sig.get("zScore"),
                            "buyRatio": sig.get("buyRatio"),
                            "largeBuyCount": sig.get("largeBuyCount"),
                            "slopeK": sig.get("slopeK"),
                            "slopeR2": sig.get("slopeR2"),
                            "slopeConfirms": sig.get("slopeConfirms"),
                        }
                    )

        avg_return = (sum(returns) / len(returns)) if returns else 0.0
        avg_dd = (sum(drawdowns) / len(drawdowns)) if drawdowns else 0.0
        win_rate = (win_count / signal_count) if signal_count > 0 else 0.0

        market_results[market] = {
            "signals": signal_count,
            "wins": win_count,
            "winRate": round(win_rate, 4),
            "avgReturn": round(avg_return, 6),
            "avgDrawdown": round(avg_dd, 6),
            "symbols": len(symbols),
        }
        all_samples.extend(sample_events)

    total_signals = sum(int(v["signals"]) for v in market_results.values())
    total_wins = sum(int(v["wins"]) for v in market_results.values())
    total_win_rate = (total_wins / total_signals) if total_signals > 0 else 0.0

    return {
        "market": market_scope,
        "days": days,
        "params": {
            "zThreshold": params.z_threshold,
            "lookbackMinutes": params.lookback_minutes,
            "detectionWindowMinutes": params.detection_window_minutes,
            "minLargeCount": params.min_large_count,
            "buyRatioThreshold": params.buy_ratio_threshold,
            "minPersistentSpanMinutes": params.min_persistent_span_minutes,
            "minAvgIntervalMinutes": params.min_avg_interval_minutes,
            "minDistinctTimeBuckets": params.min_distinct_time_buckets,
            "forecastHorizonMinutes": params.forecast_horizon_minutes,
            "cooldownMinutes": params.cooldown_minutes,
            "symbolLimit": params.symbol_limit,
        },
        "summary": {
            "signals": total_signals,
            "wins": total_wins,
            "winRate": round(total_win_rate, 4),
        },
        "markets": market_results,
        "samples": sorted(all_samples, key=lambda x: (float(x.get("score") or 0.0), int(x.get("ts") or 0)), reverse=True)[:200],
        "eventType": _SIGNAL_EVENT_TYPE,
        "ts": int(time.time() * 1000),
    }


_RUNS: dict[str, dict[str, Any]] = {}
_RUNS_LOCK = asyncio.Lock()


async def start_backtest_run(*, market_scope: MarketScope, days: int, params: SignalLabParams) -> str:
    run_id = uuid.uuid4().hex
    async with _RUNS_LOCK:
        _RUNS[run_id] = {
            "runId": run_id,
            "status": "running",
            "createdAt": int(time.time() * 1000),
            "updatedAt": int(time.time() * 1000),
            "result": None,
            "error": None,
        }

    async def _runner() -> None:
        try:
            result = await run_backtest(market_scope=market_scope, days=days, params=params)
            async with _RUNS_LOCK:
                _RUNS[run_id]["status"] = "done"
                _RUNS[run_id]["result"] = result
                _RUNS[run_id]["updatedAt"] = int(time.time() * 1000)
        except Exception as e:  # noqa: BLE001
            async with _RUNS_LOCK:
                _RUNS[run_id]["status"] = "failed"
                _RUNS[run_id]["error"] = str(e)
                _RUNS[run_id]["updatedAt"] = int(time.time() * 1000)

    asyncio.create_task(_runner())
    return run_id


async def get_backtest_run(run_id: str) -> dict[str, Any] | None:
    async with _RUNS_LOCK:
        item = _RUNS.get(run_id)
        if item is None:
            return None
        return dict(item)


# 避免循环导入，放在文件尾
from coinmark_api.db_upsert import insert  # noqa: E402
