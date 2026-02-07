from __future__ import annotations

from fastapi import APIRouter, Query

from coinmark_api.services.bot.funding import get_funding_rate_top
from coinmark_api.services.bot.long_short_volume import get_long_short_volume_rank
from coinmark_api.services.bot.oi_marketcap import get_oi_marketcap_rank


router = APIRouter()


@router.get("/fundingRateTop")
async def funding_rate_top(
    limit: int = Query(15, ge=1, le=200),
    order: str = Query("abs", pattern="^(abs|desc|asc)$"),
) -> dict:
    data = await get_funding_rate_top(limit=limit, order=order)
    return {"items": data}


@router.get("/longShortVolumeRank")
async def long_short_volume_rank(
    market: str = Query("swap", pattern="^(spot|swap)$"),
    bucket: str = Query("15m", pattern="^(15m|1h|1d)$"),
    limit: int = Query(10, ge=1, le=200),
) -> dict:
    data = await get_long_short_volume_rank(market=market, bucket=bucket, limit=limit)
    return data


@router.get("/oiMarketCapRank")
async def oi_marketcap_rank(
    limit: int = Query(15, ge=1, le=200),
) -> dict:
    data = await get_oi_marketcap_rank(limit=limit)
    return {"items": data}

