from __future__ import annotations

import time
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo
from decimal import Decimal
from typing import Literal, Any

from fastapi import APIRouter, Query
from sqlalchemy import and_, select
import asyncio

from coinmark_api.config import settings
from coinmark_api.db import SessionLocal
from coinmark_api.models import SRLevel
from coinmark_api.ch import (
    TradeBucketRow,
    OBFeatureRow,
    query_trade_buckets,
    query_orderbook_features,
    query_funding_by_symbol,
    query_oi_by_symbol,
    query_market_cap_by_asset,
)
from coinmark_api.services.institutional_levels import get_symbol_latest_institutional_levels
from coinmark_api.services.binance.rest import (
    get_pairs,
    get_global_long_short_account_ratio,
    get_klines_range,
    get_open_interest_hist,
    get_symbol_status,
    get_ticker_24h,
    get_top_long_short_account_ratio,
    get_top_long_short_position_ratio,
)
from coinmark_api.services.market_supply import get_supply_snapshot
from coinmark_api.services.binance.backfill import backfill_trade_buckets_from_klines


router = APIRouter()

Market = Literal["spot", "swap"]
TimeMode = Literal["utc", "local"]

_SIGNAL_LEVEL_RANK: dict[str, int] = {
    "NONE": 0,
    "WATCH": 1,
    "CONFIRM": 2,
    "STRONG": 3,
}
_SIGNAL_COOLDOWN_MS: dict[str, int] = {
    "WATCH": 20 * 60 * 1000,
    "CONFIRM": 30 * 60 * 1000,
    "STRONG": 60 * 60 * 1000,
}
_ABSORPTION_SIGNAL_COOLDOWN_STATE: dict[str, dict[str, Any]] = {}
_FUND_SNAPSHOT_REPAIR_TS_MS: dict[str, int] = {}


def _base_from_symbol(symbol: str) -> str:
    sym = symbol.upper()
    if sym.endswith("USDT"):
        return sym[: -len("USDT")]
    return sym


def _to_float(v) -> float | None:
    if v is None:
        return None
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def _safe_mean(values: list[float]) -> float | None:
    if not values:
        return None
    return sum(values) / len(values)


def _clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


def _percentile_rank(values: list[float], value: float) -> float | None:
    if not values:
        return None
    sorted_vals = sorted(values)
    n = len(sorted_vals)
    le_count = 0
    for item in sorted_vals:
        if item <= value:
            le_count += 1
        else:
            break
    return le_count / n
    try:
        return float(v)
    except Exception:
        return None




def _bucket_ms(bucket: str) -> int:
    if bucket == "1m":
        return 60 * 1000
    if bucket == "5m":
        return 5 * 60 * 1000
    if bucket == "15m":
        return 15 * 60 * 1000
    if bucket == "1h":
        return 60 * 60 * 1000
    if bucket == "1d":
        return 24 * 60 * 60 * 1000
    raise ValueError("unsupported bucket")


