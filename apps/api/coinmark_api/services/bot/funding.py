from __future__ import annotations

import time
from decimal import Decimal, InvalidOperation

from sqlalchemy import select
from coinmark_api.db_upsert import insert

from coinmark_api.db import SessionLocal
from coinmark_api.models import FundingRateSnapshot
from coinmark_api.services.binance.rest import get_futures_premium_index_all


async def refresh_funding_rate_snapshots() -> None:
    rows = await get_futures_premium_index_all()
    now_ms = int(time.time() * 1000)

    values = []
    for r in rows:
        symbol = r.get("symbol")
        lfr = r.get("lastFundingRate")
        mp = r.get("markPrice")
        if not symbol or lfr is None or mp is None:
            continue
        try:
            values.append(
                {
                    "symbol": str(symbol),
                    "last_funding_rate": Decimal(str(lfr)),
                    "mark_price": Decimal(str(mp)),
                    "event_time_ms": now_ms,
                }
            )
        except (TypeError, InvalidOperation):
            continue

    if not values:
        return

    stmt = insert(FundingRateSnapshot).values(values)
    stmt = stmt.on_conflict_do_update(
        index_elements=["symbol"],
        set_={
            "last_funding_rate": stmt.excluded.last_funding_rate,
            "mark_price": stmt.excluded.mark_price,
            "event_time_ms": stmt.excluded.event_time_ms,
        },
    )

    async with SessionLocal() as session:
        await session.execute(stmt)
        await session.commit()


async def get_funding_rate_top(limit: int, order: str) -> list[dict]:
    async with SessionLocal() as session:
        q = select(FundingRateSnapshot)
        rows = (await session.execute(q)).scalars().all()

    if not rows:
        # 首次启动可能还未由 ingest 刷新到数据，这里做一次兜底刷新
        await refresh_funding_rate_snapshots()
        async with SessionLocal() as session:
            rows = (await session.execute(select(FundingRateSnapshot))).scalars().all()

    if order == "abs":
        rows.sort(key=lambda x: abs(x.last_funding_rate), reverse=True)
    elif order == "asc":
        rows.sort(key=lambda x: x.last_funding_rate)
    else:
        rows.sort(key=lambda x: x.last_funding_rate, reverse=True)

    rows = rows[:limit]
    return [
        {
            "symbol": r.symbol,
            "lastFundingRate": float(r.last_funding_rate),
            "markPrice": float(r.mark_price),
            "timeMs": r.event_time_ms,
        }
        for r in rows
    ]
