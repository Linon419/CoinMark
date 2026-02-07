from __future__ import annotations

import hashlib
import time
from typing import Any, Literal

from pydantic import BaseModel, Field


def now_ms() -> int:
    return int(time.time() * 1000)


class HubEvent(BaseModel):
    id: str
    type: str
    level: Literal["info", "warning", "critical"] = "info"
    title: str
    content: str = ""
    symbol: str | None = None
    market: Literal["spot", "swap", "both"] | None = None
    ts: int = Field(default_factory=now_ms)
    meta: dict[str, Any] = Field(default_factory=dict)
    dedupe_key: str | None = None


def build_event_id(*parts: object) -> str:
    raw = "|".join(str(p) for p in parts)
    digest = hashlib.sha1(raw.encode("utf-8")).hexdigest()[:16]
    return f"evt_{digest}"
