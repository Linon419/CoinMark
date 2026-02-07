import os

import pytest

os.environ.setdefault("DATABASE_URL", "postgresql+asyncpg://coinmark:coinmark@localhost:5432/coinmark")
os.environ.setdefault("REDIS_URL", "redis://localhost:6379/0")

from coinmark_api.api.routes import aggregate


@pytest.mark.asyncio
async def test_basicinfo_filters_non_trading(monkeypatch):
    async def fake_ticker_all(market):
        return [
            {"symbol": "AAAUSDT", "priceChangePercent": "1", "lastPrice": "1", "quoteVolume": "10"},
            {"symbol": "BBBUSDT", "priceChangePercent": "2", "lastPrice": "2", "quoteVolume": "20"},
        ]

    async def fake_pairs(market):
        return ["AAAUSDT"]

    monkeypatch.setattr(aggregate, "get_ticker_24h_all", fake_ticker_all)
    monkeypatch.setattr(aggregate, "get_pairs", fake_pairs)

    resp = await aggregate.basicinfo(market="swap", limit=50)
    symbols = {x["symbol"] for x in (resp["gainers"] + resp["losers"])}
    assert symbols == {"AAAUSDT"}
