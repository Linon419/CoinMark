from __future__ import annotations

import asyncio
import logging
import random
import time
from typing import Any

from coinmark_api.config import settings
from coinmark_api.services.binance.rest import get_orderbook_depth
from coinmark_api.services.symbol_filter import filter_excluded_symbols

logger = logging.getLogger("coinmark.hub")


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
