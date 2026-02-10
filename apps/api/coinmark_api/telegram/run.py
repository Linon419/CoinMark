from __future__ import annotations

import asyncio
import logging
import math
import re
import time
from datetime import datetime
from typing import Any
from zoneinfo import ZoneInfo

from aiogram import Bot, Dispatcher, F, Router
from aiogram.filters import Command, CommandObject
from aiogram.types import BotCommand, BotCommandScopeAllPrivateChats, MenuButtonCommands, Message
from redis.asyncio import Redis
from redis.asyncio import from_url as redis_from_url
from sqlalchemy import and_, desc, func, select

from coinmark_api.ch import (
    TradeBucketRow,
    query_funding_by_symbol,
    query_market_caps,
    query_oi_by_symbol,
    query_oi_snapshots,
    query_trade_buckets,
    query_trade_flow_agg,
)
from coinmark_api.config import settings
from coinmark_api.db import SessionLocal
from coinmark_api.models import AbsorptionSignalSnapshot, AnomalyEvent
from coinmark_api.services.binance.rest import (
    get_futures_premium_index,
    get_global_long_short_account_ratio,
    get_open_interest_hist,
    get_pairs,
    get_ticker_24h,
    get_ticker_24h_all,
    get_top_long_short_account_ratio,
    get_top_long_short_position_ratio,
)
from coinmark_api.services.bot.oi_marketcap import get_oi_marketcap_rank
from coinmark_api.services.symbol_filter import is_excluded_symbol


logging.basicConfig(level=getattr(logging, settings.api_log_level.upper(), logging.INFO))
logger = logging.getLogger("coinmark.telegram")

_DEFAULT_QUERY_TZ = "Australia/Sydney"
_TZ_ALIASES = {
    "UTC": "UTC",
    "SYDNEY": "Australia/Sydney",
    "SHANGHAI": "Asia/Shanghai",
}
_TZ_CACHE: dict[str, ZoneInfo] = {}


def _to_float(v: Any) -> float | None:
    if v is None:
        return None
    try:
        return float(v)
    except Exception:
        return None


def _fmt_compact(v: float | None) -> str:
    if v is None or not math.isfinite(v):
        return "-"
    av = abs(v)
    if av >= 1e8:
        return f"{v / 1e8:.2f}亿"
    if av >= 1e4:
        return f"{v / 1e4:.2f}万"
    return f"{v:.2f}"


def _fmt_pct(v: float | None, digits: int = 2) -> str:
    if v is None or not math.isfinite(v):
        return "-"
    return f"{v * 100:.{digits}f}%"


def _normalize_tz_name(raw: str | None) -> str | None:
    if not raw:
        return None
    s = str(raw).strip()
    if not s:
        return None
    alias = _TZ_ALIASES.get(s.upper())
    if alias:
        return alias
    try:
        ZoneInfo(s)
        return s
    except Exception:
        return None


def _zoneinfo_or_default(tz_name: str | None) -> ZoneInfo:
    name = _normalize_tz_name(tz_name) or _DEFAULT_QUERY_TZ
    cached = _TZ_CACHE.get(name)
    if cached is not None:
        return cached
    tz = ZoneInfo(name)
    _TZ_CACHE[name] = tz
    return tz


def _fmt_ts(ms: int | None, tz_name: str | None = None) -> str:
    if not ms:
        return "-"
    return datetime.fromtimestamp(ms / 1000, tz=_zoneinfo_or_default(tz_name)).strftime("%m-%d %H:%M")


def _event_type_label(event_type: str) -> str:
    mapping = {
        "breakout_up": "向上突破",
        "breakout_down": "向下跌破",
        "volume_spike": "量能放大",
        "amplitude_spike": "振幅放大",
        "signal_lab_persistent_buy": "SignalLab 吸筹",
        "signal_lab_bid_wall": "SignalLab 买盘墙",
        "signal_lab_ask_wall": "SignalLab 卖盘墙",
    }
    return mapping.get((event_type or "").lower(), event_type)


def _event_base_score(event_type: str) -> float:
    t = (event_type or "").lower()
    if t in {"breakout_up", "breakout_down"}:
        return 60.0
    if t == "signal_lab_persistent_buy":
        return 65.0
    if t in {"signal_lab_bid_wall", "signal_lab_ask_wall"}:
        return 62.0
    if t == "volume_spike":
        return 45.0
    if t == "amplitude_spike":
        return 40.0
    return 35.0


def _event_severity_score(event_type: str, details: dict[str, Any] | None) -> float:
    data = details or {}
    score = _event_base_score(event_type)

    volume_factor = _to_float(data.get("volumeFactor"))
    if volume_factor is not None and volume_factor > 1:
        score += min(20.0, (volume_factor - 1.0) * 3.0)

    amplitude = _to_float(data.get("amplitude"))
    if amplitude is not None and amplitude > 0:
        score += min(20.0, amplitude * 100.0 * 2.0)

    strength = _to_float(data.get("strengthScore"))
    if strength is not None and strength > 0:
        score += min(20.0, strength / 5.0)

    touches = _to_float(data.get("touches"))
    if touches is not None and touches >= 3:
        score += min(8.0, touches)

    return max(0.0, min(100.0, round(score, 1)))


def _event_level(score: float) -> str:
    if score >= 80:
        return "critical"
    if score >= 55:
        return "warning"
    return "info"


def _level_allowed(level: str, min_level: str) -> bool:
    rank = {"info": 1, "warning": 2, "critical": 3}
    return rank.get(level, 1) >= rank.get(min_level, 2)


def _event_narrative(row: AnomalyEvent) -> str:
    details = row.details if isinstance(row.details, dict) else {}
    t = str(row.event_type or "").lower()
    symbol = str(row.symbol)
    if t == "breakout_up":
        level_price = _to_float(details.get("levelPrice"))
        vol = _to_float(details.get("volumeFactor"))
        if level_price is not None:
            return f"{symbol} 向上突破 {level_price:.6f}，量能 {vol:.1f}x" if vol is not None else f"{symbol} 向上突破 {level_price:.6f}"
    if t == "breakout_down":
        level_price = _to_float(details.get("levelPrice"))
        amp = _to_float(details.get("amplitude"))
        if level_price is not None:
            return f"{symbol} 跌破 {level_price:.6f}，振幅 {_fmt_pct(amp)}" if amp is not None else f"{symbol} 跌破 {level_price:.6f}"
    if t == "volume_spike":
        vol = _to_float(details.get("volumeFactor"))
        return f"{symbol} 量能放大 {vol:.1f}x" if vol is not None else f"{symbol} 量能放大"
    if t == "amplitude_spike":
        amp = _to_float(details.get("amplitude"))
        return f"{symbol} 振幅扩大到 {_fmt_pct(amp)}" if amp is not None else f"{symbol} 振幅扩大"
    if t == "signal_lab_persistent_buy":
        score = _to_float(details.get("score"))
        buy_ratio = _to_float(details.get("buyRatio"))
        large_count = _to_float(details.get("largeBuyCount"))
        parts = [f"{symbol} SignalLab 持续吸筹"]
        if score is not None:
            parts.append(f"评分 {score:.1f}")
        if buy_ratio is not None:
            parts.append(f"买入占比 {buy_ratio * 100:.1f}%")
        if large_count is not None:
            parts.append(f"大单次数 {int(large_count)}")
        return "，".join(parts)
    if t in {"signal_lab_bid_wall", "signal_lab_ask_wall"}:
        impact_ratio = _to_float(details.get("impactRatio"))
        survive_count = _to_float(details.get("surviveCount"))
        confidence = str(details.get("confidence") or "MEDIUM")
        side_text = "买盘" if t == "signal_lab_bid_wall" else "卖盘"
        parts = [f"{symbol} SignalLab {side_text}挂单墙"]
        if impact_ratio is not None:
            parts.append(f"影响比 {impact_ratio:.2f}")
        if survive_count is not None:
            parts.append(f"存活 {int(survive_count)} 分钟")
        parts.append(f"置信度 {confidence}")
        return "，".join(parts)
    return str(row.title or f"{symbol} 出现异动")


