from __future__ import annotations

import asyncio
import logging
import random
import time
import math
from typing import Any

from coinmark_api.config import settings
from coinmark_api.db import write_session
from coinmark_api.db_upsert import insert
from coinmark_api.models import OrderbookHeatmapSnapshot
from coinmark_api.services.binance.rest import get_orderbook_depth
from coinmark_api.services.symbol_filter import filter_excluded_symbols

logger = logging.getLogger("coinmark.hub")


def _to_float(v: Any) -> float | None:
    try:
        value = float(v)
        if math.isfinite(value):
            return value
        return None
    except Exception:
        return None


def _bucket_start_1m(ts_ms: int) -> int:
    return (int(ts_ms) // 60_000) * 60_000


def _parse_step_overrides(raw: str) -> dict[str, float]:
    out: dict[str, float] = {}
    for part in (raw or "").split(","):
        item = part.strip()
        if not item:
            continue
        if ":" not in item:
            continue
        symbol, step = item.split(":", 1)
        symbol_key = symbol.strip().upper()
        step_value = _to_float(step)
        if not symbol_key or step_value is None or step_value <= 0:
            continue
        if not symbol_key.endswith("USDT"):
            symbol_key = f"{symbol_key}USDT"
        out[symbol_key] = step_value
    return out


def _calc_price_step(symbol: str, mid_price: float) -> float:
    overrides = _parse_step_overrides(settings.depth_heatmap_step_overrides)
    forced = overrides.get(symbol.upper())
    if forced and forced > 0:
        return forced

    bps = max(1.0, float(settings.depth_heatmap_step_bps or 8.0))
    raw_step = mid_price * (bps / 10_000.0)
    if raw_step <= 0:
        raw_step = mid_price * 0.0008

    abs_mid = abs(mid_price)
    if abs_mid >= 10000:
        tick = 10.0
    elif abs_mid >= 1000:
        tick = 1.0
    elif abs_mid >= 100:
        tick = 0.1
    elif abs_mid >= 10:
        tick = 0.01
    elif abs_mid >= 1:
        tick = 0.001
    else:
        tick = 0.0001

    snapped = max(tick, round(raw_step / tick) * tick)
    return snapped


def _build_heatmap_rows(
    *,
    market: str,
    symbol: str,
    depth: dict[str, Any],
    ts_ms: int,
) -> tuple[list[dict[str, Any]], float | None, float | None]:
    bids = depth.get("bids") if isinstance(depth, dict) else None
    asks = depth.get("asks") if isinstance(depth, dict) else None
    if not isinstance(bids, list) or not isinstance(asks, list) or not bids or not asks:
        return [], None, None

    best_bid = _to_float(bids[0][0]) if isinstance(bids[0], (list, tuple)) and len(bids[0]) >= 1 else None
    best_ask = _to_float(asks[0][0]) if isinstance(asks[0], (list, tuple)) and len(asks[0]) >= 1 else None
    if best_bid is None or best_ask is None or best_bid <= 0 or best_ask <= 0:
        return [], None, None

    mid = (best_bid + best_ask) / 2.0
    if mid <= 0:
        return [], None, None

    step = _calc_price_step(symbol, mid)
    if step <= 0:
        return [], None, None

    bins: dict[tuple[float, str], dict[str, float]] = {}

    def _append_levels(levels: list[Any], side: str) -> None:
        for lv in levels:
            if not isinstance(lv, (list, tuple)) or len(lv) < 2:
                continue
            price = _to_float(lv[0])
            qty = _to_float(lv[1])
            if price is None or qty is None or price <= 0 or qty <= 0:
                continue
            bin_idx = math.floor(price / step)
            price_bin = round(bin_idx * step, 10)
            notional = price * qty
            key = (price_bin, side)
            item = bins.get(key)
            if item is None:
                bins[key] = {"intensity": notional, "count": 1.0}
            else:
                item["intensity"] += notional
                item["count"] += 1.0

    _append_levels(bids, "bid")
    _append_levels(asks, "ask")

    min_intensity = max(0.0, float(settings.depth_heatmap_min_intensity_usd or 10000.0))
    bucket_start_ms = _bucket_start_1m(ts_ms)

    rows: list[dict[str, Any]] = []
    for key, agg in bins.items():
        price_bin, side = key
        intensity = float(agg.get("intensity") or 0.0)
        if intensity < min_intensity:
            continue
        rows.append(
            {
                "market": market,
                "symbol": symbol,
                "bucket_start_ms": bucket_start_ms,
                "side": side,
                "price_bin": price_bin,
                "price_step": step,
                "intensity": intensity,
                "level_count": int(agg.get("count") or 0),
            }
        )

    return rows, mid, step


async def _write_heatmap_rows(rows: list[dict[str, Any]]) -> int:
    if not rows:
        return 0
    stmt = insert(OrderbookHeatmapSnapshot).values(rows)
    stmt = stmt.on_conflict_do_update(
        index_elements=["market", "symbol", "bucket_start_ms", "side", "price_bin"],
        set_={
            "price_step": stmt.excluded.price_step,
            "intensity": stmt.excluded.intensity,
            "level_count": stmt.excluded.level_count,
        },
    )
    async with write_session() as session:
        await session.execute(stmt)
        await session.commit()
    return len(rows)


def _parse_symbols(raw: str) -> list[str]:
    out: list[str] = []
    seen: set[str] = set()
    for item in (raw or "").split(","):
        symbol = item.strip().upper()
        if not symbol:
            continue
        if symbol.endswith("USDT"):
            normalized = symbol
        else:
            normalized = f"{symbol}USDT"
        if normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)
    return filter_excluded_symbols(out)


