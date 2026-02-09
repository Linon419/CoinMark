from __future__ import annotations

import asyncio
import math
import time
import uuid
from collections import defaultdict, deque
from dataclasses import dataclass
from typing import Any, Literal

from sqlalchemy import and_, desc, func, select

from coinmark_api.db import SessionLocal
from coinmark_api.models import AnomalyEvent, TradeBucket

Market = Literal["spot", "swap"]
MarketScope = Literal["spot", "swap", "both"]

_SIGNAL_EVENT_TYPE = "signal_lab_persistent_buy"


@dataclass(slots=True)
class SignalLabParams:
    z_threshold: float = 2.8
    lookback_minutes: int = 1440
    detection_window_minutes: int = 240
    min_large_count: int = 6
    buy_ratio_threshold: float = 0.8
    min_persistent_span_minutes: int = 90
    min_avg_interval_minutes: int = 8
    min_distinct_time_buckets: int = 4
    forecast_horizon_minutes: int = 60
    cooldown_minutes: int = 180
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


def _clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


def _to_float(v: Any) -> float:
    try:
        return float(v or 0.0)
    except Exception:
        return 0.0


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


def _score_signal(z_score: float, buy_ratio: float, large_buy_count: int) -> float:
    z_part = _clamp((z_score - 2.0) * 15.0, 0.0, 30.0)
    ratio_part = _clamp((buy_ratio - 0.5) * 100.0, 0.0, 30.0)
    count_part = _clamp(float(large_buy_count) * 4.0, 0.0, 20.0)
    base = 35.0
    return round(_clamp(base + z_part + ratio_part + count_part, 0.0, 100.0), 2)


