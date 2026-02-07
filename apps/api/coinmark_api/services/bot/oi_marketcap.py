from __future__ import annotations

import asyncio
import time
from typing import Any
from decimal import Decimal, InvalidOperation

from sqlalchemy import select
from coinmark_api.db_upsert import insert

from coinmark_api.db import SessionLocal
from coinmark_api.models import AssetMarketCap, OpenInterestSnapshot
from coinmark_api.services.binance.rest import get_binance_bapi_products, get_futures_open_interest, get_futures_premium_index_all, get_pairs


def _base_from_symbol(symbol: str) -> str:
    if symbol.endswith("USDT"):
        return symbol[: -len("USDT")]
    return symbol


async def refresh_market_caps_from_binance_bapi() -> None:
    """
    使用 Binance 网页端未文档化 bapi 端点获取 circulating supply 与价格，计算市值。
    注意：该来源不保证稳定，系统会标注 source。
    """
    items = await get_binance_bapi_products(include_etf=True)
    now_ms = int(time.time() * 1000)

    # bapi 会返回同一资产在不同计价货币下的多行（USDT/USDC/法币/其他稳定币等）。
    # 为了“可复算 + 去重 + 稳定”，这里只取 USD 口径的报价（优先 USDT，其次 USDC）。
    # 这样才能保证写库时不会出现同一 asset 多行导致 ON CONFLICT 报错。
    best_by_asset: dict[str, dict[str, Any]] = {}
    for it in items:
        base = it.get("b")
        price = it.get("c")
        cs = it.get("cs")
        pm = it.get("pm")  # 计价货币（USDT/USDC/FIAT/...）
        qv = it.get("qv")  # quote volume（用于同一 pm 内部择优）
        if not base or price is None or cs is None:
            continue
        if pm not in ("USDT", "USDC"):
            continue
        try:
            p = Decimal(str(price))
            supply = Decimal(str(cs))
            quote_volume = Decimal(str(qv)) if qv is not None else Decimal("0")
        except (InvalidOperation, TypeError):
            continue
        if p <= 0 or supply <= 0:
            continue

        asset = str(base)
        candidate = {
            "asset": asset,
            "price_usd": p,
            "circulating_supply": supply,
            "market_cap_usd": p * supply,
            "source": "binance_bapi_get_products",
            "event_time_ms": now_ms,
            "_pm": str(pm),
            "_qv": quote_volume,
        }

        prev = best_by_asset.get(asset)
        if prev is None:
            best_by_asset[asset] = candidate
            continue

        # 优先级：USDT > USDC；同优先级取更大 qv
        def _rank(row: dict[str, Any]) -> tuple[int, Decimal]:
            pm_ = row.get("_pm")
            primary = 0 if pm_ == "USDT" else 1
            return (primary, Decimal(str(row.get("_qv") or "0")))

        if _rank(candidate) < _rank(prev):
            best_by_asset[asset] = candidate

    values = []
    for v in best_by_asset.values():
        v.pop("_pm", None)
        v.pop("_qv", None)
        values.append(v)

    if not values:
        return

    stmt = insert(AssetMarketCap).values(values)
    stmt = stmt.on_conflict_do_update(
        index_elements=["asset"],
        set_={
            "price_usd": stmt.excluded.price_usd,
            "circulating_supply": stmt.excluded.circulating_supply,
            "market_cap_usd": stmt.excluded.market_cap_usd,
            "source": stmt.excluded.source,
            "event_time_ms": stmt.excluded.event_time_ms,
        },
    )

    async with SessionLocal() as session:
        await session.execute(stmt)
        await session.commit()


async def refresh_open_interest_snapshots(symbols: list[str]) -> None:
    premium = await get_futures_premium_index_all()
    mark_map: dict[str, Decimal] = {}
    for r in premium:
        try:
            sym = str(r.get("symbol"))
            mark_map[sym] = Decimal(str(r.get("markPrice")))
        except (TypeError, InvalidOperation):
            continue

    sem = asyncio.Semaphore(25)
    now_ms = int(time.time() * 1000)
    values: list[dict[str, Any]] = []

    async def _one(sym: str) -> None:
        async with sem:
            try:
                oi = await get_futures_open_interest(sym)
            except Exception:
                return
        try:
            open_interest = Decimal(str(oi.get("openInterest")))
        except (TypeError, InvalidOperation):
            return
        mp = mark_map.get(sym)
        if mp is None or mp <= 0:
            return
        values.append(
            {
                "symbol": sym,
                "open_interest": open_interest,
                "mark_price": mp,
                "oi_notional_usd": open_interest * mp,
                "event_time_ms": now_ms,
            }
        )

    await asyncio.gather(*[_one(s) for s in symbols])
    if not values:
        return

    stmt = insert(OpenInterestSnapshot).values(values)
    stmt = stmt.on_conflict_do_update(
        index_elements=["symbol"],
        set_={
            "open_interest": stmt.excluded.open_interest,
            "mark_price": stmt.excluded.mark_price,
            "oi_notional_usd": stmt.excluded.oi_notional_usd,
            "event_time_ms": stmt.excluded.event_time_ms,
        },
    )

    async with SessionLocal() as session:
        await session.execute(stmt)
        await session.commit()


async def get_oi_marketcap_rank(limit: int) -> list[dict]:
    async with SessionLocal() as session:
        oi_rows = (await session.execute(select(OpenInterestSnapshot))).scalars().all()
        cap_rows = (await session.execute(select(AssetMarketCap))).scalars().all()

    if not oi_rows or not cap_rows:
        # 兜底：若 ingest 尚未跑起来，尝试刷新一次
        await refresh_market_caps_from_binance_bapi()
        swap_pairs = await get_pairs("swap")
        await refresh_open_interest_snapshots(swap_pairs)
        async with SessionLocal() as session:
            oi_rows = (await session.execute(select(OpenInterestSnapshot))).scalars().all()
            cap_rows = (await session.execute(select(AssetMarketCap))).scalars().all()

    cap_map = {r.asset: r for r in cap_rows}

    items = []
    for oi in oi_rows:
        asset = _base_from_symbol(oi.symbol)
        cap = cap_map.get(asset)
        if not cap:
            continue
        if cap.market_cap_usd <= 0:
            continue
        ratio = oi.oi_notional_usd / cap.market_cap_usd
        items.append(
            {
                "symbol": oi.symbol,
                "asset": asset,
                "oiNotionalUsd": float(oi.oi_notional_usd),
                "marketCapUsd": float(cap.market_cap_usd),
                "ratio": float(ratio),
                "marketCapSource": cap.source,
                "updatedAtMs": min(int(oi.event_time_ms), int(cap.event_time_ms)),
            }
        )

    items.sort(key=lambda x: x["ratio"], reverse=True)
    return items[:limit]