def _split_tiers(all_symbols: list[str], fast_raw: str) -> tuple[list[str], list[str]]:
    fast_set = set(_parse_symbols(fast_raw))
    fast = [s for s in all_symbols if s in fast_set]
    slow = [s for s in all_symbols if s not in fast_set]
    return fast, slow


async def _fetch_one(market: str, symbol: str, limit: int, sem: asyncio.Semaphore) -> tuple[str, bool, int, int, float]:
    started = time.perf_counter()
    async with sem:
        try:
            depth = await get_orderbook_depth(market=market, symbol=symbol, limit=limit)
            bids = depth.get("bids") if isinstance(depth, dict) else None
            asks = depth.get("asks") if isinstance(depth, dict) else None
            bid_count = len(bids) if isinstance(bids, list) else 0
            ask_count = len(asks) if isinstance(asks, list) else 0

            if settings.depth_heatmap_enabled:
                target_market = "spot" if settings.depth_heatmap_force_spot else market
                heat_depth = depth
                if target_market != market:
                    try:
                        heat_depth = await get_orderbook_depth(market=target_market, symbol=symbol, limit=limit)
                    except Exception:
                        heat_depth = None
                if isinstance(heat_depth, dict):
                    rows, _, _ = _build_heatmap_rows(
                        market=target_market,
                        symbol=symbol,
                        depth=heat_depth,
                        ts_ms=int(time.time() * 1000),
                    )
                    if rows:
                        try:
                            await _write_heatmap_rows(rows)
                        except Exception:
                            logger.exception("depth heatmap write failed market=%s symbol=%s", target_market, symbol)

            cost_ms = (time.perf_counter() - started) * 1000
            return symbol, True, bid_count, ask_count, cost_ms
        except Exception:
            cost_ms = (time.perf_counter() - started) * 1000
            return symbol, False, 0, 0, cost_ms


