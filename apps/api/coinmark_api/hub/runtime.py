from __future__ import annotations

import asyncio
import logging
import time

from sqlalchemy import delete

from coinmark_api.config import settings
from coinmark_api.db import SessionLocal
from coinmark_api.hub.anomaly_stream import HubAnomalyStream
from coinmark_api.hub.depth_fullscan import create_depth_fullscan_tasks
from coinmark_api.hub.manager import HubConnectionManager
from coinmark_api.hub.publisher import HubPublisher
from coinmark_api.models import AnomalyEvent, OrderbookHeatmapSnapshot
from coinmark_api.services.absorption_signal import cleanup_absorption_signal_snapshots
from coinmark_api.services.signal_lab import scan_climax_reversal

logger = logging.getLogger("coinmark.hub")


def _parse_origins(raw: str) -> set[str]:
    if not raw:
        return {"*"}
    return {item.strip() for item in raw.split(",") if item.strip()}


hub_connection_manager = HubConnectionManager(
    max_connections=settings.hub_max_connections,
    heartbeat_timeout_sec=settings.hub_heartbeat_timeout_sec,
    allowed_origins=_parse_origins(settings.hub_allowed_origins),
)

hub_publisher = HubPublisher(
    hub_connection_manager,
    dedupe_window_sec=settings.hub_dedupe_window_sec,
    max_events_per_sec=settings.hub_broadcast_max_events_per_sec,
)

hub_anomaly_stream = HubAnomalyStream(
    hub_publisher,
    poll_interval_sec=settings.hub_anomaly_scan_interval_sec,
    batch_size=settings.hub_anomaly_scan_batch_size,
)

_hub_tasks: list[asyncio.Task] = []
_hub_stop_event: asyncio.Event | None = None


async def _climax_scan_loop(stop_event: asyncio.Event, interval_sec: int) -> None:
    while not stop_event.is_set():
        try:
            result = await scan_climax_reversal(market_scope="both")
            if result.get("insertedEvents", 0) > 0:
                logger.info(
                    "climax scan candidates=%s events=%s",
                    result.get("candidates", 0),
                    result.get("insertedEvents", 0),
                )
        except Exception:
            logger.exception("climax scan failed")
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=max(30, interval_sec))
            break
        except asyncio.TimeoutError:
            pass


async def _db_cleanup_loop(stop_event: asyncio.Event, interval_sec: int = 900) -> None:
    """Periodically purge stale rows: heatmap 24h, absorption 24h, anomaly_events 30d."""
    while not stop_event.is_set():
        try:
            now_ms = int(time.time() * 1000)
            heatmap_cutoff = now_ms - 24 * 60 * 60 * 1000
            anomaly_cutoff = now_ms - 30 * 24 * 60 * 60 * 1000
            async with SessionLocal() as session:
                r1 = await session.execute(
                    delete(OrderbookHeatmapSnapshot).where(OrderbookHeatmapSnapshot.bucket_start_ms < heatmap_cutoff)
                )
                r2 = await session.execute(
                    delete(AnomalyEvent).where(AnomalyEvent.event_time_ms < anomaly_cutoff)
                )
                await session.commit()
                heatmap_del = int(r1.rowcount or 0)
                anomaly_del = int(r2.rowcount or 0)
            absorption_del = await cleanup_absorption_signal_snapshots(retention_hours=24)
            if heatmap_del or anomaly_del or absorption_del:
                logger.info(
                    "db cleanup heatmap=%s absorption=%s anomaly=%s",
                    heatmap_del, absorption_del, anomaly_del,
                )
        except Exception:
            logger.exception("db cleanup failed")
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=max(60, interval_sec))
            break
        except asyncio.TimeoutError:
            pass


async def start_hub_runtime() -> None:
    global _hub_tasks, _hub_stop_event

    if not settings.hub_enabled:
        logger.info("hub runtime disabled")
        return
    if _hub_tasks:
        return

    _hub_stop_event = asyncio.Event()
    _hub_tasks = [
        asyncio.create_task(
            hub_connection_manager.run_heartbeat_loop(
                interval_sec=settings.hub_heartbeat_interval_sec,
                stop_event=_hub_stop_event,
            )
        ),
        asyncio.create_task(hub_anomaly_stream.run(_hub_stop_event)),
        asyncio.create_task(
            _climax_scan_loop(_hub_stop_event, settings.hub_climax_scan_interval_sec)
        ),
        asyncio.create_task(
            _db_cleanup_loop(_hub_stop_event, settings.absorption_snapshot_cleanup_interval_sec)
        ),
    ]
    _hub_tasks.extend(create_depth_fullscan_tasks(_hub_stop_event))
    logger.info("hub runtime started")


async def stop_hub_runtime() -> None:
    global _hub_tasks, _hub_stop_event

    if _hub_stop_event is not None:
        _hub_stop_event.set()

    for task in _hub_tasks:
        task.cancel()

    if _hub_tasks:
        await asyncio.gather(*_hub_tasks, return_exceptions=True)

    _hub_tasks = []
    _hub_stop_event = None
    logger.info("hub runtime stopped")
