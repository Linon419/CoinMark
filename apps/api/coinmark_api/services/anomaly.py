from __future__ import annotations

import math
import time
from dataclasses import dataclass
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from typing import Any, Iterable

from sqlalchemy import and_, select, func

from coinmark_api.ch import TradeBucketRow, query_trade_buckets
from coinmark_api.db import SessionLocal
from coinmark_api.db_upsert import insert
from coinmark_api.models import AnomalyEvent, SRLevel
from coinmark_api.services.symbol_filter import filter_excluded_symbols, is_excluded_symbol


def _to_decimal(v: Any) -> Decimal | None:
    if v is None:
        return None
    if isinstance(v, Decimal):
        return v
    try:
        return Decimal(str(v))
    except (InvalidOperation, TypeError):
        return None


def _bucket_ms(bucket: str) -> int:
    if bucket == "15m":
        return 15 * 60 * 1000
    if bucket == "4h":
        return 4 * 60 * 60 * 1000
    raise ValueError(f"不支持的 bucket: {bucket}")


def _quantize_level_price(p: Decimal) -> Decimal:
    ap = abs(p)
    if ap == 0:
        return p
    if ap < Decimal("0.01"):
        step = Decimal("0.00000001")
    elif ap < Decimal("1"):
        step = Decimal("0.000001")
    elif ap < Decimal("10"):
        step = Decimal("0.0001")
    elif ap < Decimal("100"):
        step = Decimal("0.001")
    elif ap < Decimal("1000"):
        step = Decimal("0.01")
    elif ap < Decimal("10000"):
        step = Decimal("0.1")
    else:
        step = Decimal("1")
    return p.quantize(step, rounding=ROUND_HALF_UP)


@dataclass(frozen=True, slots=True)
class Candle:
    start_ms: int
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    quote_notional: Decimal


def _find_pivots(candles: list[Candle], reversal_pct: Decimal, min_bars: int) -> list[tuple[int, Decimal]]:
    """
    ZigZag pivot 列表：(start_ms, pivot_price)
    - reversal_pct: 反转阈值（百分比）
    - min_bars: 相邻 pivot 的最小间隔
    """
    if len(candles) < 2:
        return []

    rev = abs(reversal_pct)
    one = Decimal("1")
    pivots: list[tuple[int, Decimal]] = []

    trend = 0  # 0=unknown, 1=up, -1=down
    high = candles[0].high
    low = candles[0].low
    high_idx = 0
    low_idx = 0

    for i in range(1, len(candles)):
        c = candles[i]

        if trend == 0:
            if c.high >= high:
                high = c.high
                high_idx = i
            if c.low <= low:
                low = c.low
                low_idx = i
            if c.high >= low * (one + rev) and i - low_idx >= min_bars:
                pivots.append((candles[low_idx].start_ms, low))
                trend = 1
                high = c.high
                high_idx = i
            elif c.low <= high * (one - rev) and i - high_idx >= min_bars:
                pivots.append((candles[high_idx].start_ms, high))
                trend = -1
                low = c.low
                low_idx = i
            continue

        if trend == 1:
            if c.high >= high:
                high = c.high
                high_idx = i
            if c.low <= high * (one - rev) and i - high_idx >= min_bars:
                pivots.append((candles[high_idx].start_ms, high))
                trend = -1
                low = c.low
                low_idx = i
            continue

        if trend == -1:
            if c.low <= low:
                low = c.low
                low_idx = i
            if c.high >= low * (one + rev) and i - low_idx >= min_bars:
                pivots.append((candles[low_idx].start_ms, low))
                trend = 1
                high = c.high
                high_idx = i

    return pivots


