from __future__ import annotations

from fastapi import APIRouter, Query

from coinmark_api.services.binance.rest import get_pairs


router = APIRouter()


@router.get("/symbol/getpairs")
async def getpairs(
    market: str = Query("spot", pattern="^(spot|swap)$"),
) -> dict:
    pairs = await get_pairs(market=market)
    return {"market": market, "pairs": pairs}

