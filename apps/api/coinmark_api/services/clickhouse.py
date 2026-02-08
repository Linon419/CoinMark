from __future__ import annotations

from dataclasses import dataclass
import json
from urllib.parse import urlparse

import httpx

from coinmark_api.config import settings


@dataclass
class ClickHouseClient:
    base_url: str
    database: str
    user: str
    password: str

    async def query_json(self, sql: str) -> list[dict]:
        params = {
            "query": f"{sql} FORMAT JSONEachRow",
            "database": self.database,
        }
        auth = (self.user, self.password) if self.user else None
        timeout = httpx.Timeout(15.0)
        async with httpx.AsyncClient(timeout=timeout) as client:
            resp = await client.get(self.base_url, params=params, auth=auth)
            resp.raise_for_status()
            rows: list[dict] = []
            for line in resp.text.splitlines():
                line = line.strip()
                if not line:
                    continue
                rows.append(json.loads(line))
            return rows


def _normalize_base_url(raw: str) -> str:
    value = (raw or "").strip()
    if not value:
        return ""
    if not value.startswith("http://") and not value.startswith("https://"):
        value = f"http://{value}"
    parsed = urlparse(value)
    scheme = parsed.scheme or "http"
    netloc = parsed.netloc or parsed.path
    if ":" not in netloc:
        netloc = f"{netloc}:8123"
    return f"{scheme}://{netloc}"


def get_clickhouse_client() -> ClickHouseClient | None:
    base = _normalize_base_url(settings.clickhouse_url)
    if not base:
        return None
    user = settings.clickhouse_user or ""
    password = settings.clickhouse_password or ""
    return ClickHouseClient(
        base_url=base,
        database=settings.clickhouse_db or "default",
        user=user,
        password=password,
    )