class TgAnomalyNotifier:
    def __init__(self, bot: Bot, redis: Redis | None = None) -> None:
        self.bot = bot
        self.redis = redis
        self.market = settings.tg_notify_market
        self.chat_id = settings.tg_notify_chat_id
        self.poll_interval_sec = max(2, int(settings.tg_notify_poll_interval_sec))
        self.batch_window_sec = max(10, int(settings.tg_notify_batch_window_sec))
        self.batch_max_items = max(1, int(settings.tg_notify_batch_max_items))
        self.min_level = (settings.tg_notify_min_level or "warning").lower()
        self.state_key = f"{settings.tg_state_redis_prefix}:notify:last_id:{self.market}"
        self.last_id = 0
        self.buffer: list[AnomalyEvent] = []
        self.last_flush_at = time.monotonic()

    async def _bootstrap_last_id(self) -> None:
        if self.redis:
            raw = await self.redis.get(self.state_key)
            if raw:
                try:
                    self.last_id = int(raw)
                    return
                except Exception:
                    pass
        async with SessionLocal() as session:
            stmt = select(func.max(AnomalyEvent.id)).where(AnomalyEvent.market == self.market)
            latest = (await session.execute(stmt)).scalar()
            self.last_id = int(latest or 0)
        if self.redis:
            await self.redis.set(self.state_key, str(self.last_id))

    async def _persist_last_id(self) -> None:
        if self.redis:
            await self.redis.set(self.state_key, str(self.last_id))

    async def _fetch_new(self) -> list[AnomalyEvent]:
        async with SessionLocal() as session:
            stmt = (
                select(AnomalyEvent)
                .where(and_(AnomalyEvent.market == self.market, AnomalyEvent.id > self.last_id))
                .order_by(AnomalyEvent.id.asc())
                .limit(500)
            )
            rows = (await session.execute(stmt)).scalars().all()
        return rows

    def _filter(self, rows: list[AnomalyEvent]) -> list[AnomalyEvent]:
        out: list[AnomalyEvent] = []
        for r in rows:
            if is_excluded_symbol(getattr(r, "symbol", None)):
                continue
            details = r.details if isinstance(r.details, dict) else {}
            level = _event_level(_event_severity_score(str(r.event_type), details))
            if _level_allowed(level, self.min_level):
                out.append(r)
        return out

    def _build_batch_message(self, rows: list[AnomalyEvent]) -> str:
        lines: list[str] = [f"【市场异动快讯】{_fmt_ts(int(time.time() * 1000))}"]
        for idx, row in enumerate(rows[: self.batch_max_items], start=1):
            details = row.details if isinstance(row.details, dict) else {}
            score = _event_severity_score(str(row.event_type), details)
            level = _event_level(score)
            type_label = _event_type_label(str(row.event_type))
            tf = str(row.tf_signal or "-")
            tf_level = str(row.tf_level or "-")
            lines.append(
                f"{idx}. {row.symbol} | {type_label} | {tf}/{tf_level} | {level} {score:.1f}"
            )
            lines.append(f"   {_event_narrative(row)}")
            lines.append(f"   时间：{_fmt_ts(int(row.event_time_ms or 0))}")
        return "\n".join(lines)

    async def _flush(self) -> None:
        if not self.buffer:
            self.last_flush_at = time.monotonic()
            return
        dedupe: set[tuple[str, str, int]] = set()
        uniq: list[AnomalyEvent] = []
        for row in self.buffer:
            key = (str(row.symbol), str(row.event_type), int(row.event_time_ms or 0))
            if key in dedupe:
                continue
            dedupe.add(key)
            uniq.append(row)
        uniq.sort(
            key=lambda r: _event_severity_score(
                str(r.event_type), r.details if isinstance(r.details, dict) else {}
            ),
            reverse=True,
        )
        for i in range(0, len(uniq), self.batch_max_items):
            chunk = uniq[i : i + self.batch_max_items]
            msg = self._build_batch_message(chunk)
            await self.bot.send_message(chat_id=self.chat_id, text=msg)
        self.buffer.clear()
        self.last_flush_at = time.monotonic()

    async def run(self) -> None:
        await self._bootstrap_last_id()
        logger.info("TG 通知 Bot 已启动，market=%s, minLevel=%s", self.market, self.min_level)
        while True:
            try:
                rows = await self._fetch_new()
                if rows:
                    self.last_id = max(self.last_id, max(int(r.id) for r in rows))
                    await self._persist_last_id()
                    self.buffer.extend(self._filter(rows))

                now = time.monotonic()
                if self.buffer and (
                    len(self.buffer) >= self.batch_max_items
                    or now - self.last_flush_at >= self.batch_window_sec
                ):
                    await self._flush()

            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("TG 通知 Bot 轮询失败")
            await asyncio.sleep(self.poll_interval_sec)


