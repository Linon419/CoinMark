from __future__ import annotations

import asyncio
import logging

from sqlalchemy import func, select

from coinmark_api.db import SessionLocal
from coinmark_api.hub.publisher import HubPublisher
from coinmark_api.hub.schemas import HubEvent, build_event_id
from coinmark_api.models import AnomalyEvent

logger = logging.getLogger("coinmark.hub")


def _event_level(event_type: str) -> str:
    t = (event_type or "").lower()
    if t in {"breakout_up", "breakout_down"}:
        return "warning"
    if t in {"volume_spike", "amplitude_spike"}:
        return "info"
    return "warning"


class HubAnomalyStream:
    def __init__(
        self,
        publisher: HubPublisher,
        *,
        poll_interval_sec: int,
        batch_size: int,
    ) -> None:
        self.publisher = publisher
        self.poll_interval_sec = max(1, int(poll_interval_sec))
        self.batch_size = max(20, int(batch_size))
        self._last_id = 0

    async def _bootstrap_last_id(self) -> None:
        async with SessionLocal() as session:
            stmt = select(func.max(AnomalyEvent.id))
            latest = (await session.execute(stmt)).scalar()
            self._last_id = int(latest or 0)

    def _to_hub_event(self, row: AnomalyEvent) -> HubEvent:
        event_ts = int(row.event_time_ms)
        minute_bucket = event_ts // 60000
        raw_type = str(row.event_type or "").upper()
        hub_type = f"ANOMALY_{raw_type}"
        return HubEvent(
            id=f"anomaly_{row.id}_{build_event_id(row.market, row.symbol, row.event_type, row.event_time_ms)}",
            type=hub_type,
            level=_event_level(str(row.event_type)),
            title="市场异动",
            content=row.title,
            symbol=row.symbol,
            market=row.market,
            ts=event_ts,
            meta={
                "eventType": row.event_type,
                "tfSignal": row.tf_signal,
                "tfLevel": row.tf_level,
                "details": row.details,
            },
            dedupe_key=f"anomaly:{row.market}:{row.symbol}:{row.event_type}:{minute_bucket}",
        )

    async def poll_once(self) -> int:
        async with SessionLocal() as session:
            stmt = (
                select(AnomalyEvent)
                .where(AnomalyEvent.id > self._last_id)
                .order_by(AnomalyEvent.id.asc())
                .limit(self.batch_size)
            )
            rows = (await session.execute(stmt)).scalars().all()

        if not rows:
            return 0

        for row in rows:
            await self.publisher.publish(self._to_hub_event(row))
            self._last_id = max(self._last_id, int(row.id))
        return len(rows)

    async def run(self, stop_event: asyncio.Event) -> None:
        await self._bootstrap_last_id()
        while not stop_event.is_set():
            try:
                count = await self.poll_once()
                if count == 0:
                    await asyncio.sleep(self.poll_interval_sec)
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("hub anomaly stream loop failed")
                await asyncio.sleep(self.poll_interval_sec)
