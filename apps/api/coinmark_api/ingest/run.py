from __future__ import annotations

import asyncio
import json
import logging
import time
from decimal import Decimal

from aiokafka import AIOKafkaConsumer
from sqlalchemy import case, func

from coinmark_api.config import settings
from coinmark_api.db import SessionLocal, engine, dialect_name
from coinmark_api.db_upsert import insert
from coinmark_api.ingest.aggregator import TradeAggregator
from coinmark_api.ingest.orderbook_aggregator import OrderbookAggregator
from coinmark_api.models import Base, OrderbookFeatureBucket, TradeBucket
from coinmark_api.services.binance.rest import get_pairs, get_ticker_24h_all
from coinmark_api.ingest.ws import build_depth_features, reset_runtime_counters, run_depth_ws_ingest, run_trade_ws_ingest
from coinmark_api.services.bot.funding import refresh_funding_rate_snapshots
from coinmark_api.services.bot.oi_marketcap import refresh_market_caps_from_binance_bapi, refresh_open_interest_snapshots
from coinmark_api.migrations import migrate
from coinmark_api.services.anomaly import refresh_sr_levels, scan_anomalies
from coinmark_api.services.absorption_signal import (
    cleanup_absorption_signal_snapshots,
    refresh_absorption_signal_snapshots,
)
from coinmark_api.services.institutional_levels import refresh_institutional_level_snapshots
from coinmark_api.services.binance.backfill import backfill_trade_buckets_from_klines


logging.basicConfig(level=getattr(logging, settings.api_log_level.upper(), logging.INFO))
logger = logging.getLogger("coinmark.ingest")
logging.getLogger("httpx").setLevel(logging.WARNING)


RUNTIME_STATS = {
    "trade_flush_rows": 0,
    "trade_flush_batches": 0,
    "orderbook_flush_rows": 0,
    "orderbook_flush_batches": 0,
    "kafka_trade_msg": 0,
    "kafka_depth_msg": 0,
}

def _parse_stream_source(value: str) -> str:
    v = str(value or "ws").strip().lower()
    if v not in {"ws", "kafka"}:
        return "ws"
    return v


async def _consume_trade_from_kafka(market: str, trade_aggregator: TradeAggregator, orderbook_aggregator: OrderbookAggregator) -> None:
    brokers = [s.strip() for s in str(settings.ingest_kafka_brokers or "").split(",") if s.strip()]
    if not brokers:
        raise RuntimeError("INGEST_KAFKA_BROKERS is empty")

    topic = str(settings.ingest_kafka_trade_topic or "").strip()
    if not topic:
        raise RuntimeError("INGEST_KAFKA_TRADE_TOPIC is empty")

    group_id = f"{settings.ingest_kafka_group_id_prefix}-{market}"
    auto_offset_reset = str(settings.ingest_kafka_auto_offset_reset or "latest").strip().lower()
    if auto_offset_reset not in {"latest", "earliest"}:
        auto_offset_reset = "latest"

    consumer = AIOKafkaConsumer(
        topic,
        bootstrap_servers=brokers,
        group_id=group_id,
        enable_auto_commit=True,
        auto_offset_reset=auto_offset_reset,
        value_deserializer=lambda b: b,
    )
    await consumer.start()
    logger.info(
        "TradeKafka(%s) started brokers=%s topic=%s group=%s offset=%s",
        market,
        brokers,
        topic,
        group_id,
        auto_offset_reset,
    )
    try:
        async for msg in consumer:
            try:
                payload = json.loads((msg.value or b"{}").decode("utf-8"))
                if str(payload.get("market", "")).lower() != market:
                    continue
                symbol = str(payload.get("symbol") or "").upper()
                if not symbol:
                    continue
                ts_ms = int(payload.get("trade_time_ms") or payload.get("event_time_ms") or 0)
                if ts_ms <= 0:
                    continue
                price = Decimal(str(payload.get("price") or "0"))
                qty = Decimal(str(payload.get("qty") or "0"))
                if price <= 0 or qty <= 0:
                    continue
                notional = price * qty
                is_buyer_maker = bool(payload.get("is_buyer_maker"))
                if is_buyer_maker:
                    taker_buy_notional = Decimal("0")
                    taker_sell_notional = notional
                else:
                    taker_buy_notional = notional
                    taker_sell_notional = Decimal("0")

                await trade_aggregator.add_trade(
                    market=market,
                    symbol=symbol,
                    ts_ms=ts_ms,
                    price=price,
                    taker_buy_notional=taker_buy_notional,
                    taker_sell_notional=taker_sell_notional,
                    quote_notional=notional,
                    trade_count=1,
                )
                await orderbook_aggregator.add_trade(
                    market=market,
                    symbol=symbol,
                    ts_ms=ts_ms,
                    taker_buy_notional=taker_buy_notional,
                    taker_sell_notional=taker_sell_notional,
                )
                RUNTIME_STATS["kafka_trade_msg"] += 1
            except Exception:
                continue
    finally:
        await consumer.stop()

