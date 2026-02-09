from __future__ import annotations

import asyncio
import logging

from coinmark_api.config import settings
from coinmark_api.hub.anomaly_stream import HubAnomalyStream
from coinmark_api.hub.manager import HubConnectionManager
from coinmark_api.hub.publisher import HubPublisher
from coinmark_api.services.price_impact_wall import refresh_price_impact_walls
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


async def _wall_refresh_loop(stop_event: asyncio.Event, interval_sec: int) -> None:
    while not stop_event.is_set():
        try:
            result = await refresh_price_impact_walls(market_scope="both")
            logger.info(
                "wall refresh done candidates=%s events=%s",
                result.get("candidates", 0),
                result.get("insertedEvents", 0),
            )
        except Exception:
            logger.exception("wall refresh failed")
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=max(60, interval_sec))
            break
        except asyncio.TimeoutError:
            pass


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
            _wall_refresh_loop(_hub_stop_event, settings.hub_wall_refresh_interval_sec)
        ),
        asyncio.create_task(
            _climax_scan_loop(_hub_stop_event, settings.hub_climax_scan_interval_sec)
        ),
    ]
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
