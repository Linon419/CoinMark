from __future__ import annotations

import asyncio
import time

from coinmark_api.hub.manager import HubConnectionManager
from coinmark_api.hub.schemas import HubEvent


class HubPublisher:
    def __init__(
        self,
        manager: HubConnectionManager,
        *,
        dedupe_window_sec: int,
        max_events_per_sec: int,
    ) -> None:
        self.manager = manager
        self.dedupe_window_sec = max(1, int(dedupe_window_sec))
        self.max_events_per_sec = max(1, int(max_events_per_sec))
        self._dedupe_cache: dict[str, float] = {}
        self._rate_window_sec = 0
        self._rate_count = 0
        self._lock = asyncio.Lock()

    def _make_dedupe_key(self, event: HubEvent) -> str:
        if event.dedupe_key:
            return event.dedupe_key
        return f"{event.type}:{event.market or ''}:{event.symbol or ''}:{event.title}"

    async def publish(self, event: HubEvent) -> bool:
        now = time.time()
        now_sec = int(now)
        dedupe_key = self._make_dedupe_key(event)

        async with self._lock:
            expired = [k for k, t in self._dedupe_cache.items() if now - t >= self.dedupe_window_sec]
            for key in expired:
                self._dedupe_cache.pop(key, None)

            if dedupe_key in self._dedupe_cache:
                return False

            if now_sec != self._rate_window_sec:
                self._rate_window_sec = now_sec
                self._rate_count = 0
            if self._rate_count >= self.max_events_per_sec:
                return False
            self._rate_count += 1

            self._dedupe_cache[dedupe_key] = now

        await self.manager.broadcast_event(event.model_dump(mode="json", exclude_none=True))
        return True
