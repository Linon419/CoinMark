from __future__ import annotations

import re


_STABLE_BASE_ASSETS = {
    "USDC",
    "USDT",
    "BUSD",
    "FDUSD",
    "TUSD",
    "USDP",
    "DAI",
    "FRAX",
    "USDD",
    "USDE",
    "USD1",
    "PYUSD",
    "RLUSD",
    "LUSD",
    "SUSD",
    "USDS",
}


def _symbol_base_asset(symbol: str) -> str:
    sym = str(symbol or "").strip().upper()
    if not sym:
        return ""
    base = sym
    for quote in ("USDT", "USDC", "BUSD", "FDUSD", "TUSD", "USDP"):
        if base.endswith(quote) and len(base) > len(quote):
            base = base[: -len(quote)]
            break
    base = re.sub(r"^\d+", "", base)
    return base


def is_excluded_symbol(symbol: str | None) -> bool:
    if not symbol:
        return True
    base = _symbol_base_asset(str(symbol))
    if not base:
        return True
    return base in _STABLE_BASE_ASSETS


def filter_excluded_symbols(symbols: list[str]) -> list[str]:
    out: list[str] = []
    for symbol in symbols:
        if not is_excluded_symbol(symbol):
            out.append(symbol)
    return out

