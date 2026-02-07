from __future__ import annotations

from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker, create_async_engine

from coinmark_api.config import settings


def _create_engine() -> AsyncEngine:
    url = settings.database_url
    if url.startswith("sqlite"):
        return create_async_engine(url, pool_pre_ping=True)
    return create_async_engine(
        url,
        pool_pre_ping=True,
        pool_size=10,
        max_overflow=20,
    )


engine: AsyncEngine = _create_engine()

SessionLocal = async_sessionmaker(engine, expire_on_commit=False, class_=AsyncSession)


def dialect_name() -> str:
    return engine.dialect.name
