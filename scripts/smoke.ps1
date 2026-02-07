$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $here "..")
$api = Join-Path $root "apps\\api"

Set-Location $api

$env:DATABASE_URL = "sqlite+aiosqlite:///./_smoke.db"
$env:REDIS_URL = "redis://localhost:6379/0"

Write-Host "运行单元测试..."
.\.venv\Scripts\pytest -q

Write-Host "运行 WS + DB smoke（抓取 btcusdt@aggTrade 5 条消息并落库）..."
@'
import asyncio
import json
from decimal import Decimal

import websockets
from sqlalchemy import select

from coinmark_api.db import engine, SessionLocal
from coinmark_api.db_upsert import insert
from coinmark_api.ingest.aggregator import TradeAggregator
from coinmark_api.ingest.ws import _ws_base
from coinmark_api.models import Base, TradeBucket


async def main():
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.drop_all)
        await conn.run_sync(Base.metadata.create_all)

    agg = TradeAggregator(buckets=["15m"])
    url = _ws_base("spot") + "?streams=btcusdt@aggTrade"
    async with websockets.connect(url, ping_interval=20, ping_timeout=20, close_timeout=5) as ws:
        for _ in range(5):
            msg = await ws.recv()
            payload = json.loads(msg)
            data = payload.get("data", {})
            price = Decimal(str(data["p"]))
            qty = Decimal(str(data["q"]))
            notional = price * qty
            is_buyer_maker = bool(data["m"])
            buy = Decimal("0") if is_buyer_maker else notional
            sell = notional if is_buyer_maker else Decimal("0")
            await agg.add_trade(
                market="spot",
                symbol=data["s"],
                ts_ms=int(data["T"]),
                price=price,
                taker_buy_notional=buy,
                taker_sell_notional=sell,
                quote_notional=notional,
            )

    drained = await agg.drain()
    values = []
    for key, d in drained:
        values.append(
            {
                "market": key.market,
                "symbol": key.symbol,
                "bucket": key.bucket,
                "bucket_start_ms": key.bucket_start_ms,
                "taker_buy_notional": d.taker_buy_notional,
                "taker_sell_notional": d.taker_sell_notional,
                "quote_notional": d.quote_notional,
                "trade_count": d.trade_count,
            }
        )

    stmt = insert(TradeBucket).values(values)
    stmt = stmt.on_conflict_do_update(
        index_elements=["market", "symbol", "bucket", "bucket_start_ms"],
        set_={
            "taker_buy_notional": TradeBucket.taker_buy_notional + stmt.excluded.taker_buy_notional,
            "taker_sell_notional": TradeBucket.taker_sell_notional + stmt.excluded.taker_sell_notional,
            "quote_notional": TradeBucket.quote_notional + stmt.excluded.quote_notional,
            "trade_count": TradeBucket.trade_count + stmt.excluded.trade_count,
        },
    )

    async with SessionLocal() as session:
        await session.execute(stmt)
        await session.commit()
        rows = (await session.execute(select(TradeBucket))).scalars().all()
        print("rows=", len(rows))
        if rows:
            r = rows[0]
            print(r.market, r.symbol, r.bucket, r.trade_count, float(r.quote_notional))


asyncio.run(main())
'@ | .\.venv\Scripts\python -
