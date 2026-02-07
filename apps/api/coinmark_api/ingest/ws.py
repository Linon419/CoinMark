from __future__ import annotations

import asyncio
import json
import logging
import random
from decimal import Decimal, InvalidOperation
from typing import Any
from urllib.parse import quote

import websockets

from coinmark_api.config import settings
from coinmark_api.ingest.aggregator import TradeAggregator
from coinmark_api.ingest.orderbook_aggregator import OrderbookAggregator


logger = logging.getLogger("coinmark.ingest")


_trade_msg_count = 0
_depth_msg_count = 0


def reset_runtime_counters() -> tuple[int, int]:
    global _trade_msg_count, _depth_msg_count
    trade, depth = _trade_msg_count, _depth_msg_count
    _trade_msg_count = 0
    _depth_msg_count = 0
    return trade, depth


def _ws_base(market: str) -> str:
    if market == "spot":
        return "wss://stream.binance.com:9443/stream"
    return "wss://fstream.binance.com/stream"


def _chunk(items: list[str], size: int) -> list[list[str]]:
    if size <= 0:
        return [items]
    return [items[i : i + size] for i in range(0, len(items), size)]


def _safe_decimal(value: Any) -> Decimal | None:
    try:
        return Decimal(str(value))
    except (InvalidOperation, TypeError):
        return None


async def _run_trade_conn(market: str, streams: list[str], trade_aggregator: TradeAggregator, orderbook_aggregator: OrderbookAggregator) -> None:
    global _trade_msg_count
    encoded = quote("/".join(streams), safe="/@")
    url = _ws_base(market) + "?streams=" + encoded
    backoff = 1.0
    last_id: dict[str, int] = {}

    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, close_timeout=5) as ws:
                backoff = 1.0
                async for msg in ws:
                    try:
                        payload = json.loads(msg)
                        data = payload.get("data") if isinstance(payload, dict) else None
                        if not isinstance(data, dict):
                            continue

                        symbol = data.get("s")
                        if not symbol:
                            continue

                        agg_id = data.get("a")
                        if isinstance(agg_id, int):
                            prev = last_id.get(symbol)
                            if prev is not None and agg_id <= prev:
                                continue
                            last_id[symbol] = agg_id

                        ts_ms = int(data.get("T") or 0)
                        if ts_ms <= 0:
                            continue

                        price = _safe_decimal(data.get("p"))
                        qty = _safe_decimal(data.get("q"))
                        if price is None or qty is None:
                            continue
                        notional = price * qty

                        is_buyer_maker = bool(data.get("m"))
                        if is_buyer_maker:
                            taker_buy_notional = Decimal("0")
                            taker_sell_notional = notional
                        else:
                            taker_buy_notional = notional
                            taker_sell_notional = Decimal("0")

                        await trade_aggregator.add_trade(
                            market=market,
                            symbol=str(symbol),
                            ts_ms=ts_ms,
                            price=price,
                            taker_buy_notional=taker_buy_notional,
                            taker_sell_notional=taker_sell_notional,
                            quote_notional=notional,
                            trade_count=1,
                        )
                        await orderbook_aggregator.add_trade(
                            market=market,
                            symbol=str(symbol),
                            ts_ms=ts_ms,
                            taker_buy_notional=taker_buy_notional,
                            taker_sell_notional=taker_sell_notional,
                        )
                        _trade_msg_count += 1
                    except Exception:
                        continue
        except Exception as e:
            jitter = random.random() * 0.3
            sleep_s = min(30.0, backoff) + jitter
            logger.warning("TradeWS(%s) 杩炴帴澶辫触锛屽皢鍦?%.1fs 鍚庨噸杩烇細%s", market, sleep_s, e)
            await asyncio.sleep(sleep_s)
            backoff *= 2