async def _run_batch(tag: str, market: str, symbols: list[str], limit: int, concurrency: int) -> None:
    if not symbols:
        return
    sem = asyncio.Semaphore(max(1, int(concurrency)))
    started = time.perf_counter()
    results = await asyncio.gather(*[_fetch_one(market, s, limit, sem) for s in symbols])

    ok = sum(1 for _, succ, _, _, _ in results if succ)
    fail = len(results) - ok
    avg_ms = (sum(ms for _, _, _, _, ms in results) / len(results)) if results else 0.0
    avg_bids = (sum(b for _, succ, b, _, _ in results if succ) / max(1, ok)) if ok else 0.0
    avg_asks = (sum(a for _, succ, _, a, _ in results if succ) / max(1, ok)) if ok else 0.0
    total_ms = (time.perf_counter() - started) * 1000

    logger.info(
        "depth fullscan %s market=%s symbols=%s ok=%s fail=%s limit=%s avg_bids=%.1f avg_asks=%.1f avg_ms=%.1f total_ms=%.1f",
        tag,
        market,
        len(symbols),
        ok,
        fail,
        limit,
        avg_bids,
        avg_asks,
        avg_ms,
        total_ms,
    )


async def _loop(
    stop_event: asyncio.Event,
    *,
    tag: str,
    market: str,
    symbols: list[str],
    interval_sec: int,
    limit: int,
    concurrency: int,
    jitter_sec: int,
) -> None:
    if not symbols:
        logger.info("depth fullscan %s skipped: no symbols", tag)
        return

    while not stop_event.is_set():
        try:
            await _run_batch(tag, market, symbols, limit, concurrency)
        except Exception:
            logger.exception("depth fullscan %s failed", tag)

        timeout = max(30, int(interval_sec))
        if jitter_sec > 0:
            timeout += random.randint(-int(jitter_sec), int(jitter_sec))
            timeout = max(15, timeout)
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=timeout)
            break
        except asyncio.TimeoutError:
            pass


def create_depth_fullscan_tasks(stop_event: asyncio.Event) -> list[asyncio.Task[Any]]:
    if not settings.depth_fullscan_enabled:
        return []

    market = (settings.depth_fullscan_market or "swap").strip().lower()
    if market not in {"swap", "spot"}:
        logger.warning("depth fullscan disabled: unsupported market=%s", market)
        return []

    all_symbols = _parse_symbols(settings.depth_fullscan_symbols)
    fast_symbols, slow_symbols = _split_tiers(all_symbols, settings.depth_fullscan_fast_symbols)
    limit = settings.depth_fullscan_limit_swap if market == "swap" else settings.depth_fullscan_limit_spot
    concurrency = max(1, int(settings.depth_fullscan_concurrency))
    jitter_sec = max(0, int(settings.depth_fullscan_jitter_sec))

    logger.info(
        "depth fullscan enabled market=%s total=%s fast=%s slow=%s fast_interval=%ss slow_interval=%ss limit=%s concurrency=%s jitter=%ss",
        market,
        len(all_symbols),
        len(fast_symbols),
        len(slow_symbols),
        settings.depth_fullscan_fast_interval_sec,
        settings.depth_fullscan_slow_interval_sec,
        limit,
        concurrency,
        jitter_sec,
    )

    tasks: list[asyncio.Task[Any]] = []
    if fast_symbols:
        tasks.append(
            asyncio.create_task(
                _loop(
                    stop_event,
                    tag="fast",
                    market=market,
                    symbols=fast_symbols,
                    interval_sec=settings.depth_fullscan_fast_interval_sec,
                    limit=limit,
                    concurrency=concurrency,
                    jitter_sec=jitter_sec,
                )
            )
        )
    if slow_symbols:
        tasks.append(
            asyncio.create_task(
                _loop(
                    stop_event,
                    tag="slow",
                    market=market,
                    symbols=slow_symbols,
                    interval_sec=settings.depth_fullscan_slow_interval_sec,
                    limit=limit,
                    concurrency=concurrency,
                    jitter_sec=jitter_sec,
                )
            )
        )
    return tasks
