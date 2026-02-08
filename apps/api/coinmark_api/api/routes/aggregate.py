from __future__ import annotations

import time
from decimal import Decimal, InvalidOperation

from fastapi import APIRouter, Query
from sqlalchemy import and_, func, select

from coinmark_api.api.routes.coin import coin_fund_snapshots
from coinmark_api.services.binance.rest import get_klines, get_pairs, get_ticker_24h_all
from coinmark_api.db import SessionLocal
from coinmark_api.models import TradeBucket


router = APIRouter()


def _normalize_legacy_symbol(raw: str) -> str:
    sym = raw.strip().upper()
    if "/" not in sym:
        return sym
    parts = [part for part in sym.split("/") if part]
    if len(parts) >= 2:
        return f"{parts[0]}{parts[1]}"
    return sym.replace("/", "")


@router.get("/aggregate/fundSnapshots")
async def fund_snapshots_legacy(
    symbol: str = Query(..., min_length=3, max_length=64),
    tz_offset_min: int = Query(0, alias="tzOffsetMin"),
    time_mode: str = Query("utc", alias="timeMode", pattern="^(utc|local)$"),
) -> dict:
    normalized_symbol = _normalize_legacy_symbol(symbol)
    payload = await coin_fund_snapshots(
        symbol=normalized_symbol,
        tz_offset_min=tz_offset_min,
        time_mode=time_mode,
    )
    items = payload.get("items") or []

    swap_data = []
    spot_data = []
    for idx, item in enumerate(items, start=1):
        key = int(item.get("key") or idx)
        swap_data.append({"key": key, "value": float(item.get("swapValue") or 0.0)})
        spot_data.append({"key": key, "value": float(item.get("spotValue") or 0.0)})

    return {
        "code": 20000,
        "msg": "",
        "status": 1,
        "data": {"swapData": swap_data, "spotData": spot_data},
    }


@router.get("/aggregate/basicinfo")
async def basicinfo(
    market: str = Query("swap", pattern="^(spot|swap)$"),
    limit: int = Query(50, ge=1, le=500),
) -> dict:
    """
    主页面聚合（MVP 版本）：
    - 24h 涨跌榜（来自 Binance 24hr ticker，严格可追溯）
    """
    rows = await get_ticker_24h_all(market)
    valid = set(await get_pairs(market))
    items = []
    for r in rows:
        sym = r.get("symbol")
        if not sym or not str(sym).endswith("USDT"):
            continue
        if sym not in valid:
            continue
        try:
            pct = float(r.get("priceChangePercent"))
            last = float(r.get("lastPrice"))
            qv = float(r.get("quoteVolume"))
        except (TypeError, ValueError):
            continue
        items.append(
            {
                "symbol": str(sym),
                "lastPrice": last,
                "priceChangePercent": pct,
                "quoteVolume": qv,
            }
        )

    gainers = sorted(items, key=lambda x: x["priceChangePercent"], reverse=True)[:limit]
    losers = sorted(items, key=lambda x: x["priceChangePercent"])[:limit]
    return {"market": market, "gainers": gainers, "losers": losers}


@router.get("/kline/GetKlines")
async def klines(
    market: str = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    interval: str = Query("1h", min_length=1, max_length=10),
    limit: int = Query(200, ge=1, le=1500),
) -> dict:
    data = await get_klines(market=market, symbol=symbol, interval=interval, limit=limit)
    return {"market": market, "symbol": symbol, "interval": interval, "klines": data}


def _bucket_ms(bucket: str) -> int:
    if bucket == "15m":
        return 15 * 60 * 1000
    if bucket == "1h":
        return 60 * 60 * 1000
    if bucket == "4h":
        return 4 * 60 * 60 * 1000
    if bucket == "1d":
        return 24 * 60 * 60 * 1000
    raise ValueError("unsupported bucket")


@router.get("/aggregate/returns")
async def returns_rank(
    market: str = Query("swap", pattern="^(spot|swap)$"),
    bucket: str = Query("15m", pattern="^(15m|1h|4h|1d)$"),
    limit: int = Query(50, ge=1, le=500),
) -> dict:
    """
    近一根已收盘 K 线的涨跌幅榜（基于 trade_buckets 的 OHLC，完全可复算）。
    """
    now_ms = int(time.time() * 1000)
    bms = _bucket_ms(bucket)
    last_closed_start = (now_ms // bms) * bms - bms

    async with SessionLocal() as session:
        cnt = (
            await session.execute(
                select(func.count()).where(
                    and_(
                        TradeBucket.market == market,
                        TradeBucket.bucket == bucket,
                        TradeBucket.bucket_start_ms == last_closed_start,
                    )
                )
            )
        ).scalar_one()

        target_start = last_closed_start
        if cnt == 0:
            target_start = (
                await session.execute(
                    select(func.max(TradeBucket.bucket_start_ms)).where(
                        and_(TradeBucket.market == market, TradeBucket.bucket == bucket)
                    )
                )
            ).scalar_one_or_none()

        if target_start is None:
            return {
                "market": market,
                "bucket": bucket,
                "bucketStartMs": None,
                "bucketEndMs": None,
                "gainers": [],
                "losers": [],
            }

        rows = (
            (
                await session.execute(
                    select(TradeBucket).where(
                        and_(
                            TradeBucket.market == market,
                            TradeBucket.bucket == bucket,
                            TradeBucket.bucket_start_ms == target_start,
                            TradeBucket.open_price.is_not(None),
                            TradeBucket.close_price.is_not(None),
                        )
                    )
                )
            )
            .scalars()
            .all()
        )

    items = []
    for r in rows:
        try:
            o = Decimal(str(r.open_price))
            c = Decimal(str(r.close_price))
            h = Decimal(str(r.high_price)) if r.high_price is not None else None
            l = Decimal(str(r.low_price)) if r.low_price is not None else None
        except (InvalidOperation, TypeError):
            continue
        if o <= 0:
            continue
        ret = (c - o) / o
        amp = None
        if h is not None and l is not None and o > 0:
            amp = (h - l) / o

        items.append(
            {
                "symbol": r.symbol,
                "open": float(o),
                "close": float(c),
                "high": float(h) if h is not None else None,
                "low": float(l) if l is not None else None,
                "returnPct": float(ret),
                "amplitudePct": float(amp) if amp is not None else None,
                "quoteNotional": float(r.quote_notional),
                "tradeCount": int(r.trade_count),
            }
        )

    gainers = sorted(items, key=lambda x: x["returnPct"], reverse=True)[:limit]
    losers = sorted(items, key=lambda x: x["returnPct"])[:limit]
    return {
        "market": market,
        "bucket": bucket,
        "bucketStartMs": int(target_start),
        "bucketEndMs": int(target_start + bms),
        "gainers": gainers,
        "losers": losers,
    }
