from __future__ import annotations

from typing import Literal

from fastapi import APIRouter, Body, HTTPException, Query
from pydantic import BaseModel, Field
from sqlalchemy import and_, delete, select

from coinmark_api.db import SessionLocal
from coinmark_api.db_upsert import insert
from coinmark_api.models import Favorite


router = APIRouter()


Market = Literal["spot", "swap"]


class FavoritesUpsertBody(BaseModel):
    market: Market = Field(..., description="spot 或 swap")
    symbols: list[str] = Field(..., min_length=1, description="交易对列表，例如 BTCUSDT")


@router.get("/user/favorites")
async def list_favorites(
    clientId: str = Query(..., min_length=8, max_length=64),
    market: Market | None = Query(None),
    limit: int = Query(200, ge=1, le=2000),
) -> dict:
    async with SessionLocal() as session:
        stmt = select(Favorite).where(Favorite.client_id == clientId)
        if market:
            stmt = stmt.where(Favorite.market == market)
        stmt = stmt.order_by(Favorite.created_at.desc()).limit(limit)
        rows = (await session.execute(stmt)).scalars().all()

    return {
        "clientId": clientId,
        "items": [{"market": r.market, "symbol": r.symbol, "createdAt": r.created_at.isoformat()} for r in rows],
    }


@router.post("/user/favorites")
async def add_favorites(
    clientId: str = Query(..., min_length=8, max_length=64),
    body: FavoritesUpsertBody = Body(...),
) -> dict:
    symbols = [s.strip().upper() for s in body.symbols if s and s.strip()]
    if not symbols:
        raise HTTPException(status_code=400, detail="symbols 不能为空")
    if len(symbols) > 500:
        raise HTTPException(status_code=400, detail="一次最多添加 500 个 symbols")

    values = [{"client_id": clientId, "market": body.market, "symbol": s} for s in symbols]
    stmt = insert(Favorite).values(values)
    stmt = stmt.on_conflict_do_nothing(index_elements=["client_id", "market", "symbol"])

    async with SessionLocal() as session:
        await session.execute(stmt)
        await session.commit()

    return {"ok": True, "clientId": clientId, "market": body.market, "added": symbols}


@router.delete("/user/favorites/{symbol}")
async def remove_favorite(
    symbol: str,
    clientId: str = Query(..., min_length=8, max_length=64),
    market: Market = Query(...),
) -> dict:
    sym = symbol.strip().upper()
    if not sym:
        raise HTTPException(status_code=400, detail="symbol 不能为空")

    async with SessionLocal() as session:
        stmt = delete(Favorite).where(
            and_(Favorite.client_id == clientId, Favorite.market == market, Favorite.symbol == sym)
        )
        res = await session.execute(stmt)
        await session.commit()

    try:
        deleted = int(res.rowcount or 0)
    except Exception:
        deleted = 0
    return {"ok": True, "clientId": clientId, "market": market, "symbol": sym, "deleted": deleted}

