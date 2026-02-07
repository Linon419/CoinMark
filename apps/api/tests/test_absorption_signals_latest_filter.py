import os
import time

import pytest
from sqlalchemy import text

os.environ.setdefault("DATABASE_URL", "postgresql+asyncpg://coinmark:coinmark@localhost:5432/coinmark")
os.environ.setdefault("REDIS_URL", "redis://localhost:6379/0")

from coinmark_api.db import SessionLocal
from coinmark_api.models import AbsorptionSignalSnapshot
from coinmark_api.services.absorption_signal import list_latest_absorption_signals


@pytest.mark.asyncio
async def test_list_latest_absorption_signals_only_signals_keeps_latest_trigger_in_window():
    market = "swap"
    symbol = "__UTESTABSUSDT"
    direction = "LONG_BIAS"

    async with SessionLocal() as session:
        await session.execute(
            text(
                """
                DELETE FROM absorption_signal_snapshots
                WHERE market = :m AND symbol = :s AND direction = :d
                """
            ),
            {"m": market, "s": symbol, "d": direction},
        )

        now_ms = int(time.time() * 1000)
        old_ms = now_ms - 60_000
        session.add_all(
            [
                AbsorptionSignalSnapshot(
                    market=market,
                    symbol=symbol,
                    bucket_start_ms=old_ms,
                    direction=direction,
                    signal_state="WATCH",
                    score=70,
                    net_flow_strength=1.0,
                    impact_per_notional=0.1,
                    window_4h_passed=True,
                    window_1d_passed=False,
                    window_3d_passed=False,
                    windows={},
                    reasons=[],
                ),
                AbsorptionSignalSnapshot(
                    market=market,
                    symbol=symbol,
                    bucket_start_ms=now_ms,
                    direction=direction,
                    signal_state="NONE",
                    score=0,
                    net_flow_strength=0.0,
                    impact_per_notional=0.0,
                    window_4h_passed=False,
                    window_1d_passed=False,
                    window_3d_passed=False,
                    windows={},
                    reasons=[],
                ),
            ]
        )
        await session.commit()

    rows = await list_latest_absorption_signals(
        market=market,
        only_signals=True,
        limit=200,
        signal_lookback_minutes=60,
        direction="long",
    )

    row_map = {str(r.symbol): r for r in rows}
    assert symbol in row_map
    assert str(row_map[symbol].signal_state) == "WATCH"

    async with SessionLocal() as session:
        await session.execute(
            text(
                """
                DELETE FROM absorption_signal_snapshots
                WHERE market = :m AND symbol = :s AND direction = :d
                """
            ),
            {"m": market, "s": symbol, "d": direction},
        )
        await session.commit()