async def _consume_depth_from_kafka(market: str, orderbook_aggregator: OrderbookAggregator) -> None:
    brokers = [s.strip() for s in str(settings.ingest_kafka_brokers or "").split(",") if s.strip()]
    if not brokers:
        raise RuntimeError("INGEST_KAFKA_BROKERS is empty")

    topic = str(settings.ingest_kafka_depth_topic or "").strip()
    if not topic:
        raise RuntimeError("INGEST_KAFKA_DEPTH_TOPIC is empty")

    group_id = f"{settings.ingest_kafka_depth_group_id_prefix}-{market}"
    auto_offset_reset = str(settings.ingest_kafka_auto_offset_reset or "latest").strip().lower()
    if auto_offset_reset not in {"latest", "earliest"}:
        auto_offset_reset = "latest"

    consumer = AIOKafkaConsumer(
        topic,
        bootstrap_servers=brokers,
        group_id=group_id,
        enable_auto_commit=True,
        auto_offset_reset=auto_offset_reset,
        value_deserializer=lambda b: b,
    )
    await consumer.start()
    logger.info(
        "DepthKafka(%s) started brokers=%s topic=%s group=%s offset=%s",
        market,
        brokers,
        topic,
        group_id,
        auto_offset_reset,
    )
    try:
        async for msg in consumer:
            try:
                payload = json.loads((msg.value or b"{}").decode("utf-8"))
                if str(payload.get("market", "")).lower() != market:
                    continue

                symbol = str(payload.get("symbol") or "").upper()
                ts_ms = int(payload.get("event_time_ms") or 0)
                if not symbol or ts_ms <= 0:
                    continue

                bids = payload.get("bids")
                asks = payload.get("asks")
                if not isinstance(bids, list) or not isinstance(asks, list):
                    continue

                features = build_depth_features({"b": bids, "a": asks})
                if features is None:
                    continue

                spread_bps, depth_imbalance_l5, microprice_shift_bps, wall_pressure_l5, l1_depth_notional = features
                await orderbook_aggregator.add_orderbook_sample(
                    market=market,
                    symbol=symbol,
                    ts_ms=ts_ms,
                    spread_bps=spread_bps,
                    depth_imbalance_l5=depth_imbalance_l5,
                    microprice_shift_bps=microprice_shift_bps,
                    wall_pressure_l5=wall_pressure_l5,
                    l1_depth_notional=l1_depth_notional,
                )
                RUNTIME_STATS["kafka_depth_msg"] += 1
            except Exception:
                continue
    finally:
        await consumer.stop()

def _apply_symbol_limit(symbols: list[str]) -> list[str]:
    if settings.ingest_symbol_limit and settings.ingest_symbol_limit > 0:
        return symbols[: settings.ingest_symbol_limit]
    return symbols


def _chunk(values: list[dict], size: int) -> list[list[dict]]:
    if size <= 0:
        return [values]
    return [values[i : i + size] for i in range(0, len(values), size)]


