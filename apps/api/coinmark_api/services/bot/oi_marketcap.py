from __future__ import annotations

from coinmark_api.ch import query_oi_snapshots, query_market_caps


def _base_from_symbol(symbol: str) -> str:
    if symbol.endswith("USDT"):
        return symbol[: -len("USDT")]
    return symbol


async def get_oi_marketcap_rank(limit: int) -> list[dict]:
    oi_rows = await query_oi_snapshots()
    cap_rows = await query_market_caps()
    cap_map = {r.asset: r for r in cap_rows}
    items = []
    for oi in oi_rows:
        asset = _base_from_symbol(oi.symbol)
        cap = cap_map.get(asset)
        if not cap or cap.market_cap_usd <= 0:
            continue
        ratio = oi.oi_notional_usd / cap.market_cap_usd
        items.append({
            "symbol": oi.symbol, "asset": asset,
            "oiNotionalUsd": float(oi.oi_notional_usd), "marketCapUsd": float(cap.market_cap_usd),
            "ratio": float(ratio), "marketCapSource": cap.source,
            "updatedAtMs": min(int(oi.event_time_ms), int(cap.event_time_ms)),
        })
    items.sort(key=lambda x: x["ratio"], reverse=True)
    return items[:limit]