class QueryService:
    @staticmethod
    async def latest_price(symbol: str, market: str = "swap") -> dict[str, Any] | None:
        rows = await query_trade_buckets(
            market=market,
            symbol=symbol,
            bucket="1m",
            start_ms=int(time.time() * 1000) - 5 * 60_000,
            order="desc",
            limit=1,
        )
        if not rows:
            return None
        row: TradeBucketRow = rows[0]
        close_price = _to_float(row.close_price)
        buy = _to_float(row.taker_buy_notional) or 0.0
        sell = _to_float(row.taker_sell_notional) or 0.0
        return {
            "symbol": symbol,
            "market": market,
            "closePrice": close_price,
            "netFlow": buy - sell,
            "quoteNotional": _to_float(row.quote_notional),
            "ts": int(row.bucket_start_ms),
        }

    @staticmethod
    async def fund_snapshot(symbol: str, market: str = "swap") -> dict[str, Any] | None:
        rows = await query_trade_buckets(
            market=market,
            symbol=symbol,
            bucket="1h",
            start_ms=int(time.time() * 1000) - 25 * 3_600_000,
            order="desc",
            limit=24,
        )
        if not rows:
            return None
        latest: TradeBucketRow = rows[0]
        net_1h = (_to_float(latest.taker_buy_notional) or 0.0) - (_to_float(latest.taker_sell_notional) or 0.0)
        net_24h = 0.0
        for row in rows:
            net_24h += (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
        return {
            "symbol": symbol,
            "market": market,
            "net1h": net_1h,
            "net24h": net_24h,
            "ts": int(latest.bucket_start_ms),
        }

    @staticmethod
    async def absorption(symbol: str, market: str = "swap") -> dict[str, Any] | None:
        async with SessionLocal() as session:
            stmt = (
                select(AbsorptionSignalSnapshot)
                .where(
                    and_(
                        AbsorptionSignalSnapshot.market == market,
                        AbsorptionSignalSnapshot.symbol == symbol,
                        AbsorptionSignalSnapshot.direction == "LONG_BIAS",
                    )
                )
                .order_by(AbsorptionSignalSnapshot.bucket_start_ms.desc())
                .limit(1)
            )
            row = (await session.execute(stmt)).scalars().first()
        if not row:
            return None
        return {
            "symbol": symbol,
            "state": str(row.signal_state),
            "score": _to_float(row.score) or 0.0,
            "reasons": row.reasons if isinstance(row.reasons, list) else [],
            "ts": int(row.bucket_start_ms),
        }

    @staticmethod
    async def latest_anomalies(symbol: str | None, market: str = "swap", limit: int = 5) -> list[AnomalyEvent]:
        if symbol and is_excluded_symbol(symbol):
            return []
        async with SessionLocal() as session:
            stmt = select(AnomalyEvent).where(AnomalyEvent.market == market)
            if symbol:
                stmt = stmt.where(AnomalyEvent.symbol == symbol)
            stmt = stmt.order_by(desc(AnomalyEvent.event_time_ms)).limit(limit)
            rows = (await session.execute(stmt)).scalars().all()
        return [row for row in rows if not is_excluded_symbol(getattr(row, "symbol", None))]

    @staticmethod
    async def one_day_net_rank(market: str, direction: str, limit: int = 30) -> dict[str, Any]:
        symbols = await get_pairs(market)
        if not symbols:
            return {"count": 0, "items": []}

        start_ms = int(time.time() * 1000) - 24 * 60 * 60 * 1000
        rows = await query_trade_flow_agg(
            market=market,
            symbols=symbols,
            bucket="1m",
            start_ms=start_ms,
        )
        items: list[tuple[str, float]] = []
        for symbol, buy_sum, sell_sum in rows:
            net = float(buy_sum) - float(sell_sum)
            if direction == "in" and net > 0:
                items.append((symbol, net))
            elif direction == "out" and net < 0:
                items.append((symbol, net))

        if direction == "in":
            items.sort(key=lambda x: x[1], reverse=True)
        else:
            items.sort(key=lambda x: x[1])

        return {"count": len(items), "items": items[:limit]}

    @staticmethod
    async def one_day_net_map(market: str, symbols: list[str]) -> dict[str, float]:
        if not symbols:
            return {}
        unique_symbols = sorted(set(symbols))
        start_ms = int(time.time() * 1000) - 24 * 60 * 60 * 1000
        rows = await query_trade_flow_agg(
            market=market,
            symbols=unique_symbols,
            bucket="1m",
            start_ms=start_ms,
        )
        return {symbol: float(buy_sum) - float(sell_sum) for symbol, buy_sum, sell_sum in rows}

    @staticmethod
    async def return_rank(bucket: str, limit: int, market: str = "swap") -> dict[str, Any]:
        bucket_ms_map = {
            "15m": 15 * 60 * 1000,
            "1h": 60 * 60 * 1000,
            "4h": 4 * 60 * 60 * 1000,
            "1d": 24 * 60 * 60 * 1000,
        }
        bms = int(bucket_ms_map.get(bucket, 15 * 60 * 1000))
        now_ms = int(time.time() * 1000)
        last_closed_start = (now_ms // bms) * bms - bms

        probe = await query_trade_buckets(
            market=market,
            bucket=bucket,
            start_ms=last_closed_start,
            end_ms=last_closed_start,
            limit=1,
        )
        target_start = last_closed_start
        if not probe:
            fallback = await query_trade_buckets(
                market=market,
                bucket=bucket,
                start_ms=0,
                order="desc",
                limit=1,
            )
            target_start = int(fallback[0].bucket_start_ms) if fallback else None

        if target_start is None:
            return {"bucket": bucket, "bucketStartMs": None, "gainers": [], "losers": []}

        rows = await query_trade_buckets(
            market=market,
            bucket=bucket,
            start_ms=target_start,
            end_ms=target_start,
        )
        items: list[dict[str, Any]] = []
        for row in rows:
            open_price = _to_float(row.open_price)
            close_price = _to_float(row.close_price)
            if open_price is None or close_price is None or open_price <= 0:
                continue
            pct = (close_price - open_price) / open_price * 100.0
            items.append({"symbol": row.symbol, "pct": pct})

        gainers = sorted(items, key=lambda x: float(x["pct"]), reverse=True)[: max(1, int(limit))]
        losers = sorted(items, key=lambda x: float(x["pct"]))[: max(1, int(limit))]
        return {
            "bucket": bucket,
            "bucketStartMs": int(target_start),
            "gainers": gainers,
            "losers": losers,
        }

    @staticmethod
    async def bullindex_rank(limit: int, market: str = "swap") -> dict[str, Any]:
        now_ms = int(time.time() * 1000)
        bms = 60 * 60 * 1000
        last_closed_start = (now_ms // bms) * bms - bms

        target_start: int | None = None
        rows: list[TradeBucketRow] = []
        cur_start = int(last_closed_start)

        for _ in range(24):
            cur_end = cur_start + bms - 1
            probe = await query_trade_buckets(
                market=market,
                bucket="1h",
                start_ms=cur_start,
                end_ms=cur_end,
                limit=1,
            )
            if probe:
                target_start = cur_start
                break
            cur_start -= bms

        if target_start is None:
            return {"bucketStartMs": None, "items": []}

        rows = await query_trade_buckets(
            market=market,
            bucket="1h",
            start_ms=int(target_start),
            end_ms=int(target_start) + bms - 1,
        )
        if not rows:
            return {"bucketStartMs": int(target_start), "items": []}

        items: list[dict[str, Any]] = []
        for row in rows:
            open_price = _to_float(row.open_price)
            close_price = _to_float(row.close_price)
            buy = _to_float(row.taker_buy_notional) or 0.0
            sell = _to_float(row.taker_sell_notional) or 0.0
            if open_price is None or close_price is None or open_price <= 0:
                continue
            ret_pct = (close_price - open_price) / open_price * 100.0
            flow_denom = buy + sell
            flow_bias = ((buy - sell) / flow_denom) if flow_denom > 0 else 0.0
            score = _clamp_score(50.0 + ret_pct * 2.0 + flow_bias * 50.0)
            items.append(
                {
                    "symbol": row.symbol,
                    "score": score,
                    "retPct": ret_pct,
                    "flowBiasPct": flow_bias * 100.0,
                }
            )

        items.sort(key=lambda x: float(x.get("score") or 0.0), reverse=True)
        return {
            "bucketStartMs": int(target_start),
            "items": items[: max(1, int(limit))],
        }

    @staticmethod
    async def openinterest_growth_rank(limit: int) -> list[dict[str, Any]]:
        oi_rows = await query_oi_snapshots()
        if not oi_rows:
            return []

        candidate_count = min(max(int(limit) * 3, 40), 120)
        candidates = sorted(
            [row for row in oi_rows if _to_float(row.oi_notional_usd) is not None],
            key=lambda row: float(_to_float(row.oi_notional_usd) or 0.0),
            reverse=True,
        )[:candidate_count]

        semaphore = asyncio.Semaphore(8)

        async def _one(row: Any) -> dict[str, Any] | None:
            symbol = str(getattr(row, "symbol", "") or "")
            if not symbol:
                return None
            async with semaphore:
                try:
                    hist = await get_open_interest_hist(symbol=symbol, period="1d", limit=2)
                except Exception:
                    return None
            if not isinstance(hist, list) or len(hist) < 2:
                return None
            valid_hist = [item for item in hist if isinstance(item, dict)]
            valid_hist.sort(key=lambda item: int(item.get("timestamp") or 0))
            if len(valid_hist) < 2:
                return None
            prev = _to_float(valid_hist[-2].get("sumOpenInterestValue"))
            curr = _to_float(valid_hist[-1].get("sumOpenInterestValue"))
            if prev is None or curr is None or prev <= 0:
                return None
            pct = (curr - prev) / prev * 100.0
            return {
                "symbol": symbol,
                "changePct": pct,
                "oiNotionalUsd": _to_float(getattr(row, "oi_notional_usd", None)) or curr,
            }

        rows = await asyncio.gather(*[_one(row) for row in candidates])
        items = [item for item in rows if item is not None]
        items.sort(key=lambda x: float(x.get("changePct") or -10**9), reverse=True)
        return items[: max(1, int(limit))]

    @staticmethod
    async def oicapratio_rank(limit: int) -> list[dict[str, Any]]:
        items = await get_oi_marketcap_rank(limit=max(1, int(limit)))
        out: list[dict[str, Any]] = []
        for item in items:
            ratio = _to_float(item.get("ratio"))
            out.append(
                {
                    "symbol": str(item.get("symbol") or ""),
                    "ratioPct": (ratio * 100.0) if ratio is not None else None,
                    "oiNotionalUsd": _to_float(item.get("oiNotionalUsd")),
                    "marketCapUsd": _to_float(item.get("marketCapUsd")),
                }
            )
        return out

    @staticmethod
    async def market_overview() -> dict[str, Any]:
        valid_swap = set(await get_pairs("swap"))
        tickers_raw = await get_ticker_24h_all("swap")
        tickers: list[dict[str, Any]] = []
        for item in tickers_raw:
            symbol = str(item.get("symbol") or "")
            if not symbol.endswith("USDT") or symbol not in valid_swap:
                continue
            pct = _to_float(item.get("priceChangePercent"))
            quote_volume = _to_float(item.get("quoteVolume"))
            if pct is None or quote_volume is None:
                continue
            tickers.append(
                {
                    "symbol": symbol,
                    "pct": float(pct),
                    "quoteVolume": float(quote_volume),
                }
            )

        gainers_count = sum(1 for item in tickers if item["pct"] > 0)
        losers_count = sum(1 for item in tickers if item["pct"] < 0)

        top_active = sorted(tickers, key=lambda x: x["quoteVolume"], reverse=True)[:10]
        top_gainers = sorted(tickers, key=lambda x: x["pct"], reverse=True)[:10]
        top_losers = sorted(tickers, key=lambda x: x["pct"])[:10]

        major_symbols = ["BTCUSDT", "ETHUSDT", "SOLUSDT"]
        swap_net_map, spot_net_map = await asyncio.gather(
            QueryService.one_day_net_map("swap", major_symbols),
            QueryService.one_day_net_map("spot", major_symbols),
        )

        oi_candidates = [item["symbol"] for item in sorted(tickers, key=lambda x: x["quoteVolume"], reverse=True)[:40]]
        semaphore = asyncio.Semaphore(8)

        async def _oi_growth(symbol: str) -> tuple[str, float] | None:
            async with semaphore:
                try:
                    hist = await get_open_interest_hist(symbol=symbol, period="1d", limit=2)
                except Exception:
                    return None
            if not isinstance(hist, list) or len(hist) < 2:
                return None
            sorted_hist = sorted(
                [entry for entry in hist if isinstance(entry, dict)],
                key=lambda x: int(x.get("timestamp") or 0),
            )
            if len(sorted_hist) < 2:
                return None
            prev = _to_float(sorted_hist[-2].get("sumOpenInterestValue"))
            curr = _to_float(sorted_hist[-1].get("sumOpenInterestValue"))
            if prev is None or curr is None or prev <= 0:
                return None
            pct = (curr - prev) / prev * 100.0
            return symbol, pct

        oi_results = await asyncio.gather(*[_oi_growth(symbol) for symbol in oi_candidates])
        oi_growth_top = [item for item in oi_results if item is not None and item[1] > 0]
        oi_growth_top.sort(key=lambda x: x[1], reverse=True)
        oi_growth_top = oi_growth_top[:10]

        return {
            "gainersCount": gainers_count,
            "losersCount": losers_count,
            "sentiment": {
                "index": "-",
                "vix": "-",
                "dxy": "-",
                "us10y": "-",
            },
            "majorNetFlow": {
                "BTC": {
                    "swap": swap_net_map.get("BTCUSDT", 0.0),
                    "spot": spot_net_map.get("BTCUSDT", 0.0),
                },
                "ETH": {
                    "swap": swap_net_map.get("ETHUSDT", 0.0),
                    "spot": spot_net_map.get("ETHUSDT", 0.0),
                },
                "SOL": {
                    "swap": swap_net_map.get("SOLUSDT", 0.0),
                    "spot": spot_net_map.get("SOLUSDT", 0.0),
                },
            },
            "topActive": top_active,
            "topOiGrowth": oi_growth_top,
            "topGainers": top_gainers,
            "topLosers": top_losers,
        }


    @staticmethod
    async def _symbol_net_flow(market: str, symbol: str, days: int) -> float:
        start_ms = int(time.time() * 1000) - int(days) * 24 * 60 * 60 * 1000
        rows = await query_trade_flow_agg(
            market=market,
            symbols=[symbol],
            bucket="1m",
            start_ms=start_ms,
        )
        if not rows:
            return 0.0
        _, buy_sum, sell_sum = rows[0]
        return float(buy_sum) - float(sell_sum)

    @staticmethod
    async def symbol_fund_windows(symbol: str, windows: list[int]) -> list[dict[str, Any]]:
        if not windows:
            return []

        max_days = max([int(day) for day in windows])
        start_ms = int(time.time() * 1000) - (max_days + 1) * 24 * 60 * 60 * 1000

        swap_rows, spot_rows = await asyncio.gather(
            query_trade_buckets(
                market="swap",
                symbol=symbol,
                bucket="1d",
                start_ms=start_ms,
                order="desc",
                limit=max_days + 2,
            ),
            query_trade_buckets(
                market="spot",
                symbol=symbol,
                bucket="1d",
                start_ms=start_ms,
                order="desc",
                limit=max_days + 2,
            ),
        )

        def _sum_net(rows: list[TradeBucketRow], day: int) -> float:
            use_rows = rows[: max(0, int(day))]
            total = 0.0
            for row in use_rows:
                total += (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
            return total

        out: list[dict[str, Any]] = []
        for day in windows:
            out.append(
                {
                    "day": int(day),
                    "swap": _sum_net(swap_rows, int(day)),
                    "spot": _sum_net(spot_rows, int(day)),
                }
            )
        return out

    @staticmethod
    async def symbol_long_short_rows(symbol: str, limit: int = 5) -> list[dict[str, Any]]:
        try:
            global_rows, top_account_rows, top_position_rows = await asyncio.gather(
                get_global_long_short_account_ratio(symbol=symbol, period="1h", limit=limit),
                get_top_long_short_account_ratio(symbol=symbol, period="1h", limit=limit),
                get_top_long_short_position_ratio(symbol=symbol, period="1h", limit=limit),
            )
        except Exception:
            return []

        def _index_by_ts(rows: list[dict[str, Any]]) -> dict[int, float]:
            out: dict[int, float] = {}
            for item in rows:
                ts = int(item.get("timestamp") or 0)
                ratio = _to_float(item.get("longShortRatio"))
                if ts > 0 and ratio is not None:
                    out[ts] = float(ratio)
            return out

        g_map = _index_by_ts(global_rows if isinstance(global_rows, list) else [])
        a_map = _index_by_ts(top_account_rows if isinstance(top_account_rows, list) else [])
        p_map = _index_by_ts(top_position_rows if isinstance(top_position_rows, list) else [])

        keys = sorted(set(g_map.keys()) | set(a_map.keys()) | set(p_map.keys()))[-limit:]
        rows: list[dict[str, Any]] = []
        for key in keys:
            rows.append(
                {
                    "timestamp": key,
                    "globalRatio": g_map.get(key, 0.0),
                    "topAccountRatio": a_map.get(key, 0.0),
                    "topPositionRatio": p_map.get(key, 0.0),
                }
            )
        return rows

    @staticmethod
    async def symbol_brief(symbol: str) -> dict[str, Any]:
        now_ms = int(time.time() * 1000)
        base = _strip_usdt(symbol)

        ticker_task = get_ticker_24h("swap", symbol)
        h1_rows_task = query_trade_buckets(
            market="swap",
            symbol=symbol,
            bucket="1h",
            start_ms=now_ms - 8 * 3_600_000,
            order="desc",
            limit=8,
        )
        d1_rows_task = query_trade_buckets(
            market="swap",
            symbol=symbol,
            bucket="1d",
            start_ms=now_ms - 120 * 86_400_000,
            order="desc",
            limit=120,
        )
        oi_snapshot_task = query_oi_by_symbol(symbol)
        funding_snapshot_task = query_funding_by_symbol(symbol)
        cap_single_task = query_market_caps([base])
        cap_all_task = query_market_caps()

        ticker, h1_rows, d1_rows, oi_snapshot, funding_snapshot, cap_single_rows, cap_all_rows = await asyncio.gather(
            ticker_task,
            h1_rows_task,
            d1_rows_task,
            oi_snapshot_task,
            funding_snapshot_task,
            cap_single_task,
            cap_all_task,
        )

        try:
            premium = await get_futures_premium_index(symbol)
        except Exception:
            premium = {}

        try:
            oi_hist = await get_open_interest_hist(symbol=symbol, period="1d", limit=2)
        except Exception:
            oi_hist = []

        latest_price = _to_float((ticker or {}).get("lastPrice"))
        day_change_pct = _to_float((ticker or {}).get("priceChangePercent"))

        high_6h: float | None = None
        low_6h: float | None = None
        latest_quote_notional: float | None = None
        avg_quote_notional: float | None = None
        amp_6h_pct: float | None = None
        volume_factor: float | None = None
        range_label = "-"

        if h1_rows:
            latest_row = h1_rows[0]
            latest_quote_notional = _to_float(latest_row.quote_notional)
            highs = [_to_float(row.high_price) for row in h1_rows]
            lows = [_to_float(row.low_price) for row in h1_rows]
            highs = [x for x in highs if x is not None]
            lows = [x for x in lows if x is not None]
            if highs:
                high_6h = max(highs)
            if lows:
                low_6h = min(lows)
            if latest_price is not None and high_6h is not None and low_6h is not None and high_6h > low_6h:
                pos = (latest_price - low_6h) / (high_6h - low_6h)
                if pos <= 0.33:
                    range_label = "底部"
                elif pos >= 0.67:
                    range_label = "顶部"
                else:
                    range_label = "中部"
                amp_6h_pct = (high_6h - low_6h) / latest_price * 100.0 if latest_price > 0 else None

            quote_values = [_to_float(row.quote_notional) for row in h1_rows if _to_float(row.quote_notional) is not None]
            if quote_values:
                latest_quote_notional = quote_values[0]
            if len(quote_values) >= 2:
                avg_quote_notional = sum(quote_values[1:]) / len(quote_values[1:])
                if latest_quote_notional is not None and avg_quote_notional and avg_quote_notional > 0:
                    volume_factor = latest_quote_notional / avg_quote_notional

        today_start_ms = (now_ms // 86_400_000) * 86_400_000
        async with SessionLocal() as session:
            stmt = select(func.count()).select_from(AnomalyEvent).where(
                and_(
                    AnomalyEvent.market == "swap",
                    AnomalyEvent.symbol == symbol,
                    AnomalyEvent.event_time_ms >= today_start_ms,
                    AnomalyEvent.event_type.in_(["breakout_up", "breakout_down", "amplitude_spike"]),
                )
            )
            wave_count = int((await session.execute(stmt)).scalar() or 0)

        closed_daily_rows = [row for row in d1_rows if int(row.bucket_start_ms) < today_start_ms]
        if not closed_daily_rows:
            closed_daily_rows = d1_rows

        def _daily_return(row: TradeBucketRow) -> float | None:
            open_price = _to_float(row.open_price)
            close_price = _to_float(row.close_price)
            if open_price is None or close_price is None or open_price <= 0:
                return None
            return (close_price - open_price) / open_price * 100.0

        yesterday_ret = _daily_return(closed_daily_rows[0]) if closed_daily_rows else None

        def _window_ret(rows: list[TradeBucketRow], days: int, offset: int = 0) -> float | None:
            if len(rows) < offset + days:
                return None
            latest = rows[offset]
            oldest = rows[offset + days - 1]
            latest_close = _to_float(latest.close_price)
            oldest_open = _to_float(oldest.open_price)
            if latest_close is None or oldest_open is None or oldest_open <= 0:
                return None
            return (latest_close - oldest_open) / oldest_open * 100.0

        this_week_ret = _window_ret(closed_daily_rows, 7, 0)
        last_week_ret = _window_ret(closed_daily_rows, 7, 7)
        trend_6d = _score_trend(closed_daily_rows, 6)
        trend_60d = _score_trend(closed_daily_rows, 60)

        funding_rate = _to_float((premium or {}).get("lastFundingRate"))
        if funding_rate is None and funding_snapshot is not None:
            funding_rate = _to_float(funding_snapshot.last_funding_rate)

        oi_qty = _to_float(oi_snapshot.open_interest) if oi_snapshot is not None else None
        oi_value = _to_float(oi_snapshot.oi_notional_usd) if oi_snapshot is not None else None
        oi_qty_change_pct: float | None = None
        oi_value_change_pct: float | None = None
        if isinstance(oi_hist, list):
            valid_hist = [item for item in oi_hist if isinstance(item, dict)]
            valid_hist.sort(key=lambda item: int(item.get("timestamp") or 0))
            if len(valid_hist) >= 2:
                prev = valid_hist[-2]
                curr = valid_hist[-1]
                prev_qty = _to_float(prev.get("sumOpenInterest"))
                curr_qty = _to_float(curr.get("sumOpenInterest"))
                prev_val = _to_float(prev.get("sumOpenInterestValue"))
                curr_val = _to_float(curr.get("sumOpenInterestValue"))
                if oi_qty is None:
                    oi_qty = curr_qty
                if oi_value is None:
                    oi_value = curr_val
                if prev_qty is not None and curr_qty is not None and prev_qty > 0:
                    oi_qty_change_pct = (curr_qty - prev_qty) / prev_qty * 100.0
                if prev_val is not None and curr_val is not None and prev_val > 0:
                    oi_value_change_pct = (curr_val - prev_val) / prev_val * 100.0

        cap_row = cap_single_rows[0] if cap_single_rows else None
        market_cap = _to_float(cap_row.market_cap_usd) if cap_row is not None else None
        total_cap = sum([_to_float(row.market_cap_usd) or 0.0 for row in cap_all_rows]) if cap_all_rows else 0.0
        cap_dominance_pct = (market_cap / total_cap * 100.0) if market_cap and total_cap > 0 else None
        position_scale_pct = (oi_value / market_cap * 100.0) if oi_value is not None and market_cap and market_cap > 0 else None

        return {
            "symbol": symbol,
            "price": latest_price,
            "priceChangePct": day_change_pct,
            "rangeLabel": range_label,
            "amp6hPct": amp_6h_pct,
            "volumeFactor": volume_factor,
            "waveCount": wave_count,
            "touchText": "-",
            "yesterdayPct": yesterday_ret,
            "lastWeekPct": last_week_ret,
            "thisWeekPct": this_week_ret,
            "trend6": trend_6d,
            "trend60": trend_60d,
            "oiQty": oi_qty,
            "oiQtyChangePct": oi_qty_change_pct,
            "oiValue": oi_value,
            "oiValueChangePct": oi_value_change_pct,
            "positionScalePct": position_scale_pct,
            "fundingRate": funding_rate,
            "marketCap": market_cap,
            "capDominancePct": cap_dominance_pct,
            "fdv": None,
        }

    @staticmethod
    async def intraday_net_snapshot(symbol: str, hours: int = 6) -> list[dict[str, Any]]:
        now_ms = int(time.time() * 1000)
        hour_ms = 3_600_000
        day_start_ms = (now_ms // 86_400_000) * 86_400_000
        curr_hour_start = (now_ms // hour_ms) * hour_ms

        hist_start_ms = max(day_start_ms, curr_hour_start - max(1, int(hours)) * hour_ms)
        swap_rows_task = query_trade_buckets(
            market="swap",
            symbol=symbol,
            bucket="1h",
            start_ms=hist_start_ms,
            end_ms=curr_hour_start - hour_ms,
            order="asc",
            limit=max(1, int(hours)) + 2,
        )
        spot_rows_task = query_trade_buckets(
            market="spot",
            symbol=symbol,
            bucket="1h",
            start_ms=hist_start_ms,
            end_ms=curr_hour_start - hour_ms,
            order="asc",
            limit=max(1, int(hours)) + 2,
        )
        swap_curr_task = query_trade_flow_agg(
            market="swap",
            symbols=[symbol],
            bucket="1m",
            start_ms=curr_hour_start,
        )
        spot_curr_task = query_trade_flow_agg(
            market="spot",
            symbols=[symbol],
            bucket="1m",
            start_ms=curr_hour_start,
        )
        swap_rows, spot_rows, swap_curr_rows, spot_curr_rows = await asyncio.gather(
            swap_rows_task,
            spot_rows_task,
            swap_curr_task,
            spot_curr_task,
        )

        swap_map = {
            int(row.bucket_start_ms): (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
            for row in swap_rows
        }
        spot_map = {
            int(row.bucket_start_ms): (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
            for row in spot_rows
        }

        keys = sorted(set(swap_map.keys()) | set(spot_map.keys()))
        items: list[dict[str, Any]] = []
        for key in keys:
            items.append({"ts": key, "swap": swap_map.get(key, 0.0), "spot": spot_map.get(key, 0.0), "isCurrent": False})

        if swap_curr_rows:
            _, buy_sum, sell_sum = swap_curr_rows[0]
            swap_curr = float(buy_sum) - float(sell_sum)
        else:
            swap_curr = 0.0

        if spot_curr_rows:
            _, buy_sum, sell_sum = spot_curr_rows[0]
            spot_curr = float(buy_sum) - float(sell_sum)
        else:
            spot_curr = 0.0

        items.append({"ts": now_ms, "swap": swap_curr, "spot": spot_curr, "isCurrent": True})
        return items

    @staticmethod
    async def daily_net_series(symbol: str, days: int = 30) -> list[dict[str, Any]]:
        now_ms = int(time.time() * 1000)
        day_ms = 86_400_000
        start_ms = now_ms - (max(1, int(days)) + 2) * day_ms
        swap_rows, spot_rows = await asyncio.gather(
            query_trade_buckets(
                market="swap",
                symbol=symbol,
                bucket="1d",
                start_ms=start_ms,
                order="asc",
                limit=max(1, int(days)) + 3,
            ),
            query_trade_buckets(
                market="spot",
                symbol=symbol,
                bucket="1d",
                start_ms=start_ms,
                order="asc",
                limit=max(1, int(days)) + 3,
            ),
        )

        swap_map = {
            int(row.bucket_start_ms): (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
            for row in swap_rows
        }
        spot_map = {
            int(row.bucket_start_ms): (_to_float(row.taker_buy_notional) or 0.0) - (_to_float(row.taker_sell_notional) or 0.0)
            for row in spot_rows
        }

        keys = sorted(set(swap_map.keys()) | set(spot_map.keys()))
        items = [{"ts": key, "swap": swap_map.get(key, 0.0), "spot": spot_map.get(key, 0.0)} for key in keys]
        return items[-max(1, int(days)) :]

    @staticmethod
    async def oi_hourly_snapshot(symbol: str, hours: int = 24) -> list[dict[str, Any]]:
        rows = await get_open_interest_hist(symbol=symbol, period="1h", limit=max(2, int(hours)))
        if not isinstance(rows, list):
            rows = []
        rows = [item for item in rows if isinstance(item, dict)]
        rows.sort(key=lambda item: int(item.get("timestamp") or 0))

        day_start_ms = (int(time.time() * 1000) // 86_400_000) * 86_400_000
        day_open_oi: float | None = None
        for item in rows:
            ts = int(item.get("timestamp") or 0)
            if ts >= day_start_ms:
                day_open_oi = _to_float(item.get("sumOpenInterest"))
                break

        out: list[dict[str, Any]] = []
        for item in rows[-max(2, int(hours)) :]:
            ts = int(item.get("timestamp") or 0)
            oi_qty = _to_float(item.get("sumOpenInterest"))
            oi_val = _to_float(item.get("sumOpenInterestValue"))
            change_pct = None
            if day_open_oi is not None and oi_qty is not None and day_open_oi > 0 and ts >= day_start_ms:
                change_pct = (oi_qty - day_open_oi) / day_open_oi * 100.0
            out.append({"ts": ts, "oiQty": oi_qty, "oiValue": oi_val, "changePct": change_pct})

        latest_snapshot = await query_oi_by_symbol(symbol)
        if latest_snapshot is not None:
            now_ms = int(time.time() * 1000)
            oi_qty = _to_float(latest_snapshot.open_interest)
            oi_val = _to_float(latest_snapshot.oi_notional_usd)
            change_pct = None
            if day_open_oi is not None and oi_qty is not None and day_open_oi > 0:
                change_pct = (oi_qty - day_open_oi) / day_open_oi * 100.0
            out.append({"ts": now_ms, "oiQty": oi_qty, "oiValue": oi_val, "changePct": change_pct})

        return out

    @staticmethod
    async def oi_daily_series(symbol: str, days: int = 30) -> list[dict[str, Any]]:
        rows = await get_open_interest_hist(symbol=symbol, period="1d", limit=max(2, int(days)))
        if not isinstance(rows, list):
            rows = []
        rows = [item for item in rows if isinstance(item, dict)]
        rows.sort(key=lambda item: int(item.get("timestamp") or 0))

        out: list[dict[str, Any]] = []
        prev_qty: float | None = None
        for item in rows[-max(2, int(days)) :]:
            ts = int(item.get("timestamp") or 0)
            oi_qty = _to_float(item.get("sumOpenInterest"))
            oi_val = _to_float(item.get("sumOpenInterestValue"))
            change_pct = None
            if prev_qty is not None and oi_qty is not None and prev_qty > 0:
                change_pct = (oi_qty - prev_qty) / prev_qty * 100.0
            out.append({"ts": ts, "oiQty": oi_qty, "oiValue": oi_val, "changePct": change_pct})
            if oi_qty is not None:
                prev_qty = oi_qty

        return out

def _normalize_symbol(raw: str | None) -> str | None:
    if not raw:
        return None
    s = raw.strip().upper()
    if not s:
        return None
    if not s.endswith("USDT"):
        s = f"{s}USDT"
    return s


def _strip_usdt(symbol: str) -> str:
    s = str(symbol or "").upper()
    return s[:-4] if s.endswith("USDT") else s


def _fmt_pct_value(value: float) -> str:
    return f"{value:.2f}%"


def _fmt_pct_items(items: list[dict[str, Any]], limit: int = 10) -> str:
    if not items:
        return "-"
    return ",".join([f"{_strip_usdt(item['symbol'])}({_fmt_pct_value(float(item['pct']))})" for item in items[:limit]])


def _fmt_symbol_items(items: list[dict[str, Any]], limit: int = 10) -> str:
    if not items:
        return "-"
    return ",".join([_strip_usdt(item["symbol"]) for item in items[:limit]])


def _fmt_oi_growth_items(items: list[tuple[str, float]], limit: int = 10) -> str:
    if not items:
        return "-"
    return ",".join([f"{_strip_usdt(symbol)}({_fmt_pct_value(float(pct))})" for symbol, pct in items[:limit]])


def _fmt_flow_rank(items: list[tuple[str, float]]) -> list[str]:
    lines: list[str] = []
    for index, (symbol, net) in enumerate(items, start=1):
        lines.append(f"{index}.{_strip_usdt(symbol)} {_fmt_compact(net)}")
    return lines

def _fmt_signed_pct_value(value: float | None, digits: int = 2) -> str:
    if value is None or not math.isfinite(value):
        return "-"
    return f"{value:+.{digits}f}%"


def _trend_label(score: float | None) -> str:
    if score is None:
        return "-"
    if score >= 70:
        return "强上升"
    if score >= 55:
        return "上升"
    if score <= 30:
        return "强下降"
    if score <= 45:
        return "下降"
    return "震荡"


def _clamp_score(value: float) -> float:
    return max(0.0, min(100.0, value))


def _score_trend(rows: list[TradeBucketRow], window: int) -> float | None:
    if len(rows) < 2:
        return None
    use_rows = rows[: max(2, min(window, len(rows)))]
    latest_close = _to_float(use_rows[0].close_price)
    oldest_open = _to_float(use_rows[-1].open_price)
    if latest_close is None or oldest_open is None or oldest_open <= 0:
        return None
    net_ret = (latest_close - oldest_open) / oldest_open
    up_days = 0
    valid_days = 0
    for row in use_rows:
        open_price = _to_float(row.open_price)
        close_price = _to_float(row.close_price)
        if open_price is None or close_price is None or open_price <= 0:
            continue
        valid_days += 1
        if close_price >= open_price:
            up_days += 1
    if valid_days == 0:
        return None
    up_ratio = up_days / valid_days
    score = 50.0 + net_ret * 280.0 + (up_ratio - 0.5) * 30.0
    return round(_clamp_score(score), 0)


def _fmt_lsr_rows(rows: list[dict[str, Any]], tz_name: str | None = None) -> list[str]:
    out: list[str] = []
    for item in rows:
        ts = int(item.get("timestamp") or 0)
        hhmm = (_fmt_hhmm(ts, tz_name)[:2] + ":00") if ts > 0 else "--:--"
        out.append(
            f"{hhmm}: {float(item.get('globalRatio') or 0.0):.2f} | {float(item.get('topAccountRatio') or 0.0):.2f} | {float(item.get('topPositionRatio') or 0.0):.2f}"
        )
    return out

def _fmt_big_usd(v: float | None) -> str:
    if v is None or not math.isfinite(v):
        return "-"
    av = abs(v)
    if av >= 1e12:
        return f"{v / 1e12:.2f}万亿"
    if av >= 1e8:
        return f"{v / 1e8:.2f}亿"
    if av >= 1e4:
        return f"{v / 1e4:.2f}万"
    return f"{v:.2f}"


def _is_symbol_text(text: str) -> bool:
    t = text.strip().upper()
    if not t or t.startswith("/"):
        return False
    return bool(re.fullmatch(r"[A-Z0-9]{2,16}", t))

def _fmt_factor(value: float | None) -> str:
    if value is None or not math.isfinite(value):
        return "-"
    return f"{value:.2f}x"

def _fmt_hhmm(ms: int, tz_name: str | None = None) -> str:
    return datetime.fromtimestamp(ms / 1000, tz=_zoneinfo_or_default(tz_name)).strftime("%H:%M")


def _fmt_mmdd(ms: int, tz_name: str | None = None) -> str:
    return datetime.fromtimestamp(ms / 1000, tz=_zoneinfo_or_default(tz_name)).strftime("%m%d")


def _median(values: list[float]) -> float:
    arr = sorted(values)
    n = len(arr)
    if n == 0:
        return 0.0
    mid = n // 2
    if n % 2 == 1:
        return float(arr[mid])
    return float((arr[mid - 1] + arr[mid]) / 2.0)


def _mark_anomaly_threshold(
    values: list[float | None],
    *,
    z_threshold: float = 3.5,
    min_points: int = 8,
    fallback_abs: float | None = None,
    min_abs: float = 0.0,
) -> list[bool]:
    nums = [float(v) for v in values if v is not None and math.isfinite(v)]
    if len(nums) < max(2, int(min_points)):
        if fallback_abs is None:
            return [False for _ in values]
        return [bool(v is not None and math.isfinite(v) and abs(float(v)) >= fallback_abs) for v in values]

    med = _median(nums)
    deviations = [abs(v - med) for v in nums]
    mad = _median(deviations)

    def _fallback_std_mark(v: float | None) -> bool:
        if v is None or not math.isfinite(v):
            return False
        if abs(float(v)) < float(min_abs):
            return False
        mean = sum(nums) / len(nums)
        var = sum((x - mean) ** 2 for x in nums) / len(nums)
        std = math.sqrt(var)
        if std < 1e-9:
            if fallback_abs is None:
                return False
            return abs(float(v)) >= fallback_abs
        return abs((float(v) - mean) / std) >= 2.0

    if mad < 1e-9:
        return [_fallback_std_mark(v) for v in values]

    marks: list[bool] = []
    for v in values:
        if v is None or not math.isfinite(v):
            marks.append(False)
            continue
        fv = float(v)
        if abs(fv) < float(min_abs):
            marks.append(False)
            continue
        robust_z = 0.6745 * (fv - med) / mad
        marks.append(abs(robust_z) >= float(z_threshold))
    return marks


def _parse_inline_symbol_command(text: str) -> tuple[str, str] | None:
    raw = (text or "").strip()
    matched = re.match(r"^/(nch|ncd|oih|oid)([A-Za-z0-9]{2,16})(?:@[A-Za-z0-9_]+)?$", raw, flags=re.IGNORECASE)
    if not matched:
        return None
    command = str(matched.group(1) or "").lower()
    symbol = _normalize_symbol(matched.group(2))
    if not symbol:
        return None
    return command, symbol

def _parse_limit_arg(args: str | None, default: int, min_value: int, max_value: int) -> int | None:
    if not args or not str(args).strip():
        return int(default)
    raw = str(args).strip()
    try:
        value = int(raw)
    except Exception:
        return None
    if value < int(min_value) or value > int(max_value):
        return None
    return value


def _format_return_rank_lines(items: list[dict[str, Any]]) -> list[str]:
    lines: list[str] = []
    for idx, item in enumerate(items, start=1):
        pct = _to_float(item.get("pct"))
        pct_text = _fmt_signed_pct_value(pct) if pct is not None else "-"
        lines.append(f"{idx}.{_strip_usdt(str(item.get('symbol') or ''))} {pct_text}")
    return lines

def build_query_dispatcher() -> Dispatcher:
    dp = Dispatcher()
    router = Router()
    svc = QueryService()
    tz_state: dict[int, str] = {}

    async def _get_user_tz_name(user_id: int | None) -> str:
        if not user_id:
            return _DEFAULT_QUERY_TZ
        if user_id in tz_state:
            return tz_state[user_id]
        redis: Redis | None = None
        try:
            redis = redis_from_url(settings.redis_url, decode_responses=True)
            key = f"{settings.tg_state_redis_prefix}:query:tz:{int(user_id)}"
            raw = await redis.get(key)
            tz_name = _normalize_tz_name(raw) or _DEFAULT_QUERY_TZ
        except Exception:
            tz_name = _DEFAULT_QUERY_TZ
        finally:
            if redis:
                await redis.close()
        tz_state[int(user_id)] = tz_name
        return tz_name

    async def _set_user_tz_name(user_id: int | None, tz_name: str) -> None:
        if not user_id:
            return
        tz_state[int(user_id)] = tz_name
        redis: Redis | None = None
        try:
            redis = redis_from_url(settings.redis_url, decode_responses=True)
            key = f"{settings.tg_state_redis_prefix}:query:tz:{int(user_id)}"
            await redis.set(key, tz_name)
        except Exception:
            logger.warning("TG query bot save tz failed user_id=%s", user_id)
        finally:
            if redis:
                await redis.close()

    @router.message(Command("start"))
    async def start_cmd(message: Message) -> None:
        await message.reply(
            "你好，我是 CoinMark 查询 Bot。\n"
            "常用命令：\n"
            "/overview\n"
            "/fi1d\n"
            "/fo1d\n"
            "/si1d\n"
            "/so1d\n"
            "\n"
            "兼容命令：/help /price /fund /absorb /anomaly\n"
            "扩展命令：/nchBTC /ncdBTC /oihBTC /oidBTC"
        )

    @router.message(Command("help"))
    async def help_cmd(message: Message) -> None:
        await message.reply(
            "可用命令：\n"
            "/overview - 市场概览\n"
            "/fi1d - 合约当日净流入(最大30)\n"
            "/fo1d - 合约当日净流出\n"
            "/si1d - 现货当日净流入(最大30)\n"
            "/so1d - 现货当日净流出\n"
            "/r15m 20 - 近15分钟涨跌幅(1-60)\n"
            "/r1h 30 - 近1小时涨跌幅(1-120)\n"
            "/bullindex 30 - 多头指数排行(1-120)\n"
            "/openinterest 30 - 持仓增幅排行(1-120)\n"
            "/oicapratio 30 - 持仓/市值排行(1-120)\n"
            "/nchBTC - 盘间资金快照(按个人时区展示)\n"
            "/ncdBTC - 每日净资金(按个人时区展示)\n"
            "/oihBTC - 持仓盘间快照(按个人时区展示)\n"
            "/oidBTC - 近30天持仓(按个人时区展示)\n"
            "/tz - 查看当前时区\n"
            "/tz set Australia/Sydney - 设置时区\n"
            "\n"
            "兼容：/price BTCUSDT /fund BTCUSDT /absorb BTCUSDT /anomaly BTCUSDT"
        )

    @router.message(Command("tz"))
    async def tz_cmd(message: Message, command: CommandObject) -> None:
        user = message.from_user
        user_id = int(user.id) if user else 0
        current_tz = await _get_user_tz_name(user_id)
        args = (command.args or "").strip()
        if not args:
            await message.reply(
                "当前时区："
                f"{current_tz}\n"
                "设置示例：/tz set Australia/Sydney\n"
                "也支持：/tz set UTC 或 /tz set Asia/Shanghai"
            )
            return
        matched = re.match(r"(?i)^set\s+(.+)$", args)
        if not matched:
            await message.reply("用法：/tz 或 /tz set Australia/Sydney")
            return
        target_raw = str(matched.group(1) or "").strip()
        target = _normalize_tz_name(target_raw)
        if not target:
            await message.reply(
                "时区无效，请用 IANA 名称，例如：\n"
                "Australia/Sydney\nAsia/Shanghai\nUTC"
            )
            return
        await _set_user_tz_name(user_id, target)
        now_text = _fmt_ts(int(time.time() * 1000), target)
        await message.reply(f"已设置时区：{target}\n当前本地时间：{now_text}")

    @router.message(Command("nch"))
    async def nch_help_cmd(message: Message) -> None:
        await message.reply("用法：/nchBTC（计算 UTC0，展示按个人时区）")

    @router.message(Command("ncd"))
    async def ncd_help_cmd(message: Message) -> None:
        await message.reply("用法：/ncdBTC（计算 UTC0，展示按个人时区）")

    @router.message(Command("oih"))
    async def oih_help_cmd(message: Message) -> None:
        await message.reply("用法：/oihBTC（计算 UTC0，展示按个人时区）")

    @router.message(Command("oid"))
    async def oid_help_cmd(message: Message) -> None:
        await message.reply("用法：/oidBTC（计算 UTC0，展示按个人时区）")

    @router.message(Command("r15m"))
    async def r15m_cmd(message: Message, command: CommandObject) -> None:
        limit = _parse_limit_arg(command.args, default=20, min_value=1, max_value=60)
        if limit is None:
            await message.reply("用法：/r15m 20（范围 1-60）")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        rank = await svc.return_rank(bucket="15m", limit=limit, market="swap")
        ts = _fmt_ts(rank.get("bucketStartMs"), tz_name)
        gainers = rank.get("gainers") or []
        losers = rank.get("losers") or []
        lines_up = [f"近15分钟涨幅前{limit}（{ts}）"]
        lines_up.extend(_format_return_rank_lines(gainers))
        lines_dn = [f"近15分钟跌幅前{limit}（{ts}）"]
        lines_dn.extend(_format_return_rank_lines(losers))
        await message.reply("\n".join(lines_up))
        await message.reply("\n".join(lines_dn))

    @router.message(Command("r1h"))
    async def r1h_cmd(message: Message, command: CommandObject) -> None:
        limit = _parse_limit_arg(command.args, default=30, min_value=1, max_value=120)
        if limit is None:
            await message.reply("用法：/r1h 30（范围 1-120）")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        rank = await svc.return_rank(bucket="1h", limit=limit, market="swap")
        ts = _fmt_ts(rank.get("bucketStartMs"), tz_name)
        gainers = rank.get("gainers") or []
        losers = rank.get("losers") or []
        lines_up = [f"近1小时涨幅前{limit}（{ts}）"]
        lines_up.extend(_format_return_rank_lines(gainers))
        lines_dn = [f"近1小时跌幅前{limit}（{ts}）"]
        lines_dn.extend(_format_return_rank_lines(losers))
        await message.reply("\n".join(lines_up))
        await message.reply("\n".join(lines_dn))

    @router.message(Command("bullindex"))
    async def bullindex_cmd(message: Message, command: CommandObject) -> None:
        limit = _parse_limit_arg(command.args, default=30, min_value=1, max_value=120)
        if limit is None:
            await message.reply("用法：/bullindex 30（范围 1-120）")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        rank = await svc.bullindex_rank(limit=limit, market="swap")
        items = rank.get("items") or []
        if not items:
            await message.reply("/bullindex 暂无数据")
            return
        ts = _fmt_ts(rank.get("bucketStartMs"), tz_name)
        lines = [f"多头指数前{limit}（{ts}）"]
        for idx, item in enumerate(items, start=1):
            lines.append(
                f"{idx}.{_strip_usdt(str(item.get('symbol') or ''))} {float(item.get('score') or 0.0):.1f}分 | {_fmt_signed_pct_value(_to_float(item.get('retPct')))} | {_fmt_signed_pct_value(_to_float(item.get('flowBiasPct')))}"
            )
        await message.reply("\n".join(lines))

    @router.message(Command("openinterest"))
    async def openinterest_cmd(message: Message, command: CommandObject) -> None:
        limit = _parse_limit_arg(command.args, default=30, min_value=1, max_value=120)
        if limit is None:
            await message.reply("用法：/openinterest 30（范围 1-120）")
            return
        items = await svc.openinterest_growth_rank(limit=limit)
        if not items:
            await message.reply("/openinterest 暂无数据")
            return
        lines = [f"持仓增幅前{limit}（1d）"]
        for idx, item in enumerate(items, start=1):
            lines.append(
                f"{idx}.{_strip_usdt(str(item.get('symbol') or ''))} {_fmt_signed_pct_value(_to_float(item.get('changePct')))} | {_fmt_big_usd(_to_float(item.get('oiNotionalUsd')))}U"
            )
        await message.reply("\n".join(lines))

    @router.message(Command("oicapratio"))
    async def oicapratio_cmd(message: Message, command: CommandObject) -> None:
        limit = _parse_limit_arg(command.args, default=30, min_value=1, max_value=120)
        if limit is None:
            await message.reply("用法：/oicapratio 30（范围 1-120）")
            return
        items = await svc.oicapratio_rank(limit=limit)
        if not items:
            await message.reply("/oicapratio 暂无数据")
            return
        lines = [f"持仓/市值比例前{limit}"]
        for idx, item in enumerate(items, start=1):
            lines.append(
                f"{idx}.{_strip_usdt(str(item.get('symbol') or ''))} {_fmt_signed_pct_value(_to_float(item.get('ratioPct')))} | {_fmt_big_usd(_to_float(item.get('oiNotionalUsd')))}U / {_fmt_big_usd(_to_float(item.get('marketCapUsd')))}U"
            )
        await message.reply("\n".join(lines))

    @router.message(Command("hotmarket"))
    async def hotmarket_cmd(message: Message) -> None:
        await message.reply("/hotmarket 功能正在接入中，先用 /overview")

    @router.message(Command("suit"))
    async def suit_cmd(message: Message) -> None:
        await message.reply("/suit 功能正在接入中")

    @router.message(Command("fundrate"))
    async def fundrate_cmd(message: Message) -> None:
        await message.reply("/fundrate 功能正在接入中")

    @router.message(Command("showtime"))
    async def showtime_cmd(message: Message) -> None:
        await message.reply("/showtime 功能正在接入中")

    @router.message(Command("awake"))
    async def awake_cmd(message: Message) -> None:
        await message.reply("/awake 功能正在接入中")

    @router.message(Command("myfav"))
    async def myfav_cmd(message: Message) -> None:
        await message.reply("/myfav 功能正在接入中")

    @router.message(Command("settings"))
    async def settings_cmd(message: Message) -> None:
        await message.reply("/settings 功能正在接入中")

    @router.message(Command("overview"))
    async def overview_cmd(message: Message) -> None:
        data = await svc.market_overview()
        sentiment = data.get("sentiment") or {}
        major = data.get("majorNetFlow") or {}
        btc = major.get("BTC") or {}
        eth = major.get("ETH") or {}
        sol = major.get("SOL") or {}
        lines = [
            f"市场概览 ({int(data.get('gainersCount') or 0)}涨,{int(data.get('losersCount') or 0)}跌)",
            "",
            "#情绪指数 | VIX | DXY | US10Y",
            f"{sentiment.get('index', '-')} | {sentiment.get('vix', '-')} | {sentiment.get('dxy', '-')} | {sentiment.get('us10y', '-')}",
            "#大盘净资金",
            f"BTC {_fmt_compact(_to_float(btc.get('swap')))} | {_fmt_compact(_to_float(btc.get('spot')))}",
            f"ETH {_fmt_compact(_to_float(eth.get('swap')))} | {_fmt_compact(_to_float(eth.get('spot')))}",
            f"SOL {_fmt_compact(_to_float(sol.get('swap')))} | {_fmt_compact(_to_float(sol.get('spot')))}",
            "#今日最活跃前10",
            _fmt_symbol_items(data.get("topActive") or [], limit=10),
            "#今日持仓增幅前10",
            _fmt_oi_growth_items(data.get("topOiGrowth") or [], limit=10),
            "#今日涨幅前10",
            _fmt_pct_items(data.get("topGainers") or [], limit=10),
            "#今日跌幅前10",
            _fmt_pct_items(data.get("topLosers") or [], limit=10),
        ]
        await message.reply("\n".join(lines))

    @router.message(Command("fi1d"))
    async def fi1d_cmd(message: Message) -> None:
        rank = await svc.one_day_net_rank(market="swap", direction="in", limit=30)
        lines = [f"{int(rank.get('count') or 0)}个合约1日净流入排行", ""]
        lines.extend(_fmt_flow_rank(rank.get("items") or []))
        await message.reply("\n".join(lines))

    @router.message(Command("fo1d"))
    async def fo1d_cmd(message: Message) -> None:
        rank = await svc.one_day_net_rank(market="swap", direction="out", limit=30)
        lines = [f"{int(rank.get('count') or 0)}个合约1日净流出排行", ""]
        lines.extend(_fmt_flow_rank(rank.get("items") or []))
        await message.reply("\n".join(lines))

    @router.message(Command("si1d"))
    async def si1d_cmd(message: Message) -> None:
        rank = await svc.one_day_net_rank(market="spot", direction="in", limit=30)
        lines = [f"{int(rank.get('count') or 0)}个现货1日净流入排行", ""]
        lines.extend(_fmt_flow_rank(rank.get("items") or []))
        await message.reply("\n".join(lines))

    @router.message(Command("so1d"))
    async def so1d_cmd(message: Message) -> None:
        rank = await svc.one_day_net_rank(market="spot", direction="out", limit=30)
        lines = [f"{int(rank.get('count') or 0)}个现货1日净流出排行", ""]
        lines.extend(_fmt_flow_rank(rank.get("items") or []))
        await message.reply("\n".join(lines))

    @router.message(Command("price"))
    async def price_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/price BTCUSDT")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        data = await svc.latest_price(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"没找到 {symbol} 的最新价格数据")
            return
        await message.reply(
            f"【价格】{symbol}\n"
            f"最新价：{data['closePrice'] if data['closePrice'] is not None else '-'}\n"
            f"1m净流：{_fmt_compact(data['netFlow'])}\n"
            f"成交额：{_fmt_compact(data['quoteNotional'])}\n"
            f"时间：{_fmt_ts(int(data['ts']), tz_name)}"
        )

    @router.message(Command("fund"))
    async def fund_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/fund BTCUSDT")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        data = await svc.fund_snapshot(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"没找到 {symbol} 的资金快照")
            return
        await message.reply(
            f"【资金】{symbol}\n"
            f"近1h净流：{_fmt_compact(data['net1h'])}\n"
            f"近24h累计：{_fmt_compact(data['net24h'])}\n"
            f"时间：{_fmt_ts(int(data['ts']), tz_name)}"
        )

    @router.message(Command("absorb"))
    async def absorb_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/absorb BTCUSDT")
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        data = await svc.absorption(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"没找到 {symbol} 的吸筹信号")
            return
        reasons = "；".join([str(item) for item in data.get("reasons", [])[:3]]) or "-"
        await message.reply(
            f"【吸筹】{symbol}\n"
            f"状态：{data['state']}\n"
            f"分数：{int(round(float(data['score'])))}\n"
            f"依据：{reasons}\n"
            f"时间：{_fmt_ts(int(data['ts']), tz_name)}"
        )

    @router.message(Command("anomaly"))
    async def anomaly_cmd(message: Message, command: CommandObject) -> None:
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        symbol = _normalize_symbol(command.args)
        rows = await svc.latest_anomalies(symbol=symbol, market=settings.tg_notify_market, limit=5)
        if not rows:
            await message.reply("最近没有异动事件")
            return
        lines: list[str] = [f"【异动】{symbol or settings.tg_notify_market.upper()} 最近5条"]
        for row in rows:
            details = row.details if isinstance(row.details, dict) else {}
            score = _event_severity_score(str(row.event_type), details)
            lines.append(
                f"- {row.symbol} {_event_type_label(str(row.event_type))} | {_event_level(score)} {score:.1f} | {_fmt_ts(int(row.event_time_ms), tz_name)}"
            )
        await message.reply("\n".join(lines))

    @router.message(F.text.regexp(r"(?i)^/(nch|ncd|oih|oid)[a-z0-9]{2,16}(?:@[a-z0-9_]+)?$"))
    async def slash_inline_cmd(message: Message) -> None:
        text = (message.text or "").strip()
        parsed = _parse_inline_symbol_command(text)
        if not parsed:
            return

        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)

        cmd, symbol = parsed
        pairs = set(await get_pairs("swap"))
        if symbol not in pairs:
            await message.reply("币种不存在或未在合约市场交易")
            return

        base = _strip_usdt(symbol)

        if cmd == "nch":
            rows = await svc.intraday_net_snapshot(symbol=symbol, hours=6)
            if not rows:
                await message.reply(f"{base} 盘间资金暂无数据")
                return
            swap_marks = _mark_anomaly_threshold(
                [_to_float(item.get("swap")) for item in rows],
                z_threshold=3.0,
                min_points=6,
                fallback_abs=5_000_000.0,
                min_abs=1_000_000.0,
            )
            spot_marks = _mark_anomaly_threshold(
                [_to_float(item.get("spot")) for item in rows],
                z_threshold=3.0,
                min_points=6,
                fallback_abs=5_000_000.0,
                min_abs=1_000_000.0,
            )

            lines = [f"{base}盘间资金快照 (合约 | 现货)", "* 为阈值异常", ""]
            for idx, item in enumerate(rows):
                ts = int(item.get("ts") or 0)
                swap_text = _fmt_compact(_to_float(item.get("swap"))) + ("*" if swap_marks[idx] else "")
                spot_text = _fmt_compact(_to_float(item.get("spot"))) + ("*" if spot_marks[idx] else "")
                lines.append(f"{_fmt_hhmm(ts, tz_name)} {swap_text} | {spot_text}")
            await message.reply("\n".join(lines))
            return

        if cmd == "ncd":
            rows = await svc.daily_net_series(symbol=symbol, days=30)
            if not rows:
                await message.reply(f"{base} 每日净资金暂无数据")
                return
            swap_marks = _mark_anomaly_threshold(
                [_to_float(item.get("swap")) for item in rows],
                z_threshold=3.2,
                min_points=10,
                fallback_abs=20_000_000.0,
                min_abs=5_000_000.0,
            )
            spot_marks = _mark_anomaly_threshold(
                [_to_float(item.get("spot")) for item in rows],
                z_threshold=3.2,
                min_points=10,
                fallback_abs=20_000_000.0,
                min_abs=5_000_000.0,
            )

            lines = [f"{base}每日净资金 (合约 | 现货)", "* 为阈值异常", ""]
            for idx, item in enumerate(rows):
                ts = int(item.get("ts") or 0)
                swap_text = _fmt_compact(_to_float(item.get("swap"))) + ("*" if swap_marks[idx] else "")
                spot_text = _fmt_compact(_to_float(item.get("spot"))) + ("*" if spot_marks[idx] else "")
                lines.append(f"{_fmt_mmdd(ts, tz_name)} {swap_text} | {spot_text}")
            await message.reply("\n".join(lines))
            return

        if cmd == "oih":
            rows = await svc.oi_hourly_snapshot(symbol=symbol, hours=24)
            if not rows:
                await message.reply(f"{base} 持仓快照暂无数据")
                return
            pct_marks = _mark_anomaly_threshold(
                [_to_float(item.get("changePct")) for item in rows],
                z_threshold=3.0,
                min_points=8,
                fallback_abs=3.0,
                min_abs=0.5,
            )

            lines = [f"{base}持仓快照 (较开盘时的变化)", "* 为阈值异常", ""]
            for idx, item in enumerate(rows):
                ts = int(item.get("ts") or 0)
                oi_qty = _to_float(item.get("oiQty"))
                oi_val = _to_float(item.get("oiValue"))
                pct = _to_float(item.get("changePct"))
                star = "*" if pct_marks[idx] else ""
                pct_text = f" ({_fmt_signed_pct_value(pct).replace('+', '')})" if pct is not None else ""
                lines.append(f"{_fmt_hhmm(ts, tz_name)} {_fmt_compact(oi_qty)}{star} ({_fmt_big_usd(oi_val)}U){pct_text}")
            await message.reply("\n".join(lines))
            return

        if cmd == "oid":
            rows = await svc.oi_daily_series(symbol=symbol, days=30)
            if not rows:
                await message.reply(f"{base} 近30天持仓暂无数据")
                return
            pct_marks = _mark_anomaly_threshold(
                [_to_float(item.get("changePct")) for item in rows],
                z_threshold=3.2,
                min_points=10,
                fallback_abs=3.0,
                min_abs=0.5,
            )

            lines = [f"{base}近30天持仓数据 (较前一天的变化)", "* 为阈值异常", ""]
            for idx, item in enumerate(rows):
                ts = int(item.get("ts") or 0)
                oi_qty = _to_float(item.get("oiQty"))
                oi_val = _to_float(item.get("oiValue"))
                pct = _to_float(item.get("changePct"))
                star = "*" if pct_marks[idx] else ""
                pct_text = f" {_fmt_signed_pct_value(pct).replace('+', '')}" if pct is not None else ""
                lines.append(f"{_fmt_mmdd(ts, tz_name)} {_fmt_compact(oi_qty)}{star}{pct_text} ({_fmt_big_usd(oi_val)}U)")
            await message.reply("\n".join(lines))
            return

    @router.message(F.text)
    async def symbol_text_cmd(message: Message) -> None:
        text = (message.text or "").strip()
        if not _is_symbol_text(text):
            return
        user = message.from_user
        tz_name = await _get_user_tz_name(int(user.id) if user else 0)
        symbol = _normalize_symbol(text)
        if not symbol:
            return

        pairs = set(await get_pairs("swap"))
        if symbol not in pairs:
            return

        brief, lsr_rows, fund_rows = await asyncio.gather(
            svc.symbol_brief(symbol),
            svc.symbol_long_short_rows(symbol, limit=5),
            svc.symbol_fund_windows(symbol, [1, 2, 3, 5, 7, 10, 15, 20, 25, 30]),
        )

        base = _strip_usdt(symbol)
        lines: list[str] = [
            f"#{base}",
            "",
            f"当前价格 {brief.get('price') if brief.get('price') is not None else '-'} ({_fmt_signed_pct_value(brief.get('priceChangePct'))})",
            f"价格区间 {brief.get('rangeLabel', '-')} (6小时)",
            f"振幅量能 {_fmt_signed_pct_value(brief.get('amp6hPct')).replace('+', '')} , {_fmt_factor(_to_float(brief.get('volumeFactor')))}",
            f"今日波动 {int(brief.get('waveCount') or 0)}次",
            f"摸顶探底 {brief.get('touchText', '-')}",
            f"昨日涨幅 {_fmt_signed_pct_value(brief.get('yesterdayPct'))}",
            f"上周涨幅 {_fmt_signed_pct_value(brief.get('lastWeekPct'))}",
            f"本周涨幅 {_fmt_signed_pct_value(brief.get('thisWeekPct'))}",
            f"六日趋势 {int(brief.get('trend6')) if brief.get('trend6') is not None else '-'}分 ({_trend_label(brief.get('trend6'))})",
            f"六十趋势 {int(brief.get('trend60')) if brief.get('trend60') is not None else '-'}分 ({_trend_label(brief.get('trend60'))})",
            f"持仓数量 {_fmt_compact(_to_float(brief.get('oiQty')))}币 ({_fmt_signed_pct_value(brief.get('oiQtyChangePct'))})",
            f"持仓价值 {_fmt_big_usd(_to_float(brief.get('oiValue')))}U ({_fmt_signed_pct_value(brief.get('oiValueChangePct'))})",
            f"头寸规模 {_fmt_signed_pct_value(brief.get('positionScalePct')).replace('+', '')} (持仓:市值)",
            f"资金费率 {_fmt_signed_pct_value((_to_float(brief.get('fundingRate')) or 0.0) * 100.0, digits=4) if brief.get('fundingRate') is not None else '-'}",
            f"流通市值 {_fmt_big_usd(_to_float(brief.get('marketCap')))} ({_fmt_signed_pct_value(brief.get('capDominancePct')).replace('+', '')})",
            f"稀释市值 {_fmt_big_usd(_to_float(brief.get('fdv')))}",
            "",
            f"#{base}多空比|大户数|大户持仓",
        ]

        lsr_lines = _fmt_lsr_rows(lsr_rows, tz_name)
        if lsr_lines:
            lines.extend(lsr_lines)
        else:
            lines.append("-")

        lines.append("")
        lines.append(f"/{base} 合约资金 | 现货资金")
        for item in fund_rows:
            day = int(item.get("day") or 0)
            swap_net = _to_float(item.get("swap"))
            spot_net = _to_float(item.get("spot"))
            lines.append(f"{day:02d}D {_fmt_compact(swap_net)} | {_fmt_compact(spot_net)}")

        lines.extend(
            [
                "",
                f"/nch{base} 资金盘间快照",
                f"资金每日流向 /ncd{base}",
                f"/oih{base} 持仓盘间快照",
                f"持仓近日变化 /oid{base}",
            ]
        )
        await message.reply("\n".join(lines))

    dp.include_router(router)
    return dp


def _query_bot_menu_commands() -> list[BotCommand]:
    return [
        BotCommand(command="overview", description="市场概览"),
        BotCommand(command="fi1d", description="合约当日净流入(最大30)"),
        BotCommand(command="fo1d", description="合约当日净流出"),
        BotCommand(command="si1d", description="现货当日净流入(最大30)"),
        BotCommand(command="so1d", description="现货当日净流出"),
        BotCommand(command="r15m", description="近15分钟涨跌幅(1-60)"),
        BotCommand(command="r1h", description="近1小时涨跌幅(1-120)"),
        BotCommand(command="bullindex", description="多头指数排行"),
        BotCommand(command="openinterest", description="持仓增幅排行"),
        BotCommand(command="oicapratio", description="持仓与市值比例排行"),
        BotCommand(command="hotmarket", description="近期多空热点"),
        BotCommand(command="suit", description="多空量能排行"),
        BotCommand(command="fundrate", description="资金费排行"),
        BotCommand(command="showtime", description="波动次数排行"),
        BotCommand(command="awake", description="冬眠苏醒"),
        BotCommand(command="myfav", description="收藏"),
        BotCommand(command="settings", description="黑名单"),
        BotCommand(command="nch", description="资金盘间快照(示例:/nchBTC)"),
        BotCommand(command="ncd", description="资金每日流向(示例:/ncdBTC)"),
        BotCommand(command="oih", description="持仓盘间快照(示例:/oihBTC)"),
        BotCommand(command="oid", description="持仓近日变化(示例:/oidBTC)"),
        BotCommand(command="tz", description="设置时区(示例:/tz set Australia/Sydney)"),
        BotCommand(command="help", description="使用帮助"),
    ]


async def run_query_bot(token: str) -> None:
    bot = Bot(token=token)
    dp = build_query_dispatcher()
    try:
        await bot.set_my_commands(_query_bot_menu_commands(), scope=BotCommandScopeAllPrivateChats())
        await bot.set_chat_menu_button(menu_button=MenuButtonCommands())
    except Exception:
        logger.exception("TG query bot menu init failed")
    logger.info("TG 查询 Bot 已启动")
    try:
        await dp.start_polling(bot, allowed_updates=dp.resolve_used_update_types(), polling_timeout=max(10, int(settings.tg_query_poll_timeout_sec)))
    finally:
        await bot.session.close()


async def run_notify_bot(token: str) -> None:
    bot = Bot(token=token)
    redis: Redis | None = None
    try:
        try:
            redis = redis_from_url(settings.redis_url, decode_responses=True)
            await redis.ping()
        except Exception:
            logger.warning("TG 通知 Bot 无法连接 Redis，将使用进程内状态")
            redis = None

        notifier = TgAnomalyNotifier(bot=bot, redis=redis)
        await notifier.run()
    finally:
        if redis:
            await redis.close()
        await bot.session.close()


async def main_async() -> None:
    if not settings.tg_enabled:
        logger.info("TG 功能未启用（TG_ENABLED=false）")
        while True:
            await asyncio.sleep(3600)

    tasks: list[asyncio.Task] = []
    notify_token = (settings.tg_notify_bot_token or "").strip()
    query_token = (settings.tg_query_bot_token or "").strip()
    notify_chat_id = (settings.tg_notify_chat_id or "").strip()

    if notify_token and notify_chat_id:
        tasks.append(asyncio.create_task(run_notify_bot(notify_token), name="tg-notify-bot"))
    else:
        logger.warning("TG 通知 Bot 未启动：缺少 TG_NOTIFY_BOT_TOKEN 或 TG_NOTIFY_CHAT_ID")

    if query_token:
        tasks.append(asyncio.create_task(run_query_bot(query_token), name="tg-query-bot"))
    else:
        logger.warning("TG 查询 Bot 未启动：缺少 TG_QUERY_BOT_TOKEN")

    if not tasks:
        logger.warning("TG 无可启动 Bot，退出")
        return

    await asyncio.gather(*tasks)


def main() -> None:
    asyncio.run(main_async())


if __name__ == "__main__":
    main()
