from __future__ import annotations

import logging

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from coinmark_api.api.router import router as api_router
from coinmark_api.config import settings
from coinmark_api.models import Base
from coinmark_api.db import engine
from coinmark_api.migrations import migrate
from coinmark_api.hub import start_hub_runtime, stop_hub_runtime


logger = logging.getLogger("coinmark")


def create_app() -> FastAPI:
    app = FastAPI(title="CoinMark API", version="0.1.0")
    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=False,
        allow_methods=["*"],
        allow_headers=["*"],
    )
    app.include_router(api_router)

    @app.on_event("startup")
    async def _startup() -> None:
        await migrate(engine)
        async with engine.begin() as conn:
            await conn.run_sync(Base.metadata.create_all)
        await start_hub_runtime()

    @app.on_event("shutdown")
    async def _shutdown() -> None:
        await stop_hub_runtime()

    return app


app = create_app()


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(
        "coinmark_api.main:app",
        host=settings.api_host,
        port=settings.api_port,
        log_level=settings.api_log_level,
        reload=False,
    )
