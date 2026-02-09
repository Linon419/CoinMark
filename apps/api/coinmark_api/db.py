from __future__ import annotations

import asyncio
from contextlib import asynccontextmanager
from typing import AsyncIterator

from sqlalchemy import event
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker, create_async_engine

from coinmark_api.config import settings


def _set_sqlite_pragma(dbapi_conn: object, _rec: object) -> None:
    cursor = dbapi_conn.cursor()  # type: ignore[union-attr]
    cursor.execute("PRAGMA journal_mode=WAL")
    cursor.execute("PRAGMA synchronous=NORMAL")
    cursor.close()


def _create_engine() -> AsyncEngine:
    url = settings.database_url
    if url.startswith("sqlite"):
        eng = create_async_engine(
            url,
            pool_pre_ping=True,
            connect_args={"timeout": 30},
        )
        event.listen(eng.sync_engine, "connect", _set_sqlite_pragma)
        return eng
    return create_async_engine(
        url,
        pool_pre_ping=True,
        pool_size=10,
        max_overflow=20,
    )


engine: AsyncEngine = _create_engine()

SessionLocal = async_sessionmaker(engine, expire_on_commit=False, class_=AsyncSession)

_sqlite_write_lock = asyncio.Lock()


@asynccontextmanager
async def write_session() -> AsyncIterator[AsyncSession]:
    """Yield session with serialized writes for SQLite (WAL allows only 1 writer)."""
    if engine.dialect.name == "sqlite":
        async with _sqlite_write_lock:
            async with SessionLocal() as session:
                yield session
    else:
        async with SessionLocal() as session:
            yield session


def dialect_name() -> str:
    return engine.dialect.name
