from __future__ import annotations

import asyncio
import time
from decimal import Decimal, InvalidOperation
from typing import Any

from coinmark_api.db import SessionLocal
from coinmark_api.db_upsert import insert
from coinmark_api.models import TradeBucket
from coinmark_api.services.binance.rest import get_klines


def _to_decimal(v: Any) -> Decimal:
    if isinstance(v, Decimal):
        return v
    try:
        return Decimal(str(v))
    except (InvalidOperation, TypeError):
        return Decimal("0")


def _interval_ms(interval: str) -> int:
    if interval == "1m":
        return 60 * 1000
    if interval == "15m":
        return 15 * 60 * 1000
    if interval == "1h":
        return 60 * 60 * 1000
    if interval == "4h":
        return 4 * 60 * 60 * 1000
    if interval == "1d":
        return 24 * 60 * 60 * 1000
    raise ValueError(f"不支持的 interval: {interval}")


def _chunk(values: list[dict], size: int) -> list[list[dict]]:
    if size <= 0:
        return [values]
    return [values[i : i + size] for i in range(0, len(values), size)]


async def backfill_trade_buckets_from_klines(
    *,
    market: str,
    symbols: list[str],
    interval: str,
    limit: int,
    concurrency: int = 8,
    db_batch_size: int = 2000,
) -> int:
    """
    用 Binance K 线回填 trade_buckets（只回填已收盘 candle）。
    口径（来自 kline 数组字段）：
    - quote_notional = quote asset volume（索引 7）
    - taker_buy_notional = taker buy quote asset volume（索引 10）
    - taker_sell_notional = quote_notional - taker_buy_notional

    返回：写入（upsert）行数（最佳努力）。
    """
    if not symbols or limit <= 0:
        return 0

    now_ms = int(time.time() * 1000)
    ms = _interval_ms(interval)
    cur_start = (now_ms // ms) * ms
    last_closed_start = cur_start - ms

    sem = asyncio.Semaphore(max(1, int(concurrency)))
    out: list[dict] = []

    async def _one(sym: str) -> None:
        async with sem:
            data = await get_klines(market=market, symbol=sym, interval=interval, limit=limit)
        for row in data:
            if not isinstance(row, list) or len(row) < 11:
                continue
            try:
                open_time = int(row[0])
                close_time = int(row[6])
            except Exception:
                continue
            if open_time > last_closed_start:
                continue

            o = _to_decimal(row[1])
            h = _to_decimal(row[2])
            l = _to_decimal(row[3])
            c = _to_decimal(row[4])
            quote_notional = _to_decimal(row[7])
            trades = int(row[8]) if row[8] is not None else 0
            taker_buy_quote = _to_decimal(row[10])
            taker_sell_quote = quote_notional - taker_buy_quote
            if taker_sell_quote < 0:
                taker_sell_quote = Decimal("0")

            out.append(
                {
                    "market": market,
                    "symbol": sym,
                    "bucket": interval,
                    "bucket_start_ms": open_time,
                    "taker_buy_notional": taker_buy_quote,
                    "taker_sell_notional": taker_sell_quote,
                    "quote_notional": quote_notional,
                    "trade_count": trades,
                    "first_trade_ms": open_time,
                    "last_trade_ms": close_time,
                    "open_price": o,
                    "close_price": c,
                    "high_price": h,
                    "low_price": l,
                }
            )

    await asyncio.gather(*[_one(s) for s in symbols])
    if not out:
        return 0

    # 去重（同一 (market,symbol,bucket,bucket_start_ms) 可能从多次请求中出现）
    dedup: dict[tuple[str, str, str, int], dict] = {}
    for v in out:
        k = (v["market"], v["symbol"], v["bucket"], int(v["bucket_start_ms"]))
        dedup[k] = v
    values = list(dedup.values())

    async with SessionLocal() as session:
        written = 0
        for chunk in _chunk(values, db_batch_size):
            stmt = insert(TradeBucket).values(chunk)
            stmt = stmt.on_conflict_do_update(
                index_elements=["market", "symbol", "bucket", "bucket_start_ms"],
                set_={
                    "taker_buy_notional": stmt.excluded.taker_buy_notional,
                    "taker_sell_notional": stmt.excluded.taker_sell_notional,
                    "quote_notional": stmt.excluded.quote_notional,
                    "trade_count": stmt.excluded.trade_count,
                    "first_trade_ms": stmt.excluded.first_trade_ms,
                    "last_trade_ms": stmt.excluded.last_trade_ms,
                    "open_price": stmt.excluded.open_price,
                    "close_price": stmt.excluded.close_price,
                    "high_price": stmt.excluded.high_price,
                    "low_price": stmt.excluded.low_price,
                },
            )
            res = await session.execute(stmt)
            try:
                written += int(res.rowcount or 0)
            except Exception:
                pass
        await session.commit()

    return written
