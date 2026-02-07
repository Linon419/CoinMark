from __future__ import annotations

from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.dialects.sqlite import insert as sqlite_insert

from coinmark_api.db import dialect_name


def insert(table):
    name = dialect_name()
    if name == "postgresql":
        return pg_insert(table)
    if name == "sqlite":
        return sqlite_insert(table)
    # 其他方言暂不支持 ON CONFLICT，后续可扩展
    raise RuntimeError(f"不支持的数据库方言：{name}")