async def _top_symbols_by_volume(market: str, top_n: int) -> list[str]:
    valid = set(await get_pairs(market))
    rows = await get_ticker_24h_all(market)
    ranked: list[tuple[float, str]] = []
    for r in rows:
        sym = r.get("symbol")
        if not sym or not isinstance(sym, str) or not sym.endswith("USDT"):
            continue
        if sym not in valid:
            continue
        try:
            qv = float(r.get("quoteVolume") or 0.0)
        except (TypeError, ValueError):
            continue
        ranked.append((qv, sym))
    ranked.sort(key=lambda x: x[0], reverse=True)
    return [s for _, s in ranked[: max(1, int(top_n))]]


async def _flush_loop(aggregator: TradeAggregator) -> None:
    least_fn = func.least if dialect_name() == "postgresql" else func.min
    greatest_fn = func.greatest if dialect_name() == "postgresql" else func.max

    while True:
        await asyncio.sleep(max(1, settings.ingest_flush_interval_sec))
        drained = await aggregator.drain()
        if not drained:
            continue

        values = []
        for key, d in drained:
            values.append(
                {
                    "market": key.market,
                    "symbol": key.symbol,
                    "bucket": key.bucket,
                    "bucket_start_ms": key.bucket_start_ms,
                    "taker_buy_notional": d.taker_buy_notional,
                    "taker_sell_notional": d.taker_sell_notional,
                    "quote_notional": d.quote_notional,
                    "trade_count": d.trade_count,
                    "first_trade_ms": d.first_trade_ms,
                    "last_trade_ms": d.last_trade_ms,
                    "open_price": d.open_price,
                    "close_price": d.close_price,
                    "high_price": d.high_price,
                    "low_price": d.low_price,
                }
            )

        async with SessionLocal() as session:
            # asyncpg/Postgres 鏈夊弬鏁版暟閲忎笂闄愶紙32767锛夈€傚叏閲?USDT + 澶氭《浼氬緢瀹规槗瓒呴檺锛屽繀椤诲垎鎵?upsert銆?
            for chunk in _chunk(values, settings.ingest_db_batch_size):
                stmt = insert(TradeBucket).values(chunk)
                stmt = stmt.on_conflict_do_update(
                    index_elements=["market", "symbol", "bucket", "bucket_start_ms"],
                    set_={
                        "taker_buy_notional": TradeBucket.taker_buy_notional + stmt.excluded.taker_buy_notional,
                        "taker_sell_notional": TradeBucket.taker_sell_notional + stmt.excluded.taker_sell_notional,
                        "quote_notional": TradeBucket.quote_notional + stmt.excluded.quote_notional,
                        "trade_count": TradeBucket.trade_count + stmt.excluded.trade_count,
                        "first_trade_ms": least_fn(
                            func.coalesce(TradeBucket.first_trade_ms, stmt.excluded.first_trade_ms),
                            stmt.excluded.first_trade_ms,
                        ),
                        "last_trade_ms": greatest_fn(
                            func.coalesce(TradeBucket.last_trade_ms, stmt.excluded.last_trade_ms),
                            stmt.excluded.last_trade_ms,
                        ),
                        "open_price": case(
                            (
                                (TradeBucket.first_trade_ms.is_(None))
                                | (stmt.excluded.first_trade_ms < TradeBucket.first_trade_ms),
                                stmt.excluded.open_price,
                            ),
                            else_=TradeBucket.open_price,
                        ),
                        "close_price": case(
                            (
                                (TradeBucket.last_trade_ms.is_(None))
                                | (stmt.excluded.last_trade_ms > TradeBucket.last_trade_ms),
                                stmt.excluded.close_price,
                            ),
                            else_=TradeBucket.close_price,
                        ),
                        "high_price": greatest_fn(
                            func.coalesce(TradeBucket.high_price, stmt.excluded.high_price),
                            stmt.excluded.high_price,
                        ),
                        "low_price": least_fn(
                            func.coalesce(TradeBucket.low_price, stmt.excluded.low_price),
                            stmt.excluded.low_price,
                        ),
                    },
                )
                await session.execute(stmt)
                RUNTIME_STATS["trade_flush_batches"] += 1
            RUNTIME_STATS["trade_flush_rows"] += len(values)
            await session.commit()


