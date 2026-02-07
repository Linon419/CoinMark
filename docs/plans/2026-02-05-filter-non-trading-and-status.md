# 过滤停牌币并提示详情状态 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 首页过滤非 TRADING 币种，详情页在非 TRADING 时提示暂无成交数据。

**Architecture:** 在 Binance REST 层缓存交易对状态（TRADING/BREAK/SETTLING），`/aggregate/basicinfo` 仅展示 TRADING 白名单；`/coin/detail/basic` 返回 `symbolStatus` 字段，前端据此展示提示文案。

**Tech Stack:** FastAPI, httpx, SQLAlchemy, React (Vite), TypeScript, pytest/pytest-asyncio

---

### Task 1: Binance 状态缓存 + get_symbol_status

**Files:**
- Modify: `apps/api/coinmark_api/services/binance/rest.py`
- Create: `apps/api/tests/test_binance_rest.py`

**Step 1: Write the failing test**

```python
import pytest

from coinmark_api.services.binance import rest

@pytest.mark.asyncio
async def test_get_symbol_status_from_cache(monkeypatch):
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
```

**Step 2: Run test to verify it fails**

Run: `cd apps/api; pytest tests/test_binance_rest.py::test_get_symbol_status_from_cache -v`
Expected: FAIL（`get_symbol_status` 不存在）

**Step 3: Write minimal implementation**
- 在 `rest.py` 增加 `_status_cache` 与 `_fetch_pairs_and_status`。
- `get_pairs` 复用 `_fetch_pairs_and_status` 填充状态缓存。
- 新增 `get_symbol_status(market, symbol)` 优先读缓存，过期时刷新。

**Step 4: Run test to verify it passes**

Run: `cd apps/api; pytest tests/test_binance_rest.py::test_get_symbol_status_from_cache -v`
Expected: PASS

**Step 5: Commit**

```bash
git add apps/api/coinmark_api/services/binance/rest.py apps/api/tests/test_binance_rest.py
git commit -m "feat: cache binance symbol status"
```

---

### Task 2: 首页过滤非 TRADING

**Files:**
- Modify: `apps/api/coinmark_api/api/routes/aggregate.py`
- Create: `apps/api/tests/test_basicinfo_filter.py`

**Step 1: Write the failing test**

```python
import pytest

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
```

**Step 2: Run test to verify it fails**

Run: `cd apps/api; pytest tests/test_basicinfo_filter.py::test_basicinfo_filters_non_trading -v`
Expected: FAIL（尚未过滤）

**Step 3: Write minimal implementation**
- `aggregate.basicinfo` 中引入 `get_pairs`，仅保留在 TRADING 白名单的 symbol。

**Step 4: Run test to verify it passes**

Run: `cd apps/api; pytest tests/test_basicinfo_filter.py::test_basicinfo_filters_non_trading -v`
Expected: PASS

**Step 5: Commit**

```bash
git add apps/api/coinmark_api/api/routes/aggregate.py apps/api/tests/test_basicinfo_filter.py
git commit -m "feat: filter non-trading symbols from basicinfo"
```

---

### Task 3: 详情页提示非 TRADING

**Files:**
- Modify: `apps/api/coinmark_api/api/routes/coin.py`
- Modify: `apps/web/src/pages/CoinPage.tsx`

**Step 1: Add API field**
- 在 `/coin/detail/basic` 返回新增字段 `symbolStatus`。

**Step 2: Frontend warning text**
- `CoinPage` 根据 `symbolStatus !== "TRADING"` 显示中文提示文字（仅文字，不做标签/隐藏图表）。

**Step 3: Manual verification**
- 运行服务后访问 `ALPACAUSDT` 详情页，确认显示“非 TRADING 暂无成交数据”提示。

**Step 4: Commit**

```bash
git add apps/api/coinmark_api/api/routes/coin.py apps/web/src/pages/CoinPage.tsx
git commit -m "feat: show non-trading warning on coin detail"
```
