# Coin Detail Snapshots (Hour/Day/Recent) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让币种详情页按原版样式展示“小时快照 / 每日快照 / 近日数据”，并补齐 24 小时持仓量快照数据。

**Architecture:** 后端新增 Binance openInterestHist 轻缓存接口与 `/coin/detail/oi/hourly` 路由；前端改为顶部标签页布局，小时/每日快照用资金净流入（spot/swap），近日数据保留 K 线，新增 24h 持仓量快照图；样式与布局对齐截图。

**Tech Stack:** FastAPI, httpx, React (Vite), TypeScript, ECharts

---

### Task 1: 后端补齐 24h 持仓量快照接口（轻缓存）

**Files:**
- Modify: `apps/api/coinmark_api/services/binance/rest.py`
- Modify: `apps/api/coinmark_api/api/routes/coin.py`
- Create: `apps/api/tests/test_open_interest_hist.py`

**Step 1: Write the failing test**

```python
import pytest

from coinmark_api.services.binance import rest


@pytest.mark.asyncio
async def test_open_interest_hist_cached(monkeypatch):
    rest._oi_hist_cache.clear()

    async def fake_get_json(url, params=None, timeout_sec=20.0):
        return [
            {"sumOpenInterestValue": "100", "timestamp": 1700000000000, "openInterest": "10"},
            {"sumOpenInterestValue": "120", "timestamp": 1700003600000, "openInterest": "12"},
        ]

    monkeypatch.setattr(rest, "_get_json", fake_get_json)

    items = await rest.get_open_interest_hist("ALPACAUSDT", period="1h", limit=2)
    assert items[0]["sumOpenInterestValue"] == "100"
```

**Step 2: Run test to verify it fails**

Run: `cd apps/api; python -m pytest tests/test_open_interest_hist.py::test_open_interest_hist_cached -v`
Expected: FAIL（函数/缓存不存在）

**Step 3: Write minimal implementation**
- `rest.py` 新增 `_oi_hist_cache` + TTL（60~120 秒）
- 新增 `get_open_interest_hist(symbol, period="1h", limit=24)` 调 Binance `fapi/v1/openInterestHist`

**Step 4: Add API route**
- `/coin/detail/oi/hourly?market=swap&symbol=XXX&hours=24`
- 返回 `{symbol, hours, items:[{bucketStartMs, openInterestUsd, openInterest}]}`

**Step 5: Run test to verify it passes**

Run: `cd apps/api; python -m pytest tests/test_open_interest_hist.py::test_open_interest_hist_cached -v`
Expected: PASS

---

### Task 2: 前端按原版改造布局与图表

**Files:**
- Modify: `apps/web/src/pages/CoinPage.tsx`
- Modify: `apps/web/src/styles/app.css`

**Step 1: 重构顶部布局**
- 顶部行：symbol + tabs（基本信息/小时快照/每日快照/近日数据）+ 刷新按钮 + 关闭

**Step 2: 小时快照图**
- 使用 `flows/hourly`：绘制 spot/swap 两条折线，显示点位标签

**Step 3: 24 小时持仓量快照图**
- 使用新接口：折线显示 `sumOpenInterestValue`，柱状显示 `openInterest`
- 底部展示最近 5 个小时的百分比变化

**Step 4: 每日快照/近日数据**
- 每日快照：沿用 `flows/daily` 的双线图
- 近日数据：沿用 15m K 线

**Step 5: 目测验收**
- 页面布局、标题、标签、图例与截图一致

