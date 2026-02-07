from __future__ import annotations

import asyncio
import logging
import time
import uuid
from dataclasses import dataclass, field

from fastapi import WebSocket
from starlette.websockets import WebSocketState

logger = logging.getLogger("coinmark.hub")


@dataclass(slots=True)
class HubConnection:
    connection_id: str
    websocket: WebSocket
    created_at_ms: int
    last_seen_ms: int
    markets: set[str] = field(default_factory=set)
    symbols: set[str] = field(default_factory=set)
    event_types: set[str] = field(default_factory=set)


class HubConnectionManager:
    def __init__(
        self,
        *,
        max_connections: int,
        heartbeat_timeout_sec: int,
        allowed_origins: set[str],
    ) -> None:
        self.max_connections = max(1, int(max_connections))
        self.heartbeat_timeout_ms = max(10, int(heartbeat_timeout_sec)) * 1000
        self.allowed_origins = {o.strip() for o in allowed_origins if o.strip()}
        self._connections: dict[str, HubConnection] = {}
        self._lock = asyncio.Lock()

    def is_origin_allowed(self, origin: str | None) -> bool:
        if "*" in self.allowed_origins:
            return True
        if not origin:
            return False
        return origin in self.allowed_origins

    async def connect(self, websocket: WebSocket) -> HubConnection | None:
        async with self._lock:
            if len(self._connections) >= self.max_connections:
                await websocket.close(code=1013, reason="hub overloaded")
                return None

        await websocket.accept()
        now = int(time.time() * 1000)
        conn = HubConnection(
            connection_id=uuid.uuid4().hex,
            websocket=websocket,
            created_at_ms=now,
            last_seen_ms=now,
        )
        async with self._lock:
            self._connections[conn.connection_id] = conn
        return conn

    async def disconnect(self, connection_id: str) -> None:
        async with self._lock:
            conn = self._connections.pop(connection_id, None)
        if conn is None:
            return
        try:
            if conn.websocket.client_state == WebSocketState.CONNECTED:
                await conn.websocket.close(code=1000)
        except Exception:
            logger.debug("hub connection close ignored: %s", connection_id, exc_info=True)

    async def touch(self, connection_id: str) -> None:
        async with self._lock:
            conn = self._connections.get(connection_id)
            if conn is not None:
                conn.last_seen_ms = int(time.time() * 1000)

    async def update_subscription(
        self,
        connection_id: str,
        *,
        markets: list[str] | None,
        symbols: list[str] | None,
        event_types: list[str] | None,
    ) -> HubConnection | None:
        async with self._lock:
            conn = self._connections.get(connection_id)
            if conn is None:
                return None
            conn.last_seen_ms = int(time.time() * 1000)
            if markets is not None:
                conn.markets = {m.lower() for m in markets if m}
            if symbols is not None:
                conn.symbols = {s.upper() for s in symbols if s}
            if event_types is not None:
                conn.event_types = {t.upper() for t in event_types if t}
            return conn

    def _matches(self, conn: HubConnection, event: dict) -> bool:
        market = str(event.get("market") or "").lower()
        symbol = str(event.get("symbol") or "").upper()
        event_type = str(event.get("type") or "").upper()

        if conn.markets and market and market not in conn.markets and "both" not in conn.markets:
            return False
        if conn.symbols and symbol and symbol not in conn.symbols:
            return False
        if conn.event_types and event_type and event_type not in conn.event_types:
            return False
        return True

    async def broadcast_event(self, event: dict) -> int:
        async with self._lock:
            targets = list(self._connections.values())

        delivered = 0
        stale: list[str] = []
        for conn in targets:
            if not self._matches(conn, event):
                continue
            try:
                await conn.websocket.send_json({"kind": "event", "data": event})
                delivered += 1
            except Exception:
                stale.append(conn.connection_id)

        for connection_id in stale:
            await self.disconnect(connection_id)
        return delivered

    async def run_heartbeat_loop(self, *, interval_sec: int, stop_event: asyncio.Event) -> None:
        interval = max(5, int(interval_sec))
        while not stop_event.is_set():
            await asyncio.sleep(interval)
            now = int(time.time() * 1000)
            async with self._lock:
                connections = list(self._connections.values())

            stale_ids: list[str] = []
            for conn in connections:
                if now - conn.last_seen_ms > self.heartbeat_timeout_ms:
                    stale_ids.append(conn.connection_id)
                    continue
                try:
                    await conn.websocket.send_json({"kind": "ping", "ts": now})
                except Exception:
                    stale_ids.append(conn.connection_id)

            for connection_id in stale_ids:
                await self.disconnect(connection_id)

    async def stats(self) -> dict[str, int]:
        async with self._lock:
            active = len(self._connections)
        return {
            "active": active,
            "max": self.max_connections,
        }