async def _flush_orderbook_loop(orderbook_aggregator: OrderbookAggregator) -> None:
    while True:
        await asyncio.sleep(max(1, settings.ingest_flush_interval_sec))
        now_ms = int(time.time() * 1000)
        drained = await orderbook_aggregator.drain_closed(now_ms)
        if not drained:
            continue

        values = []
        for key, delta in drained:
            values.append(
                {
                    "market": key.market,
                    "symbol": key.symbol,
                    "bucket": key.bucket,
                    "bucket_start_ms": key.bucket_start_ms,
                    "spread_bps_sum": delta.spread_bps_sum,
                    "depth_imbalance_l5_sum": delta.depth_imbalance_l5_sum,
                    "microprice_shift_bps_sum": delta.microprice_shift_bps_sum,
                    "wall_pressure_l5_sum": delta.wall_pressure_l5_sum,
                    "sample_count": delta.sample_count,
                    "taker_buy_notional": delta.taker_buy_notional,
                    "taker_sell_notional": delta.taker_sell_notional,
                    "depletion_events": delta.depletion_events,
                    "replenishment_events": delta.replenishment_events,
                }
            )

        async with SessionLocal() as session:
            for chunk in _chunk(values, settings.ingest_db_batch_size):
                stmt = insert(OrderbookFeatureBucket).values(chunk)
                stmt = stmt.on_conflict_do_update(
                    index_elements=["market", "symbol", "bucket", "bucket_start_ms"],
                    set_={
                        "spread_bps_sum": OrderbookFeatureBucket.spread_bps_sum + stmt.excluded.spread_bps_sum,
                        "depth_imbalance_l5_sum": OrderbookFeatureBucket.depth_imbalance_l5_sum
                        + stmt.excluded.depth_imbalance_l5_sum,
                        "microprice_shift_bps_sum": OrderbookFeatureBucket.microprice_shift_bps_sum
                        + stmt.excluded.microprice_shift_bps_sum,
                        "wall_pressure_l5_sum": OrderbookFeatureBucket.wall_pressure_l5_sum
                        + stmt.excluded.wall_pressure_l5_sum,
                        "sample_count": OrderbookFeatureBucket.sample_count + stmt.excluded.sample_count,
                        "taker_buy_notional": OrderbookFeatureBucket.taker_buy_notional + stmt.excluded.taker_buy_notional,
                        "taker_sell_notional": OrderbookFeatureBucket.taker_sell_notional + stmt.excluded.taker_sell_notional,
                        "depletion_events": OrderbookFeatureBucket.depletion_events + stmt.excluded.depletion_events,
                        "replenishment_events": OrderbookFeatureBucket.replenishment_events
                        + stmt.excluded.replenishment_events,
                    },
                )
                await session.execute(stmt)
                RUNTIME_STATS["orderbook_flush_batches"] += 1
            RUNTIME_STATS["orderbook_flush_rows"] += len(values)
            await session.commit()