def _scan_symbol_signals(symbol: str, market: Market, rows: list[BucketPoint], params: SignalLabParams) -> list[dict[str, Any]]:
    if not rows:
        return []

    lookback = max(60, int(params.lookback_minutes))
    detect_window_ms = max(15, int(params.detection_window_minutes)) * 60 * 1000
    min_persistent_span_ms = max(1, int(params.min_persistent_span_minutes)) * 60 * 1000
    min_avg_interval_ms = max(1, int(params.min_avg_interval_minutes)) * 60 * 1000
    distinct_bucket_ms = 10 * 60 * 1000
    cooldown_ms = max(1, int(params.cooldown_minutes)) * 60 * 1000

    hist = deque()
    hist_sum = 0.0
    hist_sumsq = 0.0

    large_events = deque()  # (ts, side, abs_net)
    signals: list[dict[str, Any]] = []
    last_signal_ts = 0

    for row in rows:
        x = abs(row.net)

        n = len(hist)
        z_score = 0.0
        if n >= 30:
            mean = hist_sum / n
            var = max(0.0, (hist_sumsq / n) - (mean * mean))
            std = math.sqrt(var)
            if std > 1e-9:
                z_score = (x - mean) / std

        min_ts = int(row.ts) - detect_window_ms
        while large_events and large_events[0][0] < min_ts:
            large_events.popleft()

        if z_score >= float(params.z_threshold):
            side = "buy" if row.net > 0 else "sell"
            large_events.append((int(row.ts), side, x))

        buy_amt = sum(v for _, side, v in large_events if side == "buy")
        sell_amt = sum(v for _, side, v in large_events if side == "sell")
        total_amt = buy_amt + sell_amt
        buy_ratio = buy_amt / total_amt if total_amt > 0 else 0.0
        large_buy_count = sum(1 for _, side, _ in large_events if side == "buy")
        buy_event_ts = [ts for ts, side, _ in large_events if side == "buy"]
        persistent_span_ms = (buy_event_ts[-1] - buy_event_ts[0]) if len(buy_event_ts) >= 2 else 0
        avg_interval_ms = (
            sum((buy_event_ts[i] - buy_event_ts[i - 1]) for i in range(1, len(buy_event_ts))) / (len(buy_event_ts) - 1)
            if len(buy_event_ts) >= 2
            else 0.0
        )
        distinct_time_buckets = len({int(ts // distinct_bucket_ms) for ts in buy_event_ts})

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

        if int(row.ts) - last_signal_ts < cooldown_ms:
            hist.append(x)
            hist_sum += x
            hist_sumsq += x * x
            if len(hist) > lookback:
                old = hist.popleft()
                hist_sum -= old
                hist_sumsq -= old * old
            continue

        score = _score_signal(z_score, buy_ratio, large_buy_count)
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
                "score": score,
                "signalState": _signal_state(score),
                "eventType": _SIGNAL_EVENT_TYPE,
            }
        )
        last_signal_ts = int(row.ts)

        hist.append(x)
        hist_sum += x
        hist_sumsq += x * x
        if len(hist) > lookback:
            old = hist.popleft()
            hist_sum -= old
            hist_sumsq -= old * old

    return signals


async def _top_symbols(session, market: Market, symbol_limit: int) -> list[str]:
    since_ms = int(time.time() * 1000) - 24 * 60 * 60 * 1000
    stmt = (
        select(TradeBucket.symbol, func.sum(TradeBucket.quote_notional).label("qv"))
        .where(
            and_(
                TradeBucket.market == market,
                TradeBucket.bucket == "1m",
                TradeBucket.bucket_start_ms >= since_ms,
            )
        )
        .group_by(TradeBucket.symbol)
        .order_by(desc("qv"))
        .limit(max(20, int(symbol_limit)))
    )
    rows = (await session.execute(stmt)).all()
    return [str(sym) for sym, _ in rows if sym]


async def _load_rows(
    session,
    *,
    market: Market,
    symbols: list[str],
    start_ms: int,
    end_ms: int,
) -> dict[str, list[BucketPoint]]:
    if not symbols:
        return {}

    stmt = (
        select(TradeBucket)
        .where(
            and_(
                TradeBucket.market == market,
                TradeBucket.bucket == "1m",
                TradeBucket.symbol.in_(symbols),
                TradeBucket.bucket_start_ms >= int(start_ms),
                TradeBucket.bucket_start_ms <= int(end_ms),
            )
        )
        .order_by(TradeBucket.symbol.asc(), TradeBucket.bucket_start_ms.asc())
    )
    rows = (await session.execute(stmt)).scalars().all()
    grouped: dict[str, list[BucketPoint]] = defaultdict(list)
    for r in rows:
        grouped[str(r.symbol)].append(
            BucketPoint(
                ts=int(r.bucket_start_ms),
                close=_to_float(r.close_price),
                buy=_to_float(r.taker_buy_notional),
                sell=_to_float(r.taker_sell_notional),
            )
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

    async with SessionLocal() as session:
        for market in _markets(market_scope):
            symbols = await _top_symbols(session, market, params.symbol_limit)
            grouped = await _load_rows(session, market=market, symbols=symbols, start_ms=start_ms, end_ms=now_ms)

            market_signals: list[dict[str, Any]] = []
            for symbol, rows in grouped.items():
                if len(rows) < 90:
                    continue
                items = _scan_symbol_signals(symbol, market, rows, params)
                if items:
                    latest = items[-1]
                    if _signal_state_rank(str(latest.get("signalState") or "")) >= min_state_rank:
                        market_signals.append(latest)

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

        stmt = (
            select(AnomalyEvent.symbol, func.max(AnomalyEvent.event_time_ms).label("latest"))
            .where(
                and_(
                    AnomalyEvent.market == market,
                    AnomalyEvent.event_type == _SIGNAL_EVENT_TYPE,
                    AnomalyEvent.symbol.in_(symbols),
                    AnomalyEvent.event_time_ms >= cutoff_ms,
                )
            )
            .group_by(AnomalyEvent.symbol)
        )
        latest_map = {str(sym): int(ts or 0) for sym, ts in (await session.execute(stmt)).all()}

        values: list[dict[str, Any]] = []
        for item in rows:
            signal_state = str(item.get("signalState") or "").upper()
            if signal_state not in {"CONFIRM", "STRONG", "HIGH"}:
                continue
            sym = str(item.get("symbol") or "")
            ts = int(item.get("ts") or 0)
            if not sym or ts <= 0:
                continue
            prev = latest_map.get(sym, 0)
            if prev > 0 and ts - prev < max(1, int(cooldown_minutes)) * 60 * 1000:
                continue

            score = float(item.get("score") or 0.0)
            values.append(
                {
                    "market": market,
                    "symbol": sym,
                    "event_type": _SIGNAL_EVENT_TYPE,
                    "tf_signal": "1m",
                    "tf_level": "1h",
                    "event_time_ms": ts,
                    "title": f"{sym} 资金持续吸筹信号",
                    "details": {
                        "signalState": item.get("signalState"),
                        "score": score,
                        "strengthScore": score,
                        "zScore": item.get("zScore"),
                        "buyRatio": item.get("buyRatio"),
                        "largeBuyCount": item.get("largeBuyCount"),
                        "persistentSpanMinutes": item.get("persistentSpanMinutes"),
                        "avgIntervalMinutes": item.get("avgIntervalMinutes"),
                        "distinctTimeBuckets": item.get("distinctTimeBuckets"),
                        "netFlow": item.get("netFlow"),
                    },
                }
            )
            latest_map[sym] = ts

        if values:
            await session.execute(insert(AnomalyEvent).values(values))  # type: ignore[name-defined]
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

    async with SessionLocal() as session:
        for market in _markets(market_scope):
            symbols = await _top_symbols(session, market, params.symbol_limit)
            grouped = await _load_rows(session, market=market, symbols=symbols, start_ms=start_ms - params.lookback_minutes * 60 * 1000, end_ms=now_ms)

            signal_count = 0
            win_count = 0
            returns: list[float] = []
            drawdowns: list[float] = []
            sample_events: list[dict[str, Any]] = []

            for symbol, rows in grouped.items():
                if len(rows) < params.lookback_minutes // 3:
                    continue
                signals = _scan_symbol_signals(symbol, market, rows, params)
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
                                "score": sig.get("score"),
                                "signalState": sig.get("signalState"),
                                "retH": round(ret, 6),
                                "maxDrawdown": round(dd or 0.0, 6),
                                "zScore": sig.get("zScore"),
                                "buyRatio": sig.get("buyRatio"),
                                "largeBuyCount": sig.get("largeBuyCount"),
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
