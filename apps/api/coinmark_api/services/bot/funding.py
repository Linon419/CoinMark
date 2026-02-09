from __future__ import annotations

from coinmark_api.ch import query_funding_snapshots


async def get_funding_rate_top(limit: int, order: str) -> list[dict]:
    rows = await query_funding_snapshots()
    if order == "abs":
        rows.sort(key=lambda x: abs(x.last_funding_rate), reverse=True)
    elif order == "asc":
        rows.sort(key=lambda x: x.last_funding_rate)
    else:
        rows.sort(key=lambda x: x.last_funding_rate, reverse=True)
    rows = rows[:limit]
    return [
        {"symbol": r.symbol, "lastFundingRate": float(r.last_funding_rate), "markPrice": float(r.mark_price), "timeMs": r.event_time_ms}
        for r in rows
    ]