async def _runtime_report_loop(aggregator: TradeAggregator, orderbook_aggregator: OrderbookAggregator) -> None:
    while True:
        await asyncio.sleep(max(10, int(settings.ingest_runtime_report_interval_sec)))
        trade_msg_count, depth_msg_count = reset_runtime_counters()
        trade_msg_count += int(RUNTIME_STATS["kafka_trade_msg"])
        depth_msg_count += int(RUNTIME_STATS["kafka_depth_msg"])

        trade_bucket_count = len(getattr(aggregator, "_deltas", {}))
        orderbook_bucket_count = len(getattr(orderbook_aggregator, "_deltas", {}))

        logger.info(
            "IngestRuntime trade_msg=%d depth_msg=%d trade_buckets=%d orderbook_buckets=%d trade_flush_rows=%d trade_flush_batches=%d orderbook_flush_rows=%d orderbook_flush_batches=%d",
            trade_msg_count,
            depth_msg_count,
            trade_bucket_count,
            orderbook_bucket_count,
            int(RUNTIME_STATS["trade_flush_rows"]),
            int(RUNTIME_STATS["trade_flush_batches"]),
            int(RUNTIME_STATS["orderbook_flush_rows"]),
            int(RUNTIME_STATS["orderbook_flush_batches"]),
        )

        RUNTIME_STATS["trade_flush_rows"] = 0
        RUNTIME_STATS["trade_flush_batches"] = 0
        RUNTIME_STATS["orderbook_flush_rows"] = 0
        RUNTIME_STATS["orderbook_flush_batches"] = 0
        RUNTIME_STATS["kafka_trade_msg"] = 0
        RUNTIME_STATS["kafka_depth_msg"] = 0


async def _funding_loop() -> None:
    while True:
        try:
            await refresh_funding_rate_snapshots()
        except Exception as e:
            logger.warning("璧勯噾璐圭巼鍒锋柊澶辫触锛?s", e)
        await asyncio.sleep(60)


async def _marketcap_loop() -> None:
    while True:
        try:
            await refresh_market_caps_from_binance_bapi()
        except Exception as e:
            logger.warning("甯傚€煎埛鏂板け璐ワ細%s", e)
        await asyncio.sleep(10 * 60)


async def _open_interest_loop() -> None:
    # OI 绔偣鎸?symbol 鍗曚釜鏌ヨ锛氬叏閲?USDT 浼氭墦鐖?Binance 闄愭祦涓庢湰鍦扮綉缁溿€?
    # MVP 绛栫暐锛氭寜 24h quoteVolume 鍙?TopN锛屽啀鍒锋柊 OI锛屼繚璇佲€滃彲杩芥函 + 鍙绠椻€濈殑鍚屾椂鍙暱鏈熻繍琛屻€?
    while True:
        try:
            valid = set(await get_pairs("swap"))
            rows = await get_ticker_24h_all("swap")
            ranked: list[tuple[float, str]] = []
            for r in rows:
                sym = r.get("symbol")
                if not sym or not isinstance(sym, str) or not sym.endswith("USDT"):
                    continue
                if sym not in valid:
                    continue
                try:
                    qv = float(r.get("quoteVolume") or 0.0)
                except (TypeError, ValueError):
                    continue
                ranked.append((qv, sym))
            ranked.sort(key=lambda x: x[0], reverse=True)
            top_n = max(1, int(settings.oi_refresh_top_n))
            top_symbols = [s for _, s in ranked[:top_n]]
            await refresh_open_interest_snapshots(top_symbols)
        except Exception as e:
            logger.warning("OI 鍒锋柊澶辫触锛?s", e)
        await asyncio.sleep(max(30, int(settings.oi_refresh_interval_sec)))


async def _sr_levels_loop() -> None:
    while True:
        try:
            top_n = int(settings.sr_refresh_top_n)
            if settings.ingest_enable_spot:
                spot = await _top_symbols_by_volume("spot", top_n)
                await refresh_sr_levels(market="spot", symbols=spot)
            if settings.ingest_enable_swap:
                swap = await _top_symbols_by_volume("swap", top_n)
                await refresh_sr_levels(market="swap", symbols=swap)
        except Exception as e:
            logger.warning("SR 鍒锋柊澶辫触锛?s", e)
        await asyncio.sleep(max(60, int(settings.sr_refresh_interval_sec)))


