import pytest

from coinmark_api.services.binance import rest


@pytest.mark.asyncio
async def test_get_symbol_status_from_cache(monkeypatch):
    rest._pairs_cache.clear()
    rest._status_cache.clear()

    async def fake_get_json(url, params=None, timeout_sec=20.0):
        return {
            "symbols": [
                {"symbol": "AAAUSDT", "status": "TRADING", "quoteAsset": "USDT"},
                {"symbol": "ALPACAUSDT", "status": "SETTLING", "quoteAsset": "USDT"},
            ]
        }

    monkeypatch.setattr(rest, "_get_json", fake_get_json)

    status = await rest.get_symbol_status("swap", "ALPACAUSDT")
    assert status == "SETTLING"
