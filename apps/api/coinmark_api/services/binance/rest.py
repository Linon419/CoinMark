from __future__ import annotations

import asyncio
import time
from typing import Any

import httpx

from coinmark_api.services.symbol_filter import filter_excluded_symbols


SPOT_REST = "https://api.binance.com"
FUTURES_REST = "https://fapi.binance.com"
BINANCE_BAPI_PRODUCTS = "https://www.binance.com/bapi/asset/v2/public/asset-service/product/get-products"


_CACHE_TTL_SEC = 6 * 3600
_OI_HIST_TTL_SEC = 90
_LSR_HIST_TTL_SEC = 90
_pairs_cache: dict[str, tuple[float, list[str]]] = {}  # market -> (ts, pairs)
_status_cache: dict[str, tuple[float, dict[str, str]]] = {}  # market -> (ts, {symbol: status})
_oi_hist_cache: dict[str, tuple[float, list[dict[str, Any]]]] = {}  # key -> (ts, items)
_lsr_hist_cache: dict[str, tuple[float, list[dict[str, Any]]]] = {}  # key -> (ts, items)
_pairs_lock = asyncio.Lock()


async def _get_json(url: str, params: dict[str, Any] | None = None, timeout_sec: float = 20.0) -> Any:
    async with httpx.AsyncClient(timeout=timeout_sec) as client:
        r = await client.get(url, params=params)
        r.raise_for_status()
        return r.json()


def _filter_usdt_symbols(symbols: list[dict[str, Any]], quote: str = "USDT") -> list[str]:
    out: list[str] = []
    for s in symbols:
        if s.get("status") not in ("TRADING", "TRADING"):  # 兼容字段差异
            continue
        if s.get("quoteAsset") != quote:
            continue
        sym = s.get("symbol")
        if not sym or not isinstance(sym, str):
            continue
        out.append(sym)
    return out


def _cache_valid(ts: float) -> bool:
    return time.time() - ts < _CACHE_TTL_SEC


def _oi_cache_valid(ts: float) -> bool:
    return time.time() - ts < _OI_HIST_TTL_SEC


def _lsr_cache_valid(ts: float) -> bool:
    return time.time() - ts < _LSR_HIST_TTL_SEC


async def _fetch_pairs_and_status(market: str) -> tuple[list[str], dict[str, str]]:
    if market == "spot":
        data = await _get_json(f"{SPOT_REST}/api/v3/exchangeInfo")
    else:
        data = await _get_json(f"{FUTURES_REST}/fapi/v1/exchangeInfo")
    symbols = data.get("symbols", [])
    if not isinstance(symbols, list):
        return [], {}
    pairs: list[str] = []
    status_map: dict[str, str] = {}
    for s in symbols:
        if not isinstance(s, dict):
            continue
        if s.get("quoteAsset") != "USDT":
            continue
        sym = s.get("symbol")
        if not sym or not isinstance(sym, str):
            continue
        status = s.get("status")
        if isinstance(status, str) and status:
            status_map[sym] = status
        if status == "TRADING":
            pairs.append(sym)
    pairs.sort()
    return pairs, status_map


async def get_pairs(market: str) -> list[str]:
    now = time.time()
    cached = _pairs_cache.get(market)
    if cached and _cache_valid(cached[0]):
        return cached[1]

    async with _pairs_lock:
        cached = _pairs_cache.get(market)
        if cached and _cache_valid(cached[0]):
            return cached[1]

        pairs, status_map = await _fetch_pairs_and_status(market)
        pairs = filter_excluded_symbols(pairs)
        _pairs_cache[market] = (now, pairs)
        _status_cache[market] = (now, status_map)
        return pairs


async def get_symbol_status(market: str, symbol: str) -> str | None:
    sym = symbol.upper()
    cached = _status_cache.get(market)
    if cached and _cache_valid(cached[0]):
        return cached[1].get(sym)

    async with _pairs_lock:
        cached = _status_cache.get(market)
        if cached and _cache_valid(cached[0]):
            return cached[1].get(sym)

        pairs, status_map = await _fetch_pairs_and_status(market)
        now = time.time()
        _pairs_cache[market] = (now, pairs)
        _status_cache[market] = (now, status_map)
        return status_map.get(sym)


async def get_futures_premium_index_all() -> list[dict[str, Any]]:
    data = await _get_json(f"{FUTURES_REST}/fapi/v1/premiumIndex")
    if isinstance(data, list):
        return data
    return []


async def get_futures_open_interest(symbol: str) -> dict[str, Any]:
    return await _get_json(f"{FUTURES_REST}/fapi/v1/openInterest", params={"symbol": symbol})


async def get_futures_premium_index(symbol: str) -> dict[str, Any]:
    return await _get_json(f"{FUTURES_REST}/fapi/v1/premiumIndex", params={"symbol": symbol})