async def _anomaly_loop() -> None:
    while True:
        try:
            top_n = int(settings.anomaly_scan_top_n)
            history = int(settings.anomaly_history_15m)
            margin = Decimal(str(settings.anomaly_breakout_margin_pct))
            vol_factor = Decimal(str(settings.anomaly_volume_spike_factor))
            amp_factor = Decimal(str(settings.anomaly_amplitude_spike_factor))

            if settings.ingest_enable_spot:
                spot = await _top_symbols_by_volume("spot", top_n)
                await scan_anomalies(
                    market="spot",
                    symbols=spot,
                    history_15m=history,
                    breakout_margin_pct=margin,
                    volume_spike_factor=vol_factor,
                    amplitude_spike_factor=amp_factor,
                )
            if settings.ingest_enable_swap:
                swap = await _top_symbols_by_volume("swap", top_n)
                await scan_anomalies(
                    market="swap",
                    symbols=swap,
                    history_15m=history,
                    breakout_margin_pct=margin,
                    volume_spike_factor=vol_factor,
                    amplitude_spike_factor=amp_factor,
                )
        except Exception as e:
            logger.warning("寮傚姩鎵弿澶辫触锛?s", e)
        await asyncio.sleep(max(15, int(settings.anomaly_scan_interval_sec)))


async def _absorption_signal_loop() -> None:
    cleanup_interval_ms = max(60, int(settings.absorption_snapshot_cleanup_interval_sec)) * 1000
    retention_hours = max(1, int(settings.absorption_snapshot_retention_hours))
    last_cleanup_ms = 0

    while True:
        try:
            top_n = int(settings.anomaly_scan_top_n)
            if settings.ingest_enable_spot:
                await refresh_absorption_signal_snapshots(market="spot", top_n=top_n)
            if settings.ingest_enable_swap:
                await refresh_absorption_signal_snapshots(market="swap", top_n=top_n)

            now_ms = int(time.time() * 1000)
            if now_ms - last_cleanup_ms >= cleanup_interval_ms:
                deleted = await cleanup_absorption_signal_snapshots(retention_hours=retention_hours)
                if deleted > 0:
                    logger.info("Absorption snapshots cleanup done, removed=%s", deleted)
                last_cleanup_ms = now_ms
        except Exception as e:
            logger.warning("Absorption signal scan failed: %s", e)
        await asyncio.sleep(max(30, int(settings.anomaly_scan_interval_sec)))


async def _institutional_levels_loop() -> None:
    while True:
        try:
            top_n = int(settings.anomaly_scan_top_n)
            if settings.ingest_enable_spot:
                await refresh_institutional_level_snapshots(market="spot", top_n=top_n)
            if settings.ingest_enable_swap:
                await refresh_institutional_level_snapshots(market="swap", top_n=top_n)
        except Exception as e:
            logger.warning("institutional levels scan failed: %s", e)
        await asyncio.sleep(max(30, int(settings.anomaly_scan_interval_sec)))