def _cluster_prices(prices: Iterable[Decimal], cluster_pct: Decimal) -> list[Decimal]:
    ps = sorted(prices)
    if not ps:
        return []
    clusters: list[list[Decimal]] = [[ps[0]]]
    for p in ps[1:]:
        cur = clusters[-1]
        ref = cur[-1]
        tol = max(abs(ref) * cluster_pct, abs(p) * cluster_pct)
        if abs(p - ref) <= tol:
            cur.append(p)
        else:
            clusters.append([p])
    out: list[Decimal] = []
    for c in clusters:
        c_sorted = sorted(c)
        out.append(c_sorted[len(c_sorted) // 2])  # 中位数更稳
    return out


async def refresh_sr_levels(
    *,
    market: str,
    symbols: list[str],
    lookback_4h: int = 180,
    zigzag_pct: Decimal = Decimal("0.02"),
    zigzag_min_bars: int = 3,
    cluster_pct: Decimal = Decimal("0.003"),
    min_touches: int = 3,
    max_levels_per_symbol: int = 30,
) -> None:
    """
    计算 4h 支撑/阻力水平位并 upsert 到 sr_levels。
    - 数据来源：trade_buckets（由 Binance aggTrade WS 聚合得到）
    - 完全可复算：输入是固定窗口的 OHLC + 明确定义的 pivot + 聚类 + touches 规则
    """
    if not symbols:
        return

    all_values: list[dict[str, Any]] = []

    for sym in symbols:
        rows = await query_trade_buckets(
            market=market, symbol=sym, bucket="4h",
            start_ms=0, order="desc", limit=lookback_4h,
        )
        if len(rows) < 20:
            continue

        candles: list[Candle] = []
        for r in reversed(rows):
            o = _to_decimal(r.open_price)
            h = _to_decimal(r.high_price)
            l = _to_decimal(r.low_price)
            c = _to_decimal(r.close_price)
            qv = _to_decimal(r.quote_notional) or Decimal("0")
            if o is None or h is None or l is None or c is None:
                continue
            candles.append(
                Candle(
                    start_ms=int(r.bucket_start_ms),
                    open=o,
                    high=h,
                    low=l,
                    close=c,
                    quote_notional=qv,
                )
            )

        pivots = _find_pivots(candles, zigzag_pct, zigzag_min_bars)
        if not pivots:
            continue

        level_candidates = _cluster_prices((p for _, p in pivots), cluster_pct)
        if not level_candidates:
            continue

        now_ms = int(time.time() * 1000)
        values: list[dict[str, Any]] = []
        for lp in level_candidates:
            level_price = _quantize_level_price(lp)
            tol = abs(level_price) * cluster_pct
            touches = 0
            last_touch_ms = 0
            touch_qv = Decimal("0")

            for c in candles:
                if c.low - tol <= level_price <= c.high + tol:
                    touches += 1
                    last_touch_ms = max(last_touch_ms, c.start_ms + _bucket_ms("4h"))
                    touch_qv += c.quote_notional

            if touches < min_touches:
                continue

            recency_days = max(0.0, (now_ms - last_touch_ms) / 86_400_000.0)
            recency_factor = math.exp(-recency_days / 10.0)
            volume_bonus = 1.0 + math.log1p(float(touch_qv / Decimal("1000000")))
            strength = float(touches) * volume_bonus * recency_factor

            values.append(
                {
                    "market": market,
                    "symbol": sym,
                    "level_price": level_price,
                    "timeframe": "4h",
                    "touches": touches,
                    "strength_score": Decimal(str(strength)),
                    "last_touch_ms": int(last_touch_ms),
                }
            )

        if not values:
            continue

        values.sort(key=lambda x: float(x["strength_score"]), reverse=True)
        all_values.extend(values[:max_levels_per_symbol])

    if not all_values:
        return

    async with SessionLocal() as session:
        stmt = insert(SRLevel).values(all_values)
        stmt = stmt.on_conflict_do_update(
            index_elements=["market", "symbol", "timeframe", "level_price"],
            set_={
                "touches": stmt.excluded.touches,
                "strength_score": stmt.excluded.strength_score,
                "last_touch_ms": stmt.excluded.last_touch_ms,
                "updated_at": func.now(),  # type: ignore[name-defined]
            },
        )
        await session.execute(stmt)
        await session.commit()


async def scan_anomalies(
    *,
    market: str,
    symbols: list[str],
    history_15m: int = 96,
    breakout_margin_pct: Decimal = Decimal("0.001"),
    volume_spike_factor: Decimal = Decimal("3.0"),
    amplitude_spike_factor: Decimal = Decimal("2.5"),
) -> int:
    """
    扫描并落库异动事件（15m 信号 + 4h 水平位）。
    返回：新增事件数量（最佳努力，遇到冲突会被去重）。
    """
    symbols = filter_excluded_symbols(symbols)
    if not symbols:
        return 0

    now_ms = int(time.time() * 1000)
    b15 = _bucket_ms("15m")
    cur_start = (now_ms // b15) * b15
    last_closed_start = cur_start - b15
    start_ms = last_closed_start - (history_15m + 4) * b15

    # 拉取 15m 历史（ClickHouse）
    rows = await query_trade_buckets(
        market=market, symbols=symbols, bucket="15m",
        start_ms=start_ms, end_ms=last_closed_start,
    )

    series: dict[str, list[TradeBucketRow]] = {}
    for r in rows:
        series.setdefault(r.symbol, []).append(r)

    async with SessionLocal() as session:
        # 预取 sr_levels
        lvl_rows = (
            (
                await session.execute(
                    select(SRLevel)
                    .where(
                        and_(
                            SRLevel.market == market,
                            SRLevel.timeframe == "4h",
                            SRLevel.symbol.in_(symbols),
                        )
                    )
                    .order_by(SRLevel.symbol.asc(), SRLevel.strength_score.desc())
                )
            )
            .scalars()
            .all()
        )
        levels_by_symbol: dict[str, list[SRLevel]] = {}
        for r in lvl_rows:
            levels_by_symbol.setdefault(r.symbol, []).append(r)

        new_events: list[dict[str, Any]] = []
        for sym, s in series.items():
            if is_excluded_symbol(sym):
                continue
            if len(s) < 10:
                continue
            lvls = levels_by_symbol.get(sym) or []
            if not lvls:
                continue

            def _candle(tb: TradeBucketRow) -> Candle | None:
                o = _to_decimal(tb.open_price)
                h = _to_decimal(tb.high_price)
                l = _to_decimal(tb.low_price)
                c = _to_decimal(tb.close_price)
                qv = _to_decimal(tb.quote_notional) or Decimal("0")
                if o is None or h is None or l is None or c is None:
                    return None
                return Candle(start_ms=int(tb.bucket_start_ms), open=o, high=h, low=l, close=c, quote_notional=qv)

            candles = [c for tb in s if (c := _candle(tb)) is not None]
            if len(candles) < 10:
                continue

            last = candles[-1]
            prev1 = candles[-2] if len(candles) >= 2 else None
            prev2 = candles[-3] if len(candles) >= 3 else None
            if prev1 is None or prev2 is None:
                continue

            hist = candles[-(history_15m + 1) : -1]  # 排除 last
            if len(hist) >= 10:
                avg_vol = sum((c.quote_notional for c in hist), Decimal("0")) / Decimal(str(len(hist)))
                avg_amp = (
                    sum(((c.high - c.low) / c.open for c in hist if c.open > 0), Decimal("0"))
                    / Decimal(str(len(hist)))
                )
            else:
                avg_vol = Decimal("0")
                avg_amp = Decimal("0")

            cur_amp = (last.high - last.low) / last.open if last.open > 0 else Decimal("0")
            vol_factor = (last.quote_notional / avg_vol) if avg_vol > 0 else None
            amp_factor = (cur_amp / avg_amp) if avg_amp > 0 else None

            # 量能/振幅异常（完全可复算）
            if avg_vol > 0 and last.quote_notional >= avg_vol * volume_spike_factor:
                new_events.append(
                    {
                        "market": market,
                        "symbol": sym,
                        "event_type": "volume_spike",
                        "tf_signal": "15m",
                        "tf_level": None,
                        "event_time_ms": last.start_ms + b15,
                        "title": f"{sym} 量能异常放大",
                        "details": {
                            "bucketStartMs": last.start_ms,
                            "quoteNotional": str(last.quote_notional),
                            "avgQuoteNotional": str(avg_vol),
                            "volumeFactor": str(vol_factor) if vol_factor is not None else None,
                        },
                    }
                )
            if avg_amp > 0 and cur_amp >= avg_amp * amplitude_spike_factor and cur_amp >= Decimal("0.005"):
                new_events.append(
                    {
                        "market": market,
                        "symbol": sym,
                        "event_type": "amplitude_spike",
                        "tf_signal": "15m",
                        "tf_level": None,
                        "event_time_ms": last.start_ms + b15,
                        "title": f"{sym} 振幅异常放大",
                        "details": {
                            "bucketStartMs": last.start_ms,
                            "amplitude": str(cur_amp),
                            "avgAmplitude": str(avg_amp),
                            "amplitudeFactor": str(amp_factor) if amp_factor is not None else None,
                        },
                    }
                )

            # 突破（两根连续收盘确认）
            for lvl in lvls[:12]:
                lp = _to_decimal(lvl.level_price)
                if lp is None or lp <= 0:
                    continue
                up = lp * (Decimal("1") + breakout_margin_pct)
                down = lp * (Decimal("1") - breakout_margin_pct)

                # 向上突破：prev2 在附近/下方，prev1 & last 连续收在上方
                if prev1.close > up and last.close > up and prev2.close <= lp:
                    new_events.append(
                        {
                            "market": market,
                            "symbol": sym,
                            "event_type": "breakout_up",
                            "tf_signal": "15m",
                            "tf_level": "4h",
                            "event_time_ms": last.start_ms + b15,
                            "title": f"{sym} 突破阻力 {str(lp)}",
                            "details": {
                                "bucketStartMs": last.start_ms,
                                "levelPrice": str(lp),
                                "marginPct": str(breakout_margin_pct),
                                "confirmCloses": 2,
                                "close": str(last.close),
                                "prevClose": str(prev1.close),
                                "touches": int(lvl.touches),
                                "strengthScore": str(_to_decimal(lvl.strength_score) or ""),
                                "volumeFactor": str(vol_factor) if vol_factor is not None else None,
                                "amplitude": str(cur_amp),
                            },
                        }
                    )

                # 向下突破：prev2 在附近/上方，prev1 & last 连续收在下方
                if prev1.close < down and last.close < down and prev2.close >= lp:
                    new_events.append(
                        {
                            "market": market,
                            "symbol": sym,
                            "event_type": "breakout_down",
                            "tf_signal": "15m",
                            "tf_level": "4h",
                            "event_time_ms": last.start_ms + b15,
                            "title": f"{sym} 跌破支撑 {str(lp)}",
                            "details": {
                                "bucketStartMs": last.start_ms,
                                "levelPrice": str(lp),
                                "marginPct": str(breakout_margin_pct),
                                "confirmCloses": 2,
                                "close": str(last.close),
                                "prevClose": str(prev1.close),
                                "touches": int(lvl.touches),
                                "strengthScore": str(_to_decimal(lvl.strength_score) or ""),
                                "volumeFactor": str(vol_factor) if vol_factor is not None else None,
                                "amplitude": str(cur_amp),
                            },
                        }
                    )

        if not new_events:
            return 0

        # 去重依赖 uq_anomaly_event_dedup
        stmt = insert(AnomalyEvent).values(new_events)
        stmt = stmt.on_conflict_do_nothing(
            index_elements=["market", "symbol", "event_type", "tf_signal", "event_time_ms"]
        )
        res = await session.execute(stmt)
        await session.commit()
        try:
            return int(res.rowcount or 0)
        except Exception:
            return 0
