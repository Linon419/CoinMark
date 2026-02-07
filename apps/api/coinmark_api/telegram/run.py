from __future__ import annotations

import asyncio
import logging
import math
import time
from typing import Any

from aiogram import Bot, Dispatcher, Router
from aiogram.filters import Command, CommandObject
from aiogram.types import Message
from redis.asyncio import Redis
from redis.asyncio import from_url as redis_from_url
from sqlalchemy import and_, desc, func, select

from coinmark_api.config import settings
from coinmark_api.db import SessionLocal
from coinmark_api.models import AbsorptionSignalSnapshot, AnomalyEvent, TradeBucket


logging.basicConfig(level=getattr(logging, settings.api_log_level.upper(), logging.INFO))
logger = logging.getLogger("coinmark.telegram")


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


def _fmt_ts(ms: int | None) -> str:
    if not ms:
        return "-"
    return time.strftime("%m-%d %H:%M", time.localtime(ms / 1000))


def _event_type_label(event_type: str) -> str:
    mapping = {
        "breakout_up": "向上突破",
        "breakout_down": "向下跌破",
        "volume_spike": "量能放大",
        "amplitude_spike": "振幅放大",
    }
    return mapping.get((event_type or "").lower(), event_type)


def _event_base_score(event_type: str) -> float:
    t = (event_type or "").lower()
    if t in {"breakout_up", "breakout_down"}:
        return 60.0
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
        async with SessionLocal() as session:
            stmt = (
                select(TradeBucket)
                .where(
                    and_(
                        TradeBucket.market == market,
                        TradeBucket.symbol == symbol,
                        TradeBucket.bucket == "1m",
                    )
                )
                .order_by(TradeBucket.bucket_start_ms.desc())
                .limit(1)
            )
            row = (await session.execute(stmt)).scalars().first()
        if not row:
            return None
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
        async with SessionLocal() as session:
            stmt = (
                select(TradeBucket)
                .where(
                    and_(
                        TradeBucket.market == market,
                        TradeBucket.symbol == symbol,
                        TradeBucket.bucket == "1h",
                    )
                )
                .order_by(TradeBucket.bucket_start_ms.desc())
                .limit(24)
            )
            rows = (await session.execute(stmt)).scalars().all()
        if not rows:
            return None
        latest = rows[0]
        net_1h = (_to_float(latest.taker_buy_notional) or 0.0) - (_to_float(latest.taker_sell_notional) or 0.0)
        net_24h = 0.0
        for r in rows:
            net_24h += (_to_float(r.taker_buy_notional) or 0.0) - (_to_float(r.taker_sell_notional) or 0.0)
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
        async with SessionLocal() as session:
            stmt = select(AnomalyEvent).where(AnomalyEvent.market == market)
            if symbol:
                stmt = stmt.where(AnomalyEvent.symbol == symbol)
            stmt = stmt.order_by(desc(AnomalyEvent.event_time_ms)).limit(limit)
            rows = (await session.execute(stmt)).scalars().all()
        return rows


def _normalize_symbol(raw: str | None) -> str | None:
    if not raw:
        return None
    s = raw.strip().upper()
    if not s:
        return None
    if not s.endswith("USDT"):
        s = f"{s}USDT"
    return s


def build_query_dispatcher() -> Dispatcher:
    dp = Dispatcher()
    router = Router()
    svc = QueryService()

    @router.message(Command("start"))
    async def start_cmd(message: Message) -> None:
        await message.reply(
            "你好，我是 CoinMark 查询 Bot。\n"
            "命令：\n"
            "/help\n"
            "/price BTCUSDT\n"
            "/fund BTCUSDT\n"
            "/absorb BTCUSDT\n"
            "/anomaly BTCUSDT"
        )

    @router.message(Command("help"))
    async def help_cmd(message: Message) -> None:
        await message.reply(
            "可用命令：\n"
            "/price BTCUSDT - 最新价格与1m净流\n"
            "/fund BTCUSDT - 1h净流与24h累计\n"
            "/absorb BTCUSDT - 最新吸筹状态\n"
            "/anomaly BTCUSDT - 最近异动\n"
            "不带 USDT 也可以，例如 /price BTC"
        )

    @router.message(Command("price"))
    async def price_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/price BTCUSDT")
            return
        data = await svc.latest_price(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"未找到 {symbol} 的最新价格数据")
            return
        await message.reply(
            f"【价格】{symbol}\n"
            f"最新价：{data['closePrice'] if data['closePrice'] is not None else '-'}\n"
            f"1m净流：{_fmt_compact(data['netFlow'])}\n"
            f"成交额：{_fmt_compact(data['quoteNotional'])}\n"
            f"时间：{_fmt_ts(int(data['ts']))}"
        )

    @router.message(Command("fund"))
    async def fund_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/fund BTCUSDT")
            return
        data = await svc.fund_snapshot(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"未找到 {symbol} 的资金快照")
            return
        await message.reply(
            f"【资金】{symbol}\n"
            f"近1h净流：{_fmt_compact(data['net1h'])}\n"
            f"近24h累计：{_fmt_compact(data['net24h'])}\n"
            f"时间：{_fmt_ts(int(data['ts']))}"
        )

    @router.message(Command("absorb"))
    async def absorb_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        if not symbol:
            await message.reply("用法：/absorb BTCUSDT")
            return
        data = await svc.absorption(symbol=symbol, market=settings.tg_notify_market)
        if not data:
            await message.reply(f"未找到 {symbol} 的吸筹信号")
            return
        reasons = "；".join([str(x) for x in data.get("reasons", [])[:3]]) or "-"
        await message.reply(
            f"【吸筹】{symbol}\n"
            f"状态：{data['state']}\n"
            f"分数：{int(round(float(data['score'])))}\n"
            f"依据：{reasons}\n"
            f"时间：{_fmt_ts(int(data['ts']))}"
        )

    @router.message(Command("anomaly"))
    async def anomaly_cmd(message: Message, command: CommandObject) -> None:
        symbol = _normalize_symbol(command.args)
        rows = await svc.latest_anomalies(symbol=symbol, market=settings.tg_notify_market, limit=5)
        if not rows:
            await message.reply("最近没有异动事件")
            return
        lines: list[str] = [f"【异动】{symbol or settings.tg_notify_market.upper()} 最近5条"]
        for r in rows:
            details = r.details if isinstance(r.details, dict) else {}
            score = _event_severity_score(str(r.event_type), details)
            lines.append(
                f"- {r.symbol} {_event_type_label(str(r.event_type))} | {_event_level(score)} {score:.1f} | {_fmt_ts(int(r.event_time_ms))}"
            )
        await message.reply("\n".join(lines))

    dp.include_router(router)
    return dp


async def run_query_bot(token: str) -> None:
    bot = Bot(token=token)
    dp = build_query_dispatcher()
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
