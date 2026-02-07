from __future__ import annotations

import json
import logging
import time

from fastapi import APIRouter, WebSocket, WebSocketDisconnect

from coinmark_api.config import settings
from coinmark_api.hub import hub_connection_manager

logger = logging.getLogger("coinmark.hub")
router = APIRouter()


def _now_ms() -> int:
    return int(time.time() * 1000)


def _to_list(value: object) -> list[str] | None:
    if value is None:
        return None
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [str(v) for v in value if v is not None]
    return None


@router.websocket("/hub/market")
async def market_hub(websocket: WebSocket) -> None:
    if not settings.hub_enabled:
        await websocket.close(code=1001, reason="hub disabled")
        return

    origin = websocket.headers.get("origin")
    if not hub_connection_manager.is_origin_allowed(origin):
        await websocket.close(code=1008, reason="origin not allowed")
        return

    conn = await hub_connection_manager.connect(websocket)
    if conn is None:
        return

    await websocket.send_json(
        {
            "kind": "connected",
            "connectionId": conn.connection_id,
            "ts": _now_ms(),
        }
    )

    try:
        while True:
            payload = await websocket.receive_text()
            await hub_connection_manager.touch(conn.connection_id)

            try:
                data = json.loads(payload)
            except json.JSONDecodeError:
                await websocket.send_json({"kind": "error", "message": "invalid json"})
                continue

            if not isinstance(data, dict):
                continue

            op = str(data.get("op") or "").lower()
            if op == "ping":
                await websocket.send_json({"kind": "pong", "ts": _now_ms()})
                continue

            if op == "subscribe":
                markets = _to_list(data.get("markets"))
                symbols = _to_list(data.get("symbols"))
                event_types = _to_list(data.get("types"))
                updated = await hub_connection_manager.update_subscription(
                    conn.connection_id,
                    markets=markets,
                    symbols=symbols,
                    event_types=event_types,
                )
                if updated is None:
                    return
                await websocket.send_json(
                    {
                        "kind": "subscribed",
                        "markets": sorted(updated.markets),
                        "symbols": sorted(updated.symbols),
                        "types": sorted(updated.event_types),
                        "ts": _now_ms(),
                    }
                )
                continue

    except WebSocketDisconnect:
        pass
    except Exception:
        logger.exception("hub websocket loop failed")
    finally:
        await hub_connection_manager.disconnect(conn.connection_id)
