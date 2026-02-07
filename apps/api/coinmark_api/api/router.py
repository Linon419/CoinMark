from __future__ import annotations

from fastapi import APIRouter

from coinmark_api.api.routes.health import router as health_router
from coinmark_api.api.routes.bot import router as bot_router
from coinmark_api.api.routes.symbols import router as symbols_router
from coinmark_api.api.routes.aggregate import router as aggregate_router
from coinmark_api.api.routes.user import router as user_router
from coinmark_api.api.routes.anomaly import router as anomaly_router
from coinmark_api.api.routes.coin import router as coin_router
from coinmark_api.api.routes.hub import router as hub_router


router = APIRouter()
router.include_router(health_router)
router.include_router(symbols_router, prefix="/api")
router.include_router(aggregate_router, prefix="/api")
router.include_router(anomaly_router, prefix="/api")
router.include_router(coin_router, prefix="/api")
router.include_router(hub_router, prefix="/api")
router.include_router(user_router, prefix="/api")
router.include_router(bot_router, prefix="/api/bot")