def build_depth_features(payload: dict[str, Any]) -> tuple[Decimal, Decimal, Decimal, Decimal, Decimal] | None:
    bids = payload.get("b") or []
    asks = payload.get("a") or []
    if not bids or not asks:
        return None

    if len(bids[0]) < 2 or len(asks[0]) < 2:
        return None

    bid1_price = _safe_decimal(bids[0][0])
    bid1_qty = _safe_decimal(bids[0][1])
    ask1_price = _safe_decimal(asks[0][0])
    ask1_qty = _safe_decimal(asks[0][1])
    if bid1_price is None or bid1_qty is None or ask1_price is None or ask1_qty is None:
        return None

    mid = (bid1_price + ask1_price) / Decimal("2")
    if mid <= 0:
        return None

    spread_bps = (ask1_price - bid1_price) / mid * Decimal("10000")

    bid_notional_l5 = Decimal("0")
    ask_notional_l5 = Decimal("0")
    max_bid_notional_l5 = Decimal("0")
    max_ask_notional_l5 = Decimal("0")

    for level in bids[:5]:
        if len(level) < 2:
            continue
        p = _safe_decimal(level[0])
        q = _safe_decimal(level[1])
        if p is None or q is None:
            continue
        notional = p * q
        bid_notional_l5 += notional
        if notional > max_bid_notional_l5:
            max_bid_notional_l5 = notional

    for level in asks[:5]:
        if len(level) < 2:
            continue
        p = _safe_decimal(level[0])
        q = _safe_decimal(level[1])
        if p is None or q is None:
            continue
        notional = p * q
        ask_notional_l5 += notional
        if notional > max_ask_notional_l5:
            max_ask_notional_l5 = notional

    depth_denom = bid_notional_l5 + ask_notional_l5
    depth_imbalance_l5 = Decimal("0") if depth_denom <= 0 else (bid_notional_l5 - ask_notional_l5) / depth_denom

    micro_denom = bid1_qty + ask1_qty
    if micro_denom <= 0:
        microprice_shift_bps = Decimal("0")
    else:
        microprice = (ask1_price * bid1_qty + bid1_price * ask1_qty) / micro_denom
        microprice_shift_bps = (microprice - mid) / mid * Decimal("10000")

    wall_denom = max_bid_notional_l5 + max_ask_notional_l5
    wall_pressure_l5 = Decimal("0") if wall_denom <= 0 else (max_bid_notional_l5 - max_ask_notional_l5) / wall_denom

    l1_depth_notional = bid1_price * bid1_qty + ask1_price * ask1_qty
    return spread_bps, depth_imbalance_l5, microprice_shift_bps, wall_pressure_l5, l1_depth_notional


async def _run_depth_conn(market: str, streams: list[str], orderbook_aggregator: OrderbookAggregator) -> None:
    global _depth_msg_count
    encoded = quote("/".join(streams), safe="/@")
    url = _ws_base(market) + "?streams=" + encoded
    backoff = 1.0

    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, close_timeout=5) as ws:
                backoff = 1.0
                async for msg in ws:
                    try:
                        payload = json.loads(msg)
                        data = payload.get("data") if isinstance(payload, dict) else None
                        if not isinstance(data, dict):
                            continue

                        symbol = data.get("s")
                        ts_ms = int(data.get("E") or 0)
                        if not symbol or ts_ms <= 0:
                            continue

                        features = build_depth_features(data)
                        if features is None:
                            continue

                        spread_bps, depth_imbalance_l5, microprice_shift_bps, wall_pressure_l5, l1_depth_notional = features
                        await orderbook_aggregator.add_orderbook_sample(
                            market=market,
                            symbol=str(symbol),
                            ts_ms=ts_ms,
                            spread_bps=spread_bps,
                            depth_imbalance_l5=depth_imbalance_l5,
                            microprice_shift_bps=microprice_shift_bps,
                            wall_pressure_l5=wall_pressure_l5,
                            l1_depth_notional=l1_depth_notional,
                        )
                        _depth_msg_count += 1
                    except Exception:
                        continue
        except Exception as e:
            jitter = random.random() * 0.3
            sleep_s = min(30.0, backoff) + jitter
            logger.warning("DepthWS(%s) 杩炴帴澶辫触锛屽皢鍦?%.1fs 鍚庨噸杩烇細%s", market, sleep_s, e)
            await asyncio.sleep(sleep_s)
            backoff *= 2


async def run_trade_ws_ingest(market: str, symbols: list[str], trade_aggregator: TradeAggregator, orderbook_aggregator: OrderbookAggregator) -> None:
    streams = [f"{symbol.lower()}@aggTrade" for symbol in symbols]
    chunks = _chunk(streams, settings.ingest_streams_per_conn)
    logger.info("TradeWS(%s) streams=%d conn=%d", market, len(streams), len(chunks))
    await asyncio.gather(*[_run_trade_conn(market, chunk, trade_aggregator, orderbook_aggregator) for chunk in chunks])


async def run_depth_ws_ingest(market: str, symbols: list[str], orderbook_aggregator: OrderbookAggregator) -> None:
    depth_update_ms = max(100, int(settings.ingest_depth_update_ms))
    streams = [f"{symbol.lower()}@depth5@{depth_update_ms}ms" for symbol in symbols]
    chunks = _chunk(streams, settings.ingest_streams_per_conn)
    logger.info("DepthWS(%s) streams=%d conn=%d update=%dms", market, len(streams), len(chunks), depth_update_ms)
    await asyncio.gather(*[_run_depth_conn(market, chunk, orderbook_aggregator) for chunk in chunks])