def _floor_bucket_start_ms(ts: int, bucket: str) -> int:
    bms = _bucket_ms(bucket)
    return (int(ts) // bms) * bms


def _floor_bucket_start_with_offset_ms(ts: int, bucket_ms: int, offset_ms: int) -> int:
    return ((int(ts) - int(offset_ms)) // int(bucket_ms)) * int(bucket_ms) + int(offset_ms)


def _day_start_ms_by_mode(now_utc_ms: int, time_mode: TimeMode, tz_offset_min: int) -> int:
    day_ms = 24 * 60 * 60 * 1000
    if time_mode == "local":
        offset_ms = int(tz_offset_min) * 60 * 1000
        return _floor_bucket_start_with_offset_ms(now_utc_ms, day_ms, offset_ms)
    return (int(now_utc_ms) // day_ms) * day_ms


def _weekday_by_mode(now_utc_ms: int, time_mode: TimeMode, tz_offset_min: int) -> int:
    if time_mode == "local":
        shifted_ms = int(now_utc_ms) - int(tz_offset_min) * 60 * 1000
        return datetime.utcfromtimestamp(shifted_ms / 1000).weekday()
    return datetime.utcfromtimestamp(now_utc_ms / 1000).weekday()


async def _resolve_effective_market_for_symbol(market: Market, symbol: str) -> tuple[Market, bool]:
    """当请求 spot 但该交易对仅有合约时，自动回退到 swap。"""
    if market != "spot":
        return market, False

    sym = symbol.upper()
    try:
        spot_pairs = set(await get_pairs("spot"))
        if sym in spot_pairs:
            return "spot", False

        swap_pairs = set(await get_pairs("swap"))
        if sym in swap_pairs:
            return "swap", True
    except Exception:
        return market, False

    return market, False


def _build_snapshot_targets(now_utc_ms: int, tz_offset_min: int, time_mode: TimeMode) -> tuple[list[int], list[int], int]:
    day_ms = 24 * 60 * 60 * 1000
    hour_ms = 60 * 60 * 1000
    day_start_ms = _day_start_ms_by_mode(now_utc_ms, time_mode, tz_offset_min)
    offset_ms = int(tz_offset_min) * 60 * 1000 if time_mode == "local" else 0
    last_label_ms = _floor_bucket_start_with_offset_ms(now_utc_ms, hour_ms, offset_ms)
    if last_label_ms < day_start_ms:
        return ([], [], day_start_ms)
    label_ts = list(range(day_start_ms, last_label_ms + 1, hour_ms))
    cutoffs = [ts + hour_ms for ts in label_ts]
    return (label_ts, cutoffs, day_start_ms)


def _cluster_ranges(prices: list[float], cluster_pct: float, min_band_pct: float) -> list[tuple[float, float, float]]:
    if not prices:
        return []
    ps = sorted(prices)
    clusters: list[list[float]] = [[ps[0]]]
    for p in ps[1:]:
        cur = clusters[-1]
        ref = cur[-1]
        tol = max(abs(ref) * cluster_pct, abs(p) * cluster_pct)
        if abs(p - ref) <= tol:
            cur.append(p)
        else:
            clusters.append([p])
    out: list[tuple[float, float, float]] = []
    for c in clusters:
        c_sorted = sorted(c)
        low = c_sorted[0]
        high = c_sorted[-1]
        mid = c_sorted[len(c_sorted) // 2]
        if high - low <= abs(mid) * 0.0001:
            band = abs(mid) * min_band_pct
            low = mid - band
            high = mid + band
        out.append((low, high, mid))
    return out


def _fmt_pct(val: float | None, digits: int = 2) -> str:
    if val is None or not isinstance(val, (int, float)):
        return "-"
    return f"{val * 100:.{digits}f}%"


def _fmt_price(val: float | None) -> str:
    if val is None or not isinstance(val, (int, float)):
        return "-"
    abs_v = abs(float(val))
    if abs_v < 0.01:
        return f"{val:.8f}"
    if abs_v < 1:
        return f"{val:.6f}"
    if abs_v < 1000:
        return f"{val:.2f}"
    return f"{val:.0f}"


def _fmt_cn_amount(v: float | None, unit: str) -> str:
    if v is None or not isinstance(v, (int, float)):
        return "-"
    val = float(v)
    abs_v = abs(val)
    if abs_v >= 1e12:
        return f"{val / 1e12:.2f}万亿{unit}"
    if abs_v >= 1e8:
        return f"{val / 1e8:.2f}亿{unit}"
    if abs_v >= 1e4:
        return f"{val / 1e4:.2f}万{unit}"
    if abs_v >= 1:
        return f"{val:.2f}{unit}"
    return f"{val:.4f}{unit}"


def _fmt_duration(ms: int) -> str:
    if ms < 0:
        ms = 0
    minute_ms = 60 * 1000
    hour_ms = 60 * 60 * 1000
    day_ms = 24 * 60 * 60 * 1000
    if ms >= day_ms:
        days = ms // day_ms
        hours = (ms % day_ms) // hour_ms
        return f"{days}天{hours}小时"
    if ms >= hour_ms:
        return f"{ms // hour_ms}小时"
    if ms >= minute_ms:
        return f"{ms // minute_ms}分"
    return f"{max(1, ms // 1000)}秒"


def _trend_label(score: int) -> str:
    if score <= 20:
        return "强下降"
    if score <= 40:
        return "下降"
    if score <= 60:
        return "中性"
    if score <= 80:
        return "上升"
    return "强上升"


def _extract_candles(rows: list[TradeBucketRow]) -> list[dict]:
    out: list[dict] = []
    for r in rows:
        o = _to_float(r.open_price)
        h = _to_float(r.high_price)
        l = _to_float(r.low_price)
        c = _to_float(r.close_price)
        if o is None or h is None or l is None or c is None:
            continue
        out.append(
            {
                "start_ms": int(r.bucket_start_ms),
                "open": o,
                "high": h,
                "low": l,
                "close": c,
                "quote_notional": _to_float(r.quote_notional) or 0.0,
            }
        )
    return out


def _zigzag_pivots(candles: list[tuple[int, float, float, float, float]], reversal_pct: float, min_bars: int) -> list[float]:
    if len(candles) < 2:
        return []

    rev = abs(reversal_pct)
    pivots: list[float] = []
    trend = 0

    last_high = candles[0][2]
    last_low = candles[0][3]
    last_high_idx = 0
    last_low_idx = 0

    for idx, (_, _o, h, l, _c) in enumerate(candles[1:], start=1):
        if h > last_high:
            last_high = h
            last_high_idx = idx
        if l < last_low:
            last_low = l
            last_low_idx = idx

        if trend >= 0 and last_high > 0:
            if (last_high - l) / last_high >= rev and (idx - last_high_idx) >= min_bars:
                pivots.append(last_high)
                trend = -1
                last_low = l
                last_low_idx = idx

        if trend <= 0 and last_low > 0:
            if (h - last_low) / last_low >= rev and (idx - last_low_idx) >= min_bars:
                pivots.append(last_low)
                trend = 1
                last_high = h
                last_high_idx = idx

    return pivots


@router.get("/coin/detail/basic")
async def coin_detail_basic(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    time_mode: TimeMode = Query("utc", alias="timeMode", pattern="^(utc|local)$"),
    tz_offset_min: int = Query(0, alias="tzOffsetMin"),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    asset = _base_from_symbol(sym)

    basic_raw = await get_ticker_24h(effective_market, sym)

    def _num(key: str) -> float | None:
        try:
            return float(basic_raw.get(key))
        except (TypeError, ValueError):
            return None

    last_price = _num("lastPrice")
    basic = {
        "symbol": sym,
        "asset": asset,
        "market": effective_market,
        "lastPrice": last_price,
        "priceChangePercent24h": _num("priceChangePercent"),
        "highPrice24h": _num("highPrice"),
        "lowPrice24h": _num("lowPrice"),
        "quoteVolume24h": _num("quoteVolume"),
        "eventTimeMs": int(basic_raw.get("closeTime") or basic_raw.get("eventTime") or time.time() * 1000),
        "source": "binance_ticker_24h",
    }

    status = None
    try:
        status = await get_symbol_status(effective_market, sym)
    except Exception:
        status = None

    now_ms = int(time.time() * 1000)
    day_ms = 24 * 60 * 60 * 1000
    day_start_ms = _day_start_ms_by_mode(now_ms, time_mode, tz_offset_min)
    last_closed_day_ms = day_start_ms - day_ms
    daily_start_ms = last_closed_day_ms - 90 * day_ms
    hourly_start_ms = now_ms - 8 * day_ms
    m15_start_ms = now_ms - day_ms

    fund = None
    oi = None
    if effective_market == "swap":
        fund = await query_funding_by_symbol(sym)
        oi = await query_oi_by_symbol(sym)
    cap = await query_market_cap_by_asset(asset)

    async with SessionLocal() as session:
        levels = (
            (
                await session.execute(
                    select(SRLevel).where(
                        and_(SRLevel.market == effective_market, SRLevel.symbol == sym, SRLevel.timeframe == "4h")
                    )
                )
            )
            .scalars()
            .all()
        )

    daily_rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="1d", start_ms=daily_start_ms, end_ms=last_closed_day_ms)
    hourly_rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="1h", start_ms=hourly_start_ms)
    m15_rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="15m", start_ms=m15_start_ms)

    if last_price is not None:
        levels.sort(key=lambda r: abs(float(r.level_price) - float(last_price)))
    else:
        levels.sort(key=lambda r: float(r.strength_score), reverse=True)
    levels = levels[:12]

    daily_candles = _extract_candles(daily_rows)
    hourly_candles = _extract_candles(hourly_rows)
    m15_candles = _extract_candles(m15_rows)

    daily_by_start = {c["start_ms"]: c for c in daily_candles}

    # 本地时区口径下，trade_buckets 的 1d 是 UTC 切分，可能导致按本地日键查不到。
    # 这里用 1h 数据构造“本地日收盘”回退，确保昨日/上周/本周涨幅有值。
    local_daily_close_by_start: dict[int, dict[str, float]] = {}
    if time_mode == "local" and hourly_candles:
        temp: dict[int, tuple[int, float]] = {}
        for c in hourly_candles:
            ds = _day_start_ms_by_mode(int(c["start_ms"]), time_mode, tz_offset_min)
            cur = temp.get(ds)
            if cur is None or int(c["start_ms"]) > cur[0]:
                temp[ds] = (int(c["start_ms"]), float(c["close"]))
        local_daily_close_by_start = {k: {"close": v[1]} for k, v in temp.items()}

    last_closed = daily_by_start.get(last_closed_day_ms)
    prev_closed = daily_by_start.get(last_closed_day_ms - day_ms)

    if time_mode == "local":
        if last_closed is None:
            last_closed = local_daily_close_by_start.get(last_closed_day_ms)
        if prev_closed is None:
            prev_closed = local_daily_close_by_start.get(last_closed_day_ms - day_ms)

    # 价格区间（近8天1h）
    range_label = "-"
    if hourly_candles and last_price is not None:
        range_high = max(c["high"] for c in hourly_candles)
        range_low = min(c["low"] for c in hourly_candles)
        if range_high > range_low:
            pos = (last_price - range_low) / (range_high - range_low)
            high_ts = max(c["start_ms"] for c in hourly_candles if c["high"] == range_high)
            low_ts = max(c["start_ms"] for c in hourly_candles if c["low"] == range_low)
            if pos <= 0.2:
                range_label = f"底部 ({_fmt_duration(now_ms - low_ts)})"
            elif pos >= 0.8:
                range_label = f"顶部 ({_fmt_duration(now_ms - high_ts)})"
            else:
                range_label = f"中继 ({_fmt_duration(now_ms - max(high_ts, low_ts))})"

    # 振幅量能
    amp_pct = None
    high_24h = basic.get("highPrice24h")
    low_24h = basic.get("lowPrice24h")
    if isinstance(high_24h, (int, float)) and isinstance(low_24h, (int, float)) and low_24h > 0:
        amp_pct = (high_24h - low_24h) / low_24h

    vol_factor = None
    quote_24h = basic.get("quoteVolume24h")
    if daily_candles and isinstance(quote_24h, (int, float)) and quote_24h > 0:
        recent = daily_candles[-7:] if len(daily_candles) >= 7 else daily_candles
        avg_vol = sum(c["quote_notional"] for c in recent) / max(1, len(recent))
        if avg_vol > 0:
            vol_factor = quote_24h / avg_vol

    # 今日波动（15m）
    up_cnt = 0
    down_cnt = 0
    for c in m15_candles:
        o = c["open"]
        if o <= 0:
            continue
        ret = (c["close"] - o) / o
        if ret >= 0.01:
            up_cnt += 1
        elif ret <= -0.01:
            down_cnt += 1
    swing_total = up_cnt + down_cnt
    swing_label = "-" if swing_total == 0 else f"{swing_total}次 (涨{up_cnt},跌{down_cnt})"

    # 摸顶探底（近8天1h，按已收盘小时统计）
    touch_label = "-"
    if hourly_candles:
        hour_ms = 60 * 60 * 1000
        current_hour_start = (now_ms // hour_ms) * hour_ms
        closed_hourly = [c for c in hourly_candles if c["start_ms"] < current_hour_start]

        if len(closed_hourly) >= 2:
            # 第一根作为基准，不计入“第N次”
            max_high = float(closed_hourly[0]["high"])
            min_low = float(closed_hourly[0]["low"])
            high_cnt = 0
            low_cnt = 0
            last_event = None

            for c in closed_hourly[1:]:
                if c["high"] > max_high:
                    max_high = c["high"]
                    high_cnt += 1
                    last_event = ("新高", high_cnt, c["start_ms"])
                if c["low"] < min_low:
                    min_low = c["low"]
                    low_cnt += 1
                    last_event = ("新低", low_cnt, c["start_ms"])

            if last_event:
                etype, cnt, ts = last_event
                touch_label = f"第{cnt}次{etype} ({_fmt_duration(now_ms - ts)}前)"

    # 昨日/上周/本周涨幅
    yesterday_ret = None
    if last_closed and prev_closed and prev_closed["close"] > 0:
        yesterday_ret = (last_closed["close"] - prev_closed["close"]) / prev_closed["close"]

    week_ret = None
    if last_closed:
        weekday = _weekday_by_mode(now_ms, time_mode, tz_offset_min)
        week_start_ms = int(day_start_ms - weekday * day_ms)
        week_start = daily_by_start.get(week_start_ms)
        if week_start is None and time_mode == "local":
            week_start = local_daily_close_by_start.get(week_start_ms)
        if week_start and week_start["close"] > 0:
            week_ret = (last_closed["close"] - week_start["close"]) / week_start["close"]

    last_week_ret = None
    prev_7d = daily_by_start.get(last_closed_day_ms - 7 * day_ms)
    if prev_7d is None and time_mode == "local":
        prev_7d = local_daily_close_by_start.get(last_closed_day_ms - 7 * day_ms)
    if last_closed and prev_7d and prev_7d["close"] > 0:
        last_week_ret = (last_closed["close"] - prev_7d["close"]) / prev_7d["close"]

    # 趋势分数
    def _trend_score(days: int) -> tuple[int | None, str | None]:
        if len(daily_candles) < days:
            return (None, None)
        seg = daily_candles[-days:]
        first = seg[0]["close"]
        last = seg[-1]["close"]
        if first <= 0:
            return (None, None)
        pct = (last - first) / first
        score = int(round(50 + 200 * pct))
        score = max(0, min(100, score))
        return (score, _trend_label(score))

    score_6, label_6 = _trend_score(6)
    score_60, label_60 = _trend_score(60)

    # OI 与市值
    oi_change_pct = None
    oi_value_change_pct = None
    if effective_market == "swap":
        try:
            oi_hist = await get_open_interest_hist(sym, period="1h", limit=25)
        except Exception:
            oi_hist = []
        rows = []
        for r in oi_hist:
            try:
                ts = int(r.get("timestamp") or 0)
                oi_raw = r.get("openInterest") or r.get("sumOpenInterest") or 0
                oi_val = float(oi_raw)
                oi_notional = float(r.get("sumOpenInterestValue") or 0)
            except (TypeError, ValueError):
                continue
            rows.append((ts, oi_val, oi_notional))
        rows.sort(key=lambda x: x[0])
        if len(rows) >= 2:
            prev = rows[0]
            latest = rows[-1]
            if prev[1] > 0:
                oi_change_pct = (latest[1] - prev[1]) / prev[1]
            if prev[2] > 0:
                oi_value_change_pct = (latest[2] - prev[2]) / prev[2]

    open_interest = _to_float(oi.open_interest) if oi else None
    oi_notional = _to_float(oi.oi_notional_usd) if oi else None

    long_index = None
    if effective_market == "swap":
        try:
            lsr_hist = await get_global_long_short_account_ratio(sym, period="1h", limit=6)
        except Exception:
            lsr_hist = []
        ratios = []
        for r in lsr_hist:
            try:
                ratios.append(float(r.get("longShortRatio")))
            except (TypeError, ValueError):
                continue
        if ratios:
            avg_ratio = sum(ratios) / len(ratios)
            long_index = max(0.0, min(20.0, round(10 + (avg_ratio - 1) * 10, 1)))

    supply = None
    try:
        supply = await get_supply_snapshot(asset)
    except Exception:
        supply = None

    supply_circ = _to_float(supply.get("circulating_supply")) if supply else None
    supply_total = _to_float(supply.get("total_supply")) if supply else None
    supply_max = _to_float(supply.get("max_supply")) if supply else None
    supply_price = _to_float(supply.get("price")) if supply else None
    supply_cap = _to_float(supply.get("market_cap")) if supply else None

    price_for_cap = last_price or supply_price or (_to_float(cap.price_usd) if cap else None)

    market_cap = None
    if supply_circ is not None and price_for_cap is not None:
        market_cap = supply_circ * price_for_cap
    elif supply_cap is not None:
        market_cap = supply_cap
    else:
        market_cap = _to_float(cap.market_cap_usd) if cap else None

    circ_supply = supply_circ if supply_circ is not None else (_to_float(cap.circulating_supply) if cap else None)

    position_ratio = None
    if open_interest is not None and circ_supply is not None and circ_supply > 0:
        position_ratio = open_interest / circ_supply

    diluted_cap = None
    total_supply = supply_total if supply_total is not None else supply_max
    if total_supply is not None and price_for_cap is not None:
        diluted_cap = total_supply * price_for_cap

    circ_pct = None
    if market_cap is not None and diluted_cap is not None and diluted_cap > 0:
        circ_pct = market_cap / diluted_cap

    price_change_pct = None
    if isinstance(basic.get("priceChangePercent24h"), (int, float)):
        price_change_pct = basic["priceChangePercent24h"] / 100

    circ_pct_label = f" ({_fmt_pct(circ_pct, 2)})" if circ_pct is not None else ""

    basic_kvp = [
        {"label": "今日价格", "value": f"{_fmt_price(last_price)} ({_fmt_pct(price_change_pct)})"},
        {"label": "价格区间", "value": range_label},
        {
            "label": "振幅量能",
            "value": f"{_fmt_pct(amp_pct)} , {vol_factor:.2f}x" if vol_factor is not None else f"{_fmt_pct(amp_pct)} , -",
        },
        {"label": "今日波动", "value": swing_label},
        {"label": "摸顶探底", "value": touch_label},
        {"label": "昨日涨幅", "value": _fmt_pct(yesterday_ret)},
        {"label": "上周涨幅", "value": _fmt_pct(last_week_ret)},
        {"label": "本周涨幅", "value": _fmt_pct(week_ret)},
        {"label": "六日趋势", "value": "-" if score_6 is None else f"{score_6}分({label_6})"},
        {"label": "60日趋势", "value": "-" if score_60 is None else f"{score_60}分({label_60})"},
        {
            "label": "持仓数量",
            "value": "-" if open_interest is None else f"{_fmt_cn_amount(open_interest, '币')} ({_fmt_pct(oi_change_pct, 1)})",
        },
        {
            "label": "持仓价值",
            "value": "-" if oi_notional is None else f"{_fmt_cn_amount(oi_notional, 'U')} ({_fmt_pct(oi_value_change_pct, 1)})",
        },
        {"label": "头寸规模", "value": _fmt_pct(position_ratio, 1) if position_ratio is not None else "-"},
        {"label": "资金费率", "value": _fmt_pct(_to_float(fund.last_funding_rate) if fund else None, 4)},
        {"label": "多头指数", "value": "-" if long_index is None else f"{long_index:.1f}分"},
        {
            "label": "流通市值",
            "value": "-" if market_cap is None else f"{_fmt_cn_amount(market_cap, '')}{circ_pct_label}",
        },
        {"label": "稀释市值", "value": "-" if diluted_cap is None else _fmt_cn_amount(diluted_cap, "")},
    ]

    return {
        "basic": basic,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "symbolStatus": status,
        "timeMode": time_mode,
        "tzOffsetMin": tz_offset_min,
        "funding": None
        if not fund
        else {
            "lastFundingRate": _to_float(fund.last_funding_rate),
            "markPrice": _to_float(fund.mark_price),
            "timeMs": int(fund.event_time_ms),
        },
        "openInterest": None
        if not oi
        else {
            "openInterest": _to_float(oi.open_interest),
            "markPrice": _to_float(oi.mark_price),
            "oiNotionalUsd": _to_float(oi.oi_notional_usd),
            "timeMs": int(oi.event_time_ms),
        },
        "marketCap": None
        if not cap
        else {
            "priceUsd": _to_float(cap.price_usd),
            "circulatingSupply": _to_float(cap.circulating_supply),
            "marketCapUsd": _to_float(cap.market_cap_usd),
            "source": cap.source,
            "timeMs": int(cap.event_time_ms),
        },
        "srLevels": [
            {
                "levelPrice": float(r.level_price),
                "touches": int(r.touches),
                "strengthScore": float(r.strength_score),
                "lastTouchMs": int(r.last_touch_ms),
            }
            for r in levels
        ],
        "basicKvp": basic_kvp,
    }


@router.get("/coin/detail/flows/hourly")
async def coin_hourly_flow(
    symbol: str = Query(..., min_length=3, max_length=32),
    hours: int = Query(24, ge=6, le=168),
) -> dict:
    sym = symbol.strip().upper()
    now_ms = int(time.time() * 1000)
    b = 60 * 60 * 1000
    minute_ms = 60 * 1000
    cur = (now_ms // b) * b
    last_closed = cur - b
    start_ms = last_closed - hours * b
    live_cutoff_ms = (now_ms // minute_ms) * minute_ms
    include_live_partial = (live_cutoff_ms - cur) >= minute_ms
    live_rows: list[TradeBucketRow] = []

    rows = await query_trade_buckets(symbol=sym, bucket="1h", start_ms=start_ms, end_ms=last_closed)
    if include_live_partial:
        live_rows = await query_trade_buckets(symbol=sym, bucket="1m", start_ms=cur, end_ms=live_cutoff_ms - 1)

    by_key: dict[tuple[str, int], TradeBucketRow] = {(r.market, int(r.bucket_start_ms)): r for r in rows}
    timeline = list(range(start_ms, last_closed + 1, b))
    out = []
    for ts in timeline:
        spot = by_key.get(("spot", ts))
        swap = by_key.get(("swap", ts))
        out.append(
            {
                "bucketStartMs": ts,
                "spotNetNotional": _to_float((spot.taker_buy_notional if spot else 0) - (spot.taker_sell_notional if spot else 0)),
                "swapNetNotional": _to_float((swap.taker_buy_notional if swap else 0) - (swap.taker_sell_notional if swap else 0)),
                "spotQuoteNotional": _to_float(spot.quote_notional if spot else 0),
                "swapQuoteNotional": _to_float(swap.quote_notional if swap else 0),
            }
        )

    if include_live_partial:
        spot_net = 0.0
        spot_quote = 0.0
        swap_net = 0.0
        swap_quote = 0.0
        for row in live_rows:
            buy = _to_float(row.taker_buy_notional) or 0.0
            sell = _to_float(row.taker_sell_notional) or 0.0
            quote = _to_float(row.quote_notional) or 0.0
            net = buy - sell
            if row.market == "spot":
                spot_net += net
                spot_quote += quote
            elif row.market == "swap":
                swap_net += net
                swap_quote += quote
        out.append(
            {
                "bucketStartMs": now_ms,
                "spotNetNotional": spot_net,
                "swapNetNotional": swap_net,
                "spotQuoteNotional": spot_quote,
                "swapQuoteNotional": swap_quote,
                "livePartial": True,
            }
        )

    return {"symbol": sym, "hours": hours, "items": out}


@router.get("/coin/detail/flows/daily")
async def coin_daily_flow(
    symbol: str = Query(..., min_length=3, max_length=32),
    days: int = Query(30, ge=7, le=365),
    include_today: bool = Query(False, alias="includeToday"),
) -> dict:
    sym = symbol.strip().upper()
    now_ms = int(time.time() * 1000)
    b = 24 * 60 * 60 * 1000
    cur = (now_ms // b) * b
    last_closed = cur - b
    start_ms = last_closed - (days - 1) * b

    rows = await query_trade_buckets(symbol=sym, bucket="1d", start_ms=start_ms, end_ms=last_closed)

    by_key: dict[tuple[str, int], TradeBucketRow] = {(r.market, int(r.bucket_start_ms)): r for r in rows}
    timeline = list(range(start_ms, last_closed + 1, b))
    out = []
    for ts in timeline:
        spot = by_key.get(("spot", ts))
        swap = by_key.get(("swap", ts))
        out.append(
            {
                "bucketStartMs": ts,
                "spotNetNotional": _to_float((spot.taker_buy_notional if spot else 0) - (spot.taker_sell_notional if spot else 0)),
                "swapNetNotional": _to_float((swap.taker_buy_notional if swap else 0) - (swap.taker_sell_notional if swap else 0)),
                "spotQuoteNotional": _to_float(spot.quote_notional if spot else 0),
                "swapQuoteNotional": _to_float(swap.quote_notional if swap else 0),
            }
        )

    if include_today:
        day_start_ms = cur
        spot_task = get_klines_range("spot", sym, "1m", day_start_ms, now_ms)
        swap_task = get_klines_range("swap", sym, "1m", day_start_ms, now_ms)
        spot_res, swap_res = await asyncio.gather(spot_task, swap_task, return_exceptions=True)
        spot_klines = [] if isinstance(spot_res, Exception) else spot_res
        swap_klines = [] if isinstance(swap_res, Exception) else swap_res

        def _sum_net(klines: list[list]) -> tuple[float, float]:
            net = 0.0
            quote = 0.0
            for k in klines:
                try:
                    qv = float(k[7])
                    tbq = float(k[10])
                except (TypeError, ValueError, IndexError):
                    continue
                quote += qv
                net += (2 * tbq - qv)
            return net, quote

        spot_net, spot_quote = _sum_net(spot_klines)
        swap_net, swap_quote = _sum_net(swap_klines)
        if spot_klines or swap_klines:
            out.append(
                {
                    "bucketStartMs": day_start_ms,
                    "spotNetNotional": spot_net,
                    "swapNetNotional": swap_net,
                    "spotQuoteNotional": spot_quote,
                    "swapQuoteNotional": swap_quote,
                    "source": "binance_klines_1m",
                }
            )

    return {"symbol": sym, "days": days, "items": out}


@router.get("/coin/detail/oi/hourly")
async def coin_oi_hourly(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    hours: int = Query(24, ge=6, le=168),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    if effective_market != "swap":
        return {"symbol": sym, "hours": hours, "items": []}

    rows = await get_open_interest_hist(sym, period="1h", limit=int(hours))
    items = []
    ts_list: list[int] = []
    for r in rows:
        try:
            ts = int(r.get("timestamp") or 0)
            oi_raw = r.get("openInterest")
            if oi_raw is None:
                oi_raw = r.get("sumOpenInterest")
            oi = float(oi_raw)
            oi_value = float(r.get("sumOpenInterestValue"))
        except (TypeError, ValueError):
            continue
        items.append(
            {
                "bucketStartMs": ts,
                "openInterestUsd": oi_value,
                "openInterest": oi,
            }
        )
        ts_list.append(ts)

    if items and ts_list:
        start_ms = min(ts_list)
        end_ms = max(ts_list)

        price_rows_1m = await query_trade_buckets(market="swap", symbol=sym, bucket="1m", start_ms=start_ms, end_ms=end_ms)
        price_rows_1h = await query_trade_buckets(market="swap", symbol=sym, bucket="1h", start_ms=start_ms, end_ms=end_ms)

        price_map_1m = {int(r.bucket_start_ms): _to_float(r.close_price) for r in price_rows_1m}
        price_map_1h = {int(r.bucket_start_ms): _to_float(r.close_price) for r in price_rows_1h}

        for item in items:
            ts = int(item["bucketStartMs"])
            close_price = price_map_1m.get(ts)
            if close_price is None:
                close_price = price_map_1h.get(_floor_bucket_start_ms(ts, "1h"))
            item["closePrice"] = close_price
    items.sort(key=lambda x: x["bucketStartMs"])
    return {
        "symbol": sym,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "hours": hours,
        "items": items,
    }


@router.get("/coin/detail/oi/daily")
async def coin_oi_daily(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    days: int = Query(30, ge=7, le=120),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    if effective_market != "swap":
        return {"symbol": sym, "days": days, "items": []}

    rows = await get_open_interest_hist(sym, period="1d", limit=int(days))
    items = []
    ts_list: list[int] = []
    for r in rows:
        try:
            ts = int(r.get("timestamp") or 0)
            oi_raw = r.get("openInterest")
            if oi_raw is None:
                oi_raw = r.get("sumOpenInterest")
            oi = float(oi_raw)
            oi_value = float(r.get("sumOpenInterestValue"))
        except (TypeError, ValueError):
            continue
        items.append(
            {
                "bucketStartMs": ts,
                "openInterestUsd": oi_value,
                "openInterest": oi,
            }
        )
        ts_list.append(ts)

    if items and ts_list:
        start_ms = min(ts_list)
        end_ms = max(ts_list)

        price_rows_1d = await query_trade_buckets(market="swap", symbol=sym, bucket="1d", start_ms=start_ms, end_ms=end_ms)

        price_map_1d = {int(r.bucket_start_ms): _to_float(r.close_price) for r in price_rows_1d}
        for item in items:
            ts = int(item["bucketStartMs"])
            close_price = price_map_1d.get(_floor_bucket_start_ms(ts, "1d"))
            item["closePrice"] = close_price

    items.sort(key=lambda x: x["bucketStartMs"])
    return {
        "symbol": sym,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "days": days,
        "items": items,
    }


@router.get("/coin/detail/lsr/hourly")
async def coin_lsr_hourly(
    symbol: str = Query(..., min_length=3, max_length=32),
    limit: int = Query(6, ge=3, le=48),
) -> dict:
    sym = symbol.strip().upper()
    period = "1h"
    g_task = get_global_long_short_account_ratio(sym, period=period, limit=int(limit))
    t_task = get_top_long_short_account_ratio(sym, period=period, limit=int(limit))
    p_task = get_top_long_short_position_ratio(sym, period=period, limit=int(limit))
    g_rows, t_rows, p_rows = await asyncio.gather(g_task, t_task, p_task)

    def _ratio_map(rows: list[dict]) -> dict[int, float]:
        out: dict[int, float] = {}
        for r in rows:
            try:
                ts = int(r.get("timestamp") or 0)
                ratio = float(r.get("longShortRatio"))
            except (TypeError, ValueError):
                continue
            out[ts] = ratio
        return out

    g_map = _ratio_map(g_rows)
    t_map = _ratio_map(t_rows)
    p_map = _ratio_map(p_rows)

    timestamps = sorted(set(g_map) & set(t_map) & set(p_map))
    items = [
        {
            "bucketStartMs": ts,
            "accountRatio": g_map[ts],
            "topAccountRatio": t_map[ts],
            "topPositionRatio": p_map[ts],
        }
        for ts in timestamps
    ]
    return {"symbol": sym, "period": period, "items": items}


@router.get("/coin/detail/sr/short")
async def coin_sr_short(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    days: int = Query(5, ge=3, le=15),
    limit: int = Query(5, ge=1, le=10),
    timeframe: str = Query("1h", pattern="^(1h|15m)$"),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    now_ms = int(time.time() * 1000)
    lookback_ms = days * 24 * 60 * 60 * 1000
    start_ms = now_ms - lookback_ms
    bucket = "15m" if timeframe == "15m" else "1h"

    rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket=bucket, start_ms=start_ms)

    candles: list[tuple[int, float, float, float, float]] = []
    for r in rows:
        o = _to_float(r.open_price)
        h = _to_float(r.high_price)
        l = _to_float(r.low_price)
        c = _to_float(r.close_price)
        if o is None or h is None or l is None or c is None:
            continue
        candles.append((int(r.bucket_start_ms), o, h, l, c))

    if len(candles) < 10:
        return {
            "symbol": sym,
            "market": effective_market,
            "requestedMarket": market,
            "effectiveMarket": effective_market,
            "marketFallback": market_fallback,
            "supports": [],
            "resistances": [],
        }

    min_bars = 3
    if timeframe == "1h":
        reversal_pct = 0.012
    else:
        reversal_pct = 0.008
    pivots = _zigzag_pivots(candles, reversal_pct, min_bars)

    if timeframe == "1h":
        cluster_pct = 0.005
        min_band_pct = 0.003
    else:
        cluster_pct = 0.003
        min_band_pct = 0.0015
    ranges = _cluster_ranges(pivots, cluster_pct, min_band_pct)
    if not ranges:
        return {
            "symbol": sym,
            "market": effective_market,
            "requestedMarket": market,
            "effectiveMarket": effective_market,
            "marketFallback": market_fallback,
            "supports": [],
            "resistances": [],
        }

    last_close = candles[-1][4]
    supports: list[tuple[float, float, float]] = []
    resistances: list[tuple[float, float, float]] = []
    for low, high, mid in ranges:
        if mid <= last_close:
            supports.append((low, high, mid))
        else:
            resistances.append((low, high, mid))

    def _sort_key(item: tuple[float, float, float]) -> float:
        return abs(item[2] - last_close)

    supports = sorted(supports, key=_sort_key)[: int(limit)]
    resistances = sorted(resistances, key=_sort_key)[: int(limit)]

    return {
        "symbol": sym,
        "market": effective_market,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "timeframe": timeframe,
        "supports": [{"low": low, "high": high} for low, high, _ in supports],
        "resistances": [{"low": low, "high": high} for low, high, _ in resistances],
    }


@router.get("/coin/detail/fund/snapshots")
async def coin_fund_snapshots(
    symbol: str = Query(..., min_length=3, max_length=32),
    tz_offset_min: int = Query(0, alias="tzOffsetMin"),
    time_mode: TimeMode = Query("utc", alias="timeMode", pattern="^(utc|local)$"),
) -> dict:
    sym = symbol.strip().upper()
    tz = ZoneInfo("UTC")
    now = datetime.now(tz)
    now_utc_ms = int(now.timestamp() * 1000)

    _, _, start_ms = _build_snapshot_targets(now_utc_ms, tz_offset_min, time_mode)
    if now_utc_ms < start_ms:
        return {
            "symbol": sym,
            "timezone": "UTC" if time_mode == "utc" else "LOCAL",
            "timeMode": time_mode,
            "tzOffsetMin": tz_offset_min,
            "source": "trade_buckets",
            "items": [],
        }

    offset_ms = int(tz_offset_min) * 60 * 1000 if time_mode == "local" else 0
    hour_ms = 60 * 60 * 1000
    current_hour_start_ms = _floor_bucket_start_with_offset_ms(now_utc_ms, hour_ms, offset_ms)
    closed_cutoffs = list(range(start_ms + hour_ms, current_hour_start_ms + 1, hour_ms))

    async def _load_trade_rows() -> tuple[str, list[Any], dict[str, list[Any]]]:
        bucket_candidates = ["1m", "15m", "1h"]
        used_bucket = bucket_candidates[-1]
        rows: list[Any] = []
        rows_by_bucket: dict[str, list[Any]] = {}

        candidates: list[tuple[bool, int, int, str, list[Any]]] = []
        for bucket in bucket_candidates:
            bucket_rows = await query_trade_buckets(symbol=sym, bucket=bucket, start_ms=start_ms, end_ms=now_utc_ms)
            rows_by_bucket[bucket] = bucket_rows
            if not bucket_rows:
                continue
            bms = _bucket_ms(bucket)
            first_ms = int(bucket_rows[0].bucket_start_ms)
            covered = first_ms <= start_ms + bms
            gap_ms = max(0, first_ms - start_ms)
            candidates.append((covered, gap_ms, bms, bucket, bucket_rows))

        if candidates:
            covered_candidates = [item for item in candidates if item[0]]
            if covered_candidates:
                covered_candidates.sort(key=lambda item: (item[2], item[1]))
                chosen = covered_candidates[0]
            else:
                candidates.sort(key=lambda item: (item[1], item[2]))
                chosen = candidates[0]
            used_bucket = chosen[3]
            rows = chosen[4]

        return used_bucket, rows, rows_by_bucket

    def _build_trade_response(used_bucket: str, rows: list[TradeBucketRow], source_suffix: str | None = None) -> dict:
        by_market: dict[str, list[tuple[int, float]]] = {"spot": [], "swap": []}
        for row in rows:
            net = (row.taker_buy_notional or Decimal("0")) - (row.taker_sell_notional or Decimal("0"))
            by_market[row.market].append((int(row.bucket_start_ms), float(net)))

        def _cum_at(entries: list[tuple[int, float]], cutoffs: list[int]) -> list[float]:
            if not cutoffs:
                return []
            if not entries:
                return [0.0 for _ in cutoffs]
            entries.sort(key=lambda item: item[0])
            out: list[float] = []
            index = 0
            total = 0.0
            for cutoff in cutoffs:
                while index < len(entries) and entries[index][0] < cutoff:
                    total += entries[index][1]
                    index += 1
                out.append(total)
            return out

        swap_closed_vals = _cum_at(by_market.get("swap", []), closed_cutoffs)
        spot_closed_vals = _cum_at(by_market.get("spot", []), closed_cutoffs)
        swap_now_val = _cum_at(by_market.get("swap", []), [now_utc_ms + 1])[0]
        spot_now_val = _cum_at(by_market.get("spot", []), [now_utc_ms + 1])[0]

        items = []
        for idx, ts in enumerate(closed_cutoffs):
            items.append(
                {
                    "key": idx + 1,
                    "labelTsMs": ts,
                    "swapValue": swap_closed_vals[idx] if idx < len(swap_closed_vals) else 0,
                    "spotValue": spot_closed_vals[idx] if idx < len(spot_closed_vals) else 0,
                }
            )

        if now_utc_ms - current_hour_start_ms >= 60 * 1000:
            items.append(
                {
                    "key": len(items) + 1,
                    "labelTsMs": now_utc_ms,
                    "swapValue": swap_now_val,
                    "spotValue": spot_now_val,
                }
            )

        source = f"trade_buckets_{used_bucket}"
        if source_suffix:
            source = f"{source}_{source_suffix}"

        return {
            "symbol": sym,
            "timezone": "UTC" if time_mode == "utc" else "LOCAL",
            "timeMode": time_mode,
            "tzOffsetMin": tz_offset_min,
            "source": source,
            "items": items,
        }

    def _assess_trade_bucket_health(rows_by_bucket: dict[str, list[TradeBucketRow]]) -> tuple[bool, str]:
        rows_1m = rows_by_bucket.get("1m") or []
        if not rows_1m:
            return False, "no_1m_rows"

        latest_1m_by_market: dict[str, int] = {}
        for row in rows_1m:
            ts = int(row.bucket_start_ms)
            prev = latest_1m_by_market.get(row.market)
            if prev is None or ts > prev:
                latest_1m_by_market[row.market] = ts

        fresh_window_ms = 10 * 60 * 1000
        stale_markets = [
            market
            for market, ts in latest_1m_by_market.items()
            if now_utc_ms - int(ts) > fresh_window_ms
        ]
        if stale_markets:
            return False, f"stale_1m_{'_'.join(sorted(stale_markets))}"

        rows_1h = rows_by_bucket.get("1h") or []
        recent_start = current_hour_start_ms - 2 * hour_ms

        by_hour_1m: dict[tuple[str, int], float] = {}
        for row in rows_1m:
            ts = int(row.bucket_start_ms)
            if ts < recent_start or ts >= current_hour_start_ms:
                continue
            hour_start = (ts // hour_ms) * hour_ms
            net = float((row.taker_buy_notional or Decimal("0")) - (row.taker_sell_notional or Decimal("0")))
            key = (row.market, hour_start)
            by_hour_1m[key] = by_hour_1m.get(key, 0.0) + net

        by_hour_1h: dict[tuple[str, int], float] = {}
        for row in rows_1h:
            ts = int(row.bucket_start_ms)
            if ts < recent_start or ts >= current_hour_start_ms:
                continue
            net = float((row.taker_buy_notional or Decimal("0")) - (row.taker_sell_notional or Decimal("0")))
            by_hour_1h[(row.market, ts)] = net

        compared = 0
        mismatch = 0
        for key, hour_val in by_hour_1h.items():
            minute_val = by_hour_1m.get(key)
            if minute_val is None:
                continue
            compared += 1
            if abs(hour_val - minute_val) > 0.01:
                mismatch += 1

        if compared > 0 and mismatch > 0:
            return False, f"h1_m1_mismatch_{mismatch}_{compared}"

        return True, "ok"

    used_bucket, rows, rows_by_bucket = await _load_trade_rows()
    trade_resp = _build_trade_response(used_bucket, rows)
    trade_ok, trade_reason = _assess_trade_bucket_health(rows_by_bucket)
    if trade_ok and trade_resp["items"]:
        return trade_resp

    # 不健康时：回补 K 线到 trade_buckets，再次读取 trade_buckets 返回
    repair_key = f"{sym}:{time_mode}:{tz_offset_min}"
    last_repair_ts = _FUND_SNAPSHOT_REPAIR_TS_MS.get(repair_key, 0)
    cooldown_ms = 90 * 1000
    if now_utc_ms - last_repair_ts > cooldown_ms:
        _FUND_SNAPSHOT_REPAIR_TS_MS[repair_key] = now_utc_ms
        # stale 时按“当前统计窗口”动态回补，避免仅补最近 240 分钟导致 UTC 当日前半段缺口。
        # Binance K 线单次 limit 上限约为 1500，做一个保守上限。
        window_minutes = max(1, int((now_utc_ms - start_ms + 60_000 - 1) // 60_000))
        repair_1m_limit = min(1500, max(240, window_minutes + 5))
        repair_intervals = [("1m", repair_1m_limit), ("15m", 192), ("1h", 96)]
        backfill_concurrency = max(1, min(2, int(settings.backfill_concurrency)))
        backfill_batch = max(100, int(settings.ingest_db_batch_size))

        tasks = []
        for market in ["spot", "swap"]:
            for interval, limit in repair_intervals:
                tasks.append(
                    backfill_trade_buckets_from_klines(
                        market=market,
                        symbols=[sym],
                        interval=interval,
                        limit=limit,
                        concurrency=backfill_concurrency,
                        db_batch_size=backfill_batch,
                    )
                )
        await asyncio.gather(*tasks, return_exceptions=True)

        used_bucket, rows, rows_by_bucket = await _load_trade_rows()
        trade_resp = _build_trade_response(used_bucket, rows, source_suffix=f"repaired_{trade_reason}")
        trade_ok, trade_reason = _assess_trade_bucket_health(rows_by_bucket)
        if trade_ok:
            return trade_resp
        trade_resp["source"] = f"{trade_resp['source']}_still_unhealthy_{trade_reason}"
        return trade_resp

    trade_resp["source"] = f"{trade_resp['source']}_unhealthy_{trade_reason}_repair_cooldown"
    return trade_resp


@router.get("/coin/detail/fund/snapshot-health")
async def coin_fund_snapshot_health(
    symbol: str = Query(..., min_length=3, max_length=32),
    tz_offset_min: int = Query(0, alias="tzOffsetMin"),
    time_mode: TimeMode = Query("utc", alias="timeMode", pattern="^(utc|local)$"),
) -> dict:
    sym = symbol.strip().upper()
    now_utc_ms = int(datetime.now(ZoneInfo("UTC")).timestamp() * 1000)
    _, _, start_ms = _build_snapshot_targets(now_utc_ms, tz_offset_min, time_mode)

    hour_ms = 60 * 60 * 1000
    recent_start_ms = max(0, start_ms - 2 * hour_ms)
    offset_ms = int(tz_offset_min) * 60 * 1000 if time_mode == "local" else 0
    current_hour_start_ms = _floor_bucket_start_with_offset_ms(now_utc_ms, hour_ms, offset_ms)

    rows_1m = await query_trade_buckets(symbol=sym, bucket="1m", start_ms=recent_start_ms, end_ms=now_utc_ms)
    rows_1h = await query_trade_buckets(symbol=sym, bucket="1h", start_ms=current_hour_start_ms - 2 * hour_ms, end_ms=current_hour_start_ms - 1)

    latest_1m_by_market: dict[str, int] = {}
    for row in rows_1m:
        ts = int(row.bucket_start_ms)
        prev = latest_1m_by_market.get(row.market)
        if prev is None or ts > prev:
            latest_1m_by_market[row.market] = ts

    fresh_window_ms = 10 * 60 * 1000
    stale_markets = [
        market
        for market, ts in latest_1m_by_market.items()
        if now_utc_ms - int(ts) > fresh_window_ms
    ]

    by_hour_1m: dict[tuple[str, int], float] = {}
    for row in rows_1m:
        ts = int(row.bucket_start_ms)
        if ts < current_hour_start_ms - 2 * hour_ms or ts >= current_hour_start_ms:
            continue
        hour_start = (ts // hour_ms) * hour_ms
        net = float((row.taker_buy_notional or Decimal("0")) - (row.taker_sell_notional or Decimal("0")))
        key = (row.market, hour_start)
        by_hour_1m[key] = by_hour_1m.get(key, 0.0) + net

    by_hour_1h: dict[tuple[str, int], float] = {}
    for row in rows_1h:
        ts = int(row.bucket_start_ms)
        net = float((row.taker_buy_notional or Decimal("0")) - (row.taker_sell_notional or Decimal("0")))
        by_hour_1h[(row.market, ts)] = net

    compared = 0
    mismatch = 0
    for key, hour_val in by_hour_1h.items():
        minute_val = by_hour_1m.get(key)
        if minute_val is None:
            continue
        compared += 1
        if abs(hour_val - minute_val) > 0.01:
            mismatch += 1

    no_1m_rows = not rows_1m
    healthy = (not no_1m_rows) and (not stale_markets) and (mismatch == 0)
    if no_1m_rows:
        reason = "no_1m_rows"
    elif stale_markets:
        reason = f"stale_1m_{'_'.join(sorted(stale_markets))}"
    elif mismatch > 0:
        reason = f"h1_m1_mismatch_{mismatch}_{compared}"
    else:
        reason = "ok"

    repair_key = f"{sym}:{time_mode}:{tz_offset_min}"
    cooldown_ms = 90 * 1000
    last_repair_at_ms = int(_FUND_SNAPSHOT_REPAIR_TS_MS.get(repair_key, 0) or 0)
    cooldown_remaining_ms = 0
    if last_repair_at_ms > 0:
        cooldown_remaining_ms = max(0, cooldown_ms - (now_utc_ms - last_repair_at_ms))

    return {
        "symbol": sym,
        "timezone": "UTC" if time_mode == "utc" else "LOCAL",
        "timeMode": time_mode,
        "tzOffsetMin": tz_offset_min,
        "healthy": healthy,
        "reason": reason,
        "latest1mByMarket": {
            "spot": latest_1m_by_market.get("spot"),
            "swap": latest_1m_by_market.get("swap"),
        },
        "freshWindowSec": fresh_window_ms // 1000,
        "checkWindowHours": 2,
        "h1m1Consistency": {
            "compared": compared,
            "mismatch": mismatch,
        },
        "lastRepairAtMs": last_repair_at_ms or None,
        "repairCooldownMs": cooldown_ms,
        "repairCooldownRemainingMs": cooldown_remaining_ms,
        "canTriggerRepair": cooldown_remaining_ms <= 0,
    }


@router.get("/coin/detail/fund/intraday")
async def coin_fund_intraday(
    symbol: str = Query(..., min_length=3, max_length=32),
    bucket: str = Query("1h", pattern="^(1m|5m|15m|1h)$"),
    limit: int = Query(60, ge=12, le=1440),
) -> dict:
    sym = symbol.strip().upper()
    now_ms = int(time.time() * 1000)
    day_ms = 24 * 60 * 60 * 1000
    day_start_ms = (now_ms // day_ms) * day_ms
    bucket_ms = _bucket_ms(bucket)
    last_bucket_start = _floor_bucket_start_ms(now_ms, bucket)
    if last_bucket_start < day_start_ms:
        return {
            "symbol": sym,
            "bucket": bucket,
            "dayStartMs": day_start_ms,
            "lastBucketMs": last_bucket_start,
            "spotAvailable": False,
            "swapAvailable": False,
            "source": "trade_buckets",
            "items": [],
        }

    total_count = int((last_bucket_start - day_start_ms) // bucket_ms) + 1
    start_index = max(0, total_count - limit)
    timeline = [day_start_ms + i * bucket_ms for i in range(start_index, total_count)]

    query_bucket = "1m" if bucket == "5m" else bucket
    rows = await query_trade_buckets(symbol=sym, bucket=query_bucket, start_ms=day_start_ms, end_ms=last_bucket_start)

    by_market: dict[str, dict[int, float]] = {"spot": {}, "swap": {}}
    for r in rows:
        net = (r.taker_buy_notional or Decimal("0")) - (r.taker_sell_notional or Decimal("0"))
        ts = int(r.bucket_start_ms)
        if bucket == "5m":
            ts = _floor_bucket_start_ms(ts, "5m")
        bucket_map = by_market.get(r.market)
        if bucket_map is None:
            bucket_map = {}
            by_market[r.market] = bucket_map
        bucket_map[ts] = bucket_map.get(ts, 0.0) + float(net)

    spot_available = bool(by_market.get("spot"))
    swap_available = bool(by_market.get("swap"))

    all_starts = [day_start_ms + i * bucket_ms for i in range(total_count)]

    def _cum_map(entries: dict[int, float]) -> dict[int, float]:
        total = 0.0
        out: dict[int, float] = {}
        for ts in all_starts:
            total += entries.get(ts, 0.0)
            out[ts] = total
        return out

    swap_cum = _cum_map(by_market.get("swap", {}))
    spot_cum = _cum_map(by_market.get("spot", {}))

    items = []
    for ts in timeline:
        items.append(
            {
                "bucketStartMs": ts,
                "swapValue": swap_cum.get(ts, 0.0),
                "spotValue": spot_cum.get(ts, 0.0),
                "swapDelta": by_market.get("swap", {}).get(ts, 0.0),
                "spotDelta": by_market.get("spot", {}).get(ts, 0.0),
            }
        )

    return {
        "symbol": sym,
        "bucket": bucket,
        "dayStartMs": day_start_ms,
        "lastBucketMs": last_bucket_start,
        "spotAvailable": spot_available,
        "swapAvailable": swap_available,
        "source": f"trade_buckets_{query_bucket}",
        "items": items,
    }


@router.get("/coin/detail/orderbook/intraday")
async def coin_orderbook_intraday(
    symbol: str = Query(..., min_length=3, max_length=32),
    bucket: str = Query("1m", pattern="^(1m)$"),
    limit: int = Query(60, ge=12, le=240),
) -> dict:
    sym = symbol.strip().upper()
    now_ms = int(time.time() * 1000)
    bucket_ms = _bucket_ms(bucket)
    last_bucket_start = _floor_bucket_start_ms(now_ms, bucket)
    if last_bucket_start <= 0:
        return {
            "symbol": sym,
            "bucket": bucket,
            "spotAvailable": False,
            "swapAvailable": False,
            "source": "orderbook_feature_buckets_1m",
            "items": [],
        }

    start_ms = max(0, last_bucket_start - (int(limit) - 1) * bucket_ms)

    rows = await query_orderbook_features(symbol=sym, bucket=bucket, start_ms=start_ms, end_ms=last_bucket_start)

    by_market: dict[str, dict[int, OBFeatureRow]] = {"spot": {}, "swap": {}}
    for row in rows:
        by_market.setdefault(row.market, {})[int(row.bucket_start_ms)] = row

    timeline = list(range(start_ms, last_bucket_start + 1, bucket_ms))
    items = []
    spot_available = False
    swap_available = False

    def _avg_or_none(sum_val, sample_count: int) -> float | None:
        if sample_count <= 0:
            return None
        val = _to_float(sum_val)
        if val is None:
            return None
        return val / sample_count

    for ts in timeline:
        spot = by_market.get("spot", {}).get(ts)
        swap = by_market.get("swap", {}).get(ts)
        if spot is not None:
            spot_available = True
        if swap is not None:
            swap_available = True

        def _item_of(row: OBFeatureRow | None) -> dict:
            if row is None:
                return {
                    "spreadBps": None,
                    "depthImbalanceL5": None,
                    "micropriceShiftBps": None,
                    "wallPressureL5": None,
                    "aggrBuyRatio": None,
                    "replenishScore": None,
                    "sampleCount": 0,
                }
            sample_count = int(row.sample_count or 0)
            buy = _to_float(row.taker_buy_notional) or 0.0
            sell = _to_float(row.taker_sell_notional) or 0.0
            denom = buy + sell
            aggr_buy_ratio = (buy / denom) if denom > 0 else None
            dep = int(row.depletion_events or 0)
            rep = int(row.replenishment_events or 0)
            replenish = 50.0 if dep <= 0 else max(0.0, min(100.0, rep / dep * 100.0))
            return {
                "spreadBps": _avg_or_none(row.spread_bps_sum, sample_count),
                "depthImbalanceL5": _avg_or_none(row.depth_imbalance_l5_sum, sample_count),
                "micropriceShiftBps": _avg_or_none(row.microprice_shift_bps_sum, sample_count),
                "wallPressureL5": _avg_or_none(row.wall_pressure_l5_sum, sample_count),
                "aggrBuyRatio": aggr_buy_ratio,
                "replenishScore": replenish,
                "sampleCount": sample_count,
            }

        items.append(
            {
                "bucketStartMs": ts,
                "swap": _item_of(swap),
                "spot": _item_of(spot),
            }
        )

    return {
        "symbol": sym,
        "bucket": bucket,
        "spotAvailable": spot_available,
        "swapAvailable": swap_available,
        "source": "orderbook_feature_buckets_1m",
        "items": items,
    }


@router.get("/coin/detail/orderbook/absorption-signal")
async def coin_orderbook_absorption_signal(
    symbol: str = Query(..., min_length=3, max_length=32),
    market: Market = Query("swap", pattern="^(spot|swap)$"),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    now_ms = int(time.time() * 1000)

    lookback_minutes = 3 * 24 * 60
    start_ms = max(0, now_ms - lookback_minutes * 60 * 1000)

    ob_rows = await query_orderbook_features(market=effective_market, symbol=sym, bucket="1m", start_ms=start_ms)
    trade_rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="1m", start_ms=start_ms)

    ob_map: dict[int, OBFeatureRow] = {int(r.bucket_start_ms): r for r in ob_rows}
    tr_map: dict[int, TradeBucketRow] = {int(r.bucket_start_ms): r for r in trade_rows}

    timeline = sorted(set(ob_map.keys()) | set(tr_map.keys()))
    if not timeline:
        return {
            "symbol": sym,
            "requestedMarket": market,
            "effectiveMarket": effective_market,
            "marketFallback": market_fallback,
            "signalState": "NONE",
            "direction": "LONG_BIAS",
            "score": 0,
            "cooldown": {"active": False, "secondsRemaining": 0},
            "windows": {
                "4h": {"passed": False},
                "1d": {"passed": False},
                "3d": {"passed": False},
            },
            "reasons": ["缺少盘口/成交数据"],
            "ts": now_ms,
            "source": "orderbook_feature_buckets_1m+trade_buckets_1m",
        }

    rows: list[dict[str, Any]] = []
    for ts in timeline:
        ob = ob_map.get(ts)
        tr = tr_map.get(ts)

        buy = _to_float(ob.taker_buy_notional if ob else (tr.taker_buy_notional if tr else 0)) or 0.0
        sell = _to_float(ob.taker_sell_notional if ob else (tr.taker_sell_notional if tr else 0)) or 0.0
        quote = _to_float(tr.quote_notional if tr else 0) or 0.0
        net = buy - sell
        denom = buy + sell
        aggr_buy_ratio = (buy / denom) if denom > 0 else None

        sample_count = int(ob.sample_count or 0) if ob else 0
        spread_bps = (_to_float(ob.spread_bps_sum) / sample_count) if (ob and sample_count > 0 and _to_float(ob.spread_bps_sum) is not None) else None
        depth_imbalance = (
            (_to_float(ob.depth_imbalance_l5_sum) / sample_count)
            if (ob and sample_count > 0 and _to_float(ob.depth_imbalance_l5_sum) is not None)
            else None
        )

        replenish_score: float | None = None
        if ob:
            dep = int(ob.depletion_events or 0)
            rep = int(ob.replenishment_events or 0)
            replenish_score = 50.0 if dep <= 0 else _clamp(rep / dep * 100.0, 0.0, 100.0)

        close_price = _to_float(tr.close_price) if tr else None

        rows.append(
            {
                "ts": ts,
                "netBuyNotional": net,
                "aggrBuyRatio": aggr_buy_ratio,
                "spreadBps": spread_bps,
                "depthImbalanceL5": depth_imbalance,
                "replenishScore": replenish_score,
                "closePrice": close_price,
                "quoteNotional": quote,
            }
        )

    for i in range(len(rows)):
        prev = rows[i - 1] if i > 0 else None
        cur_close = rows[i].get("closePrice")
        prev_close = prev.get("closePrice") if prev else None
        if (
            cur_close is not None
            and prev_close is not None
            and isinstance(cur_close, (int, float))
            and isinstance(prev_close, (int, float))
            and prev_close > 0
        ):
            rows[i]["ret1m"] = cur_close / prev_close - 1.0
        else:
            rows[i]["ret1m"] = 0.0

    spread_values = [float(r["spreadBps"]) for r in rows if r.get("spreadBps") is not None]

    def _window_eval(
        minutes: int,
        persistence_threshold: float = 0.60,
        impact_threshold_pct: float = 0.25,
        thin_threshold: float = 0.60,
        replenish_threshold: float = 55.0,
    ) -> dict[str, Any]:
        need = minutes
        win = rows[-need:] if len(rows) >= need else rows[:]
        if not win:
            return {
                "minutes": minutes,
                "passed": False,
                "buyPersistenceRatio": 0.0,
                "buyPersistenceThreshold": persistence_threshold,
                "netRetAbsPct": 0.0,
                "netRetAbsThresholdPct": impact_threshold_pct,
                "spreadThinRank": None,
                "thinThreshold": thin_threshold,
                "replenishAvg": None,
                "replenishThreshold": replenish_threshold,
            }

        buy_flags = [
            1
            for r in win
            if (r.get("aggrBuyRatio") is not None and float(r.get("aggrBuyRatio")) >= 0.58 and float(r.get("netBuyNotional") or 0.0) > 0)
        ]
        persistence_ratio = len(buy_flags) / len(win)

        net_ret_abs_pct = abs(sum(float(r.get("ret1m") or 0.0) for r in win)) * 100.0

        last_spread = next((r.get("spreadBps") for r in reversed(win) if r.get("spreadBps") is not None), None)
        spread_rank = None
        if last_spread is not None:
            spread_rank = _percentile_rank(spread_values, float(last_spread))

        replenish_values = [float(r["replenishScore"]) for r in win if r.get("replenishScore") is not None]
        replenish_avg = _safe_mean(replenish_values)

        thin_ok = (spread_rank is not None and spread_rank >= thin_threshold)
        replenish_ok = (replenish_avg is not None and replenish_avg >= replenish_threshold)

        passed = (
            persistence_ratio >= persistence_threshold
            and net_ret_abs_pct <= impact_threshold_pct
            and (thin_ok or replenish_ok)
        )

        return {
            "minutes": minutes,
            "passed": passed,
            "buyPersistenceRatio": round(persistence_ratio, 4),
            "buyPersistenceThreshold": persistence_threshold,
            "netRetAbsPct": round(net_ret_abs_pct, 4),
            "netRetAbsThresholdPct": impact_threshold_pct,
            "spreadThinRank": None if spread_rank is None else round(spread_rank, 4),
            "thinThreshold": thin_threshold,
            "replenishAvg": None if replenish_avg is None else round(replenish_avg, 2),
            "replenishThreshold": replenish_threshold,
        }

    w4h = _window_eval(240, persistence_threshold=0.60, impact_threshold_pct=0.35)
    w1d = _window_eval(1440, persistence_threshold=0.55, impact_threshold_pct=0.80)
    w3d = _window_eval(4320, persistence_threshold=0.50, impact_threshold_pct=1.60)

    scores: dict[str, float] = {}
    for key, w in (("4h", w4h), ("1d", w1d), ("3d", w3d)):
        persistence_th = max(1e-9, float(w.get("buyPersistenceThreshold") or 0.6))
        impact_th = max(1e-9, float(w.get("netRetAbsThresholdPct") or 0.25))
        thin_th = max(1e-9, float(w.get("thinThreshold") or 0.6))
        replenish_th = max(1e-9, float(w.get("replenishThreshold") or 55.0))
        persistence_score = _clamp((float(w["buyPersistenceRatio"]) / persistence_th) * 40.0, 0.0, 40.0)
        impact_score = _clamp(((impact_th - float(w["netRetAbsPct"])) / impact_th) * 35.0, 0.0, 35.0)
        thin_or_replenish = max(
            0.0 if w.get("spreadThinRank") is None else _clamp((float(w["spreadThinRank"]) / thin_th) * 25.0, 0.0, 25.0),
            0.0 if w.get("replenishAvg") is None else _clamp((float(w["replenishAvg"]) / replenish_th) * 25.0, 0.0, 25.0),
        )
        scores[key] = round(_clamp(persistence_score + impact_score + thin_or_replenish, 0.0, 100.0), 1)

    signal_state = "NONE"
    if w3d["passed"] and scores["3d"] >= 78:
        signal_state = "STRONG"
    elif w1d["passed"] and scores["1d"] >= 65:
        signal_state = "CONFIRM"
    elif w4h["passed"] and scores["4h"] >= 55:
        signal_state = "WATCH"

    score = 0.0
    continuation_reason: str | None = None
    if signal_state == "NONE":
        recent4h = rows[-240:] if rows else []
        recent12h = rows[-720:] if len(rows) >= 720 else rows
        flow4h = _safe_mean([float(r.get("netBuyNotional") or 0.0) for r in recent4h]) or 0.0
        flow12h = _safe_mean([float(r.get("netBuyNotional") or 0.0) for r in recent12h]) or 0.0
        persistence_1d = float(w1d.get("buyPersistenceRatio") or 0.0)
        continuation_ok = flow4h > 0 and flow12h > 0 and persistence_1d >= 0.50 and float(w1d.get("netRetAbsPct") or 99.0) <= 1.20
        if continuation_ok and max(scores["4h"], scores["1d"]) >= 52:
            signal_state = "WATCH"
            score = max(52.0, min(68.0, max(scores["4h"], scores["1d"])))
            continuation_reason = "FLOW_CONTINUATION_12H"

    if signal_state == "WATCH":
        score = max(score, scores["4h"])
    elif signal_state == "CONFIRM":
        score = max(scores["4h"], scores["1d"])
    elif signal_state == "STRONG":
        score = max(scores["4h"], scores["1d"], scores["3d"])

    latest_ts = int(rows[-1]["ts"])
    cooldown_active = False
    cooldown_remaining_sec = 0
    state_key = f"{effective_market}:{sym}:LONG_BIAS"
    prev = _ABSORPTION_SIGNAL_COOLDOWN_STATE.get(state_key)
    if prev:
        prev_level = str(prev.get("signalState") or "NONE")
        prev_ts = int(prev.get("ts") or 0)
        cooldown_ms = _SIGNAL_COOLDOWN_MS.get(prev_level, 0)
        if cooldown_ms > 0 and _SIGNAL_LEVEL_RANK.get(signal_state, 0) <= _SIGNAL_LEVEL_RANK.get(prev_level, 0):
            remaining = prev_ts + cooldown_ms - latest_ts
            if remaining > 0:
                cooldown_active = True
                cooldown_remaining_sec = int(remaining // 1000)
                signal_state = "NONE"

    if signal_state != "NONE":
        _ABSORPTION_SIGNAL_COOLDOWN_STATE[state_key] = {
            "signalState": signal_state,
            "ts": latest_ts,
        }

    reasons: list[str] = []
    if w4h["passed"]:
        reasons.append("4h: 持续主动买入且低冲击")
    if w1d["passed"]:
        reasons.append("1d: 吸收结构确认")
    if w3d["passed"]:
        reasons.append("3d: 结构性吸筹延续")
    if continuation_reason:
        reasons.append("FLOW_CONTINUATION_12H")
    if not reasons:
        reasons.append("未满足吸收信号阈值")

    return {
        "symbol": sym,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "signalState": signal_state,
        "direction": "LONG_BIAS",
        "score": int(round(score)),
        "cooldown": {
            "active": cooldown_active,
            "secondsRemaining": cooldown_remaining_sec,
        },
        "windows": {
            "4h": {**w4h, "score": scores["4h"]},
            "1d": {**w1d, "score": scores["1d"]},
            "3d": {**w3d, "score": scores["3d"]},
        },
        "reasons": reasons,
        "ts": latest_ts,
        "source": "orderbook_feature_buckets_1m+trade_buckets_1m",
    }


@router.get("/coin/detail/orderbook/institutional-levels")
async def coin_orderbook_institutional_levels(
    symbol: str = Query(..., min_length=3, max_length=32),
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    lookbackMinutes: int = Query(24 * 60, ge=30, le=7 * 24 * 60),
    topK: int = Query(3, ge=1, le=10),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)

    payload = await get_symbol_latest_institutional_levels(
        market=effective_market,
        symbol=sym,
        lookback_minutes=lookbackMinutes,
        top_k=topK,
    )

    payload.update(
        {
            "symbol": sym,
            "requestedMarket": market,
            "effectiveMarket": effective_market,
            "marketFallback": market_fallback,
            "source": "orderbook_real_levels_1m",
        }
    )
    return payload

@router.get("/coin/detail/recent")
async def coin_recent(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    limit: int = Query(200, ge=50, le=1500),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="15m", start_ms=0, order="desc", limit=limit)

    items = []
    for r in reversed(rows):
        net = (r.taker_buy_notional or Decimal("0")) - (r.taker_sell_notional or Decimal("0"))
        items.append(
            {
                "bucketStartMs": int(r.bucket_start_ms),
                "open": _to_float(r.open_price),
                "high": _to_float(r.high_price),
                "low": _to_float(r.low_price),
                "close": _to_float(r.close_price),
                "quoteNotional": _to_float(r.quote_notional),
                "tradeCount": int(r.trade_count),
                "netNotional": _to_float(net),
            }
        )

    return {
        "market": effective_market,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "symbol": sym,
        "items": items,
    }


@router.get("/coin/detail/recent/daily")
async def coin_recent_daily(
    market: Market = Query("swap", pattern="^(spot|swap)$"),
    symbol: str = Query(..., min_length=3, max_length=32),
    days: int = Query(20, ge=10, le=90),
    include_today: bool = Query(False, alias="includeToday"),
) -> dict:
    sym = symbol.strip().upper()
    effective_market, market_fallback = await _resolve_effective_market_for_symbol(market, sym)
    rows = await query_trade_buckets(market=effective_market, symbol=sym, bucket="1d", start_ms=0, order="desc", limit=days)

    items = []
    for r in reversed(rows):
        items.append(
            {
                "bucketStartMs": int(r.bucket_start_ms),
                "open": _to_float(r.open_price),
                "high": _to_float(r.high_price),
                "low": _to_float(r.low_price),
                "close": _to_float(r.close_price),
            }
        )

    if include_today:
        now_ms = int(time.time() * 1000)
        day_start_ms = (now_ms // (24 * 60 * 60 * 1000)) * (24 * 60 * 60 * 1000)
        if not items or items[-1]["bucketStartMs"] < day_start_ms:
            klines = await get_klines_range(effective_market, sym, "1m", day_start_ms, now_ms)
            if klines:
                open_price = float(klines[0][1])
                close_price = float(klines[-1][4])
                high_price = max(float(k[2]) for k in klines)
                low_price = min(float(k[3]) for k in klines)
                items.append(
                    {
                        "bucketStartMs": day_start_ms,
                        "open": open_price,
                        "high": high_price,
                        "low": low_price,
                        "close": close_price,
                    }
                )

    return {
        "market": effective_market,
        "requestedMarket": market,
        "effectiveMarket": effective_market,
        "marketFallback": market_fallback,
        "symbol": sym,
        "items": items,
    }