async def _backfill_once() -> None:
    if not settings.backfill_enable:
        return
    try:
        logger.info("Backfill start TopN=%s concurrency=%s", settings.backfill_top_n, settings.backfill_concurrency)
        top_n = int(settings.backfill_top_n)
        conc = int(settings.backfill_concurrency)
        batch = int(settings.ingest_db_batch_size)

        if settings.ingest_enable_spot:
            spot = await _top_symbols_by_volume("spot", top_n)
            if int(settings.backfill_1m_limit) > 0:
                await backfill_trade_buckets_from_klines(
                    market="spot",
                    symbols=spot,
                    interval="1m",
                    limit=int(settings.backfill_1m_limit),
                    concurrency=conc,
                    db_batch_size=batch,
                )
            await backfill_trade_buckets_from_klines(
                market="spot",
                symbols=spot,
                interval="15m",
                limit=int(settings.backfill_15m_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="spot",
                symbols=spot,
                interval="1h",
                limit=int(settings.backfill_1h_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="spot",
                symbols=spot,
                interval="4h",
                limit=int(settings.backfill_4h_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="spot",
                symbols=spot,
                interval="1d",
                limit=int(settings.backfill_1d_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await refresh_sr_levels(market="spot", symbols=spot)

        if settings.ingest_enable_swap:
            swap = await _top_symbols_by_volume("swap", top_n)
            if int(settings.backfill_1m_limit) > 0:
                await backfill_trade_buckets_from_klines(
                    market="swap",
                    symbols=swap,
                    interval="1m",
                    limit=int(settings.backfill_1m_limit),
                    concurrency=conc,
                    db_batch_size=batch,
                )
            await backfill_trade_buckets_from_klines(
                market="swap",
                symbols=swap,
                interval="15m",
                limit=int(settings.backfill_15m_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="swap",
                symbols=swap,
                interval="1h",
                limit=int(settings.backfill_1h_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="swap",
                symbols=swap,
                interval="4h",
                limit=int(settings.backfill_4h_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await backfill_trade_buckets_from_klines(
                market="swap",
                symbols=swap,
                interval="1d",
                limit=int(settings.backfill_1d_limit),
                concurrency=conc,
                db_batch_size=batch,
            )
            await refresh_sr_levels(market="swap", symbols=swap)

        logger.info("Backfill completed")
    except Exception as e:
        logger.warning("Backfill failed, keep incremental ingest: %s", e)


async def main() -> None:
    await migrate(engine)
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    aggregator = TradeAggregator(buckets=["1m", "15m", "1h", "4h", "1d"])
    orderbook_aggregator = OrderbookAggregator(bucket="1m")

    tasks = []

    trade_source_spot = _parse_stream_source(settings.ingest_trade_source_spot)
    trade_source_swap = _parse_stream_source(settings.ingest_trade_source_swap)
    depth_source_spot = _parse_stream_source(settings.ingest_depth_source_spot)
    depth_source_swap = _parse_stream_source(settings.ingest_depth_source_swap)

    logger.info(
        "IngestSource trade_spot=%s trade_swap=%s depth_spot=%s depth_swap=%s depth_enabled=%s",
        trade_source_spot,
        trade_source_swap,
        depth_source_spot,
        depth_source_swap,
        settings.ingest_enable_depth,
    )

    if settings.ingest_enable_spot:
        spot_pairs = _apply_symbol_limit(await get_pairs("spot"))
        if trade_source_spot == "kafka":
            tasks.append(_consume_trade_from_kafka("spot", aggregator, orderbook_aggregator))
        else:
            tasks.append(run_trade_ws_ingest("spot", spot_pairs, aggregator, orderbook_aggregator))

        if settings.ingest_enable_depth:
            if depth_source_spot == "kafka":
                tasks.append(_consume_depth_from_kafka("spot", orderbook_aggregator))
            else:
                tasks.append(run_depth_ws_ingest("spot", spot_pairs, orderbook_aggregator))

    if settings.ingest_enable_swap:
        swap_pairs = _apply_symbol_limit(await get_pairs("swap"))
        if trade_source_swap == "kafka":
            tasks.append(_consume_trade_from_kafka("swap", aggregator, orderbook_aggregator))
        else:
            tasks.append(run_trade_ws_ingest("swap", swap_pairs, aggregator, orderbook_aggregator))

        if settings.ingest_enable_depth:
            if depth_source_swap == "kafka":
                tasks.append(_consume_depth_from_kafka("swap", orderbook_aggregator))
            else:
                tasks.append(run_depth_ws_ingest("swap", swap_pairs, orderbook_aggregator))

    if not tasks:
        raise RuntimeError("No ingest market enabled, check INGEST_ENABLE_SPOT/INGEST_ENABLE_SWAP")

    tasks.append(_flush_loop(aggregator))
    tasks.append(_flush_orderbook_loop(orderbook_aggregator))
    tasks.append(_funding_loop())
    tasks.append(_marketcap_loop())
    tasks.append(_open_interest_loop())
    tasks.append(_sr_levels_loop())
    tasks.append(_anomaly_loop())
    tasks.append(_absorption_signal_loop())
    tasks.append(_institutional_levels_loop())
    tasks.append(_backfill_once())
    tasks.append(_runtime_report_loop(aggregator, orderbook_aggregator))
    await asyncio.gather(*tasks)


if __name__ == "__main__":
    asyncio.run(main())