async def get_open_interest_hist(symbol: str, period: str = "1h", limit: int = 24) -> list[dict[str, Any]]:
    sym = symbol.upper()
    key = f"{sym}:{period}:{int(limit)}"
    cached = _oi_hist_cache.get(key)
    if cached and _oi_cache_valid(cached[0]):
        return cached[1]

    params = {"symbol": sym, "period": period, "limit": int(limit)}
    data = await _get_json(f"{FUTURES_REST}/futures/data/openInterestHist", params=params)
    if not isinstance(data, list):
        data = []
    _oi_hist_cache[key] = (time.time(), data)
    return data


async def _get_lsr_hist(endpoint: str, symbol: str, period: str, limit: int) -> list[dict[str, Any]]:
    sym = symbol.upper()
    key = f"{endpoint}:{sym}:{period}:{int(limit)}"
    cached = _lsr_hist_cache.get(key)
    if cached and _lsr_cache_valid(cached[0]):
        return cached[1]

    params = {"symbol": sym, "period": period, "limit": int(limit)}
    data = await _get_json(f"{FUTURES_REST}/futures/data/{endpoint}", params=params)
    if not isinstance(data, list):
        data = []
    _lsr_hist_cache[key] = (time.time(), data)
    return data


async def get_global_long_short_account_ratio(symbol: str, period: str = "1h", limit: int = 24) -> list[dict[str, Any]]:
    return await _get_lsr_hist("globalLongShortAccountRatio", symbol, period, limit)


async def get_top_long_short_account_ratio(symbol: str, period: str = "1h", limit: int = 24) -> list[dict[str, Any]]:
    return await _get_lsr_hist("topLongShortAccountRatio", symbol, period, limit)


async def get_top_long_short_position_ratio(symbol: str, period: str = "1h", limit: int = 24) -> list[dict[str, Any]]:
    return await _get_lsr_hist("topLongShortPositionRatio", symbol, period, limit)


async def get_binance_bapi_products(include_etf: bool = True) -> list[dict[str, Any]]:
    params = {"includeEtf": "true" if include_etf else "false"}
    data = await _get_json(BINANCE_BAPI_PRODUCTS, params=params)
    if isinstance(data, dict) and data.get("code") == "000000":
        items = data.get("data")
        if isinstance(items, list):
            return items
    return []


async def get_ticker_24h_all(market: str) -> list[dict[str, Any]]:
    if market == "spot":
        data = await _get_json(f"{SPOT_REST}/api/v3/ticker/24hr")
    else:
        data = await _get_json(f"{FUTURES_REST}/fapi/v1/ticker/24hr")
    if isinstance(data, list):
        return data
    return []


async def get_ticker_24h(market: str, symbol: str) -> dict[str, Any]:
    params = {"symbol": symbol}
    if market == "spot":
        data = await _get_json(f"{SPOT_REST}/api/v3/ticker/24hr", params=params)
    else:
        data = await _get_json(f"{FUTURES_REST}/fapi/v1/ticker/24hr", params=params)
    if isinstance(data, dict):
        return data
    return {}


async def get_orderbook_depth(market: str, symbol: str, limit: int = 1000) -> dict[str, Any]:
    params = {"symbol": symbol, "limit": int(limit)}
    if market == "spot":
        data = await _get_json(f"{SPOT_REST}/api/v3/depth", params=params, timeout_sec=10.0)
    else:
        data = await _get_json(f"{FUTURES_REST}/fapi/v1/depth", params=params, timeout_sec=10.0)
    if isinstance(data, dict):
        return data
    return {}


async def get_klines(market: str, symbol: str, interval: str, limit: int = 200) -> list[list[Any]]:
    params = {"symbol": symbol, "interval": interval, "limit": limit}
    if market == "spot":
        data = await _get_json(f"{SPOT_REST}/api/v3/klines", params=params)
    else:
        data = await _get_json(f"{FUTURES_REST}/fapi/v1/klines", params=params)
    if isinstance(data, list):
        return data
    return []


def _interval_ms(interval: str) -> int:
    if interval.endswith("m"):
        return int(interval[:-1]) * 60 * 1000
    if interval.endswith("h"):
        return int(interval[:-1]) * 60 * 60 * 1000
    if interval.endswith("d"):
        return int(interval[:-1]) * 24 * 60 * 60 * 1000
    raise ValueError("unsupported interval")


async def get_klines_range(
    market: str,
    symbol: str,
    interval: str,
    start_ms: int,
    end_ms: int,
    limit: int = 1000,
) -> list[list[Any]]:
    cur = int(start_ms)
    out: list[list[Any]] = []
    step = _interval_ms(interval)
    while cur <= end_ms:
        params = {"symbol": symbol, "interval": interval, "startTime": cur, "endTime": end_ms, "limit": int(limit)}
        if market == "spot":
            data = await _get_json(f"{SPOT_REST}/api/v3/klines", params=params)
        else:
            data = await _get_json(f"{FUTURES_REST}/fapi/v1/klines", params=params)
        if not isinstance(data, list) or not data:
            break
        out.extend(data)
        last_open = int(data[-1][0])
        next_start = last_open + step
        if next_start <= cur:
            break
        cur = next_start
    return out
