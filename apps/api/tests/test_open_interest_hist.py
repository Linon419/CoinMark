import pytest

from coinmark_api.services.binance import rest


@pytest.mark.asyncio
async def test_open_interest_hist_cached(monkeypatch):
    rest._oi_hist_cache.clear()

    calls = {"count": 0}

    async def fake_get_json(url, params=None, timeout_sec=20.0):
        calls["count"] += 1
        return [
            {"sumOpenInterestValue": "100", "timestamp": 1700000000000, "openInterest": "10"},
            {"sumOpenInterestValue": "120", "timestamp": 1700003600000, "openInterest": "12"},
        ]

    monkeypatch.setattr(rest, "_get_json", fake_get_json)

    items = await rest.get_open_interest_hist("ALPACAUSDT", period="1h", limit=2)
    items2 = await rest.get_open_interest_hist("ALPACAUSDT", period="1h", limit=2)

    assert items[0]["sumOpenInterestValue"] == "100"
    assert items2[1]["sumOpenInterestValue"] == "120"
    assert calls["count"] == 1
