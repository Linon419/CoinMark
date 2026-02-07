import asyncio
import time
from typing import Any

import httpx


_CACHE_TTL_SEC = 6 * 60 * 60
_cache: dict[str, dict[str, Any]] = {}
_cache_ts: float = 0.0
_lock = asyncio.Lock()


def _to_float(v) -> float | None:
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


async def _fetch_apex_compliance() -> list[dict[str, Any]]:
    url = "https://www.binance.com/bapi/apex/v1/friendly/apex/marketing/complianceSymbolList"
    headers = {"User-Agent": "coinmark/1.0"}
    async with httpx.AsyncClient(timeout=8) as client:
        resp = await client.get(url, headers=headers)
        resp.raise_for_status()
        data = resp.json()
    if isinstance(data, dict):
        items = data.get("data")
        if isinstance(items, list):
            return items
        if isinstance(items, dict) and isinstance(items.get("data"), list):
            return items["data"]
    return []


async def _fetch_asset_products() -> list[dict[str, Any]]:
    url = "https://www.binance.com/exchange-api/v2/public/asset-service/product/get-products"
    params = {"includeEtf": "true"}
    headers = {"User-Agent": "coinmark/1.0"}
    async with httpx.AsyncClient(timeout=8) as client:
        resp = await client.get(url, params=params, headers=headers)
        resp.raise_for_status()
        data = resp.json()
    if isinstance(data, dict):
        items = data.get("data")
        if isinstance(items, list):
            return items
        if isinstance(items, dict) and isinstance(items.get("data"), list):
            return items["data"]
    return []


def _merge_snapshot(base: str, row: dict[str, Any], cur: dict[str, Any] | None) -> dict[str, Any]:
    if cur is None:
        return row
    for key in ("circulating_supply", "total_supply", "max_supply", "market_cap", "price"):
        if cur.get(key) is None and row.get(key) is not None:
            cur[key] = row[key]
    if cur.get("source") is None and row.get("source") is not None:
        cur["source"] = row["source"]
    return cur


async def _refresh_cache() -> None:
    global _cache_ts
    next_cache: dict[str, dict[str, Any]] = {}

    try:
        apex_rows = await _fetch_apex_compliance()
    except Exception:
        apex_rows = []

    for row in apex_rows:
        base = str(row.get("name") or "").upper()
        symbol_pair = str(row.get("symbol") or "").upper()
        if not base and symbol_pair.endswith("USDT"):
            base = symbol_pair[: -len("USDT")]
        if not base:
            continue
        entry = {
            "symbol_pair": symbol_pair or None,
            "circulating_supply": _to_float(row.get("circulatingSupply")),
            "total_supply": _to_float(row.get("totalSupply")),
            "max_supply": _to_float(row.get("maxSupply")),
            "market_cap": _to_float(row.get("marketCap")),
            "price": _to_float(row.get("price")),
            "source": "binance_apex",
        }
        cur = next_cache.get(base)
        if cur is None:
            next_cache[base] = entry
        else:
            cur_cap = cur.get("market_cap") or 0
            next_cap = entry.get("market_cap") or 0
            if next_cap > cur_cap:
                next_cache[base] = _merge_snapshot(base, entry, cur)
            else:
                next_cache[base] = _merge_snapshot(base, entry, cur)

    try:
        product_rows = await _fetch_asset_products()
    except Exception:
        product_rows = []

    for row in product_rows:
        base = str(row.get("b") or "").upper()
        quote = str(row.get("q") or "").upper()
        symbol_pair = str(row.get("s") or "").upper()
        if not base and symbol_pair.endswith("USDT"):
            base = symbol_pair[: -len("USDT")]
            quote = "USDT"
        if not base or (quote and quote != "USDT"):
            continue
        entry = {
            "symbol_pair": symbol_pair or None,
            "circulating_supply": _to_float(row.get("cs")),
            "market_cap": None,
            "price": _to_float(row.get("c")),
            "source": "binance_asset",
        }
        cur = next_cache.get(base)
        next_cache[base] = _merge_snapshot(base, entry, cur)

    _cache.clear()
    _cache.update(next_cache)
    _cache_ts = time.time()


async def get_supply_snapshot(symbol: str) -> dict[str, Any] | None:
    now = time.time()
    if now - _cache_ts > _CACHE_TTL_SEC or not _cache:
        async with _lock:
            if time.time() - _cache_ts > _CACHE_TTL_SEC or not _cache:
                await _refresh_cache()

    return _cache.get(symbol.upper())
