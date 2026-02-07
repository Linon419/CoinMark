from __future__ import annotations

import asyncio
from collections import defaultdict
from decimal import Decimal
from typing import Iterable

from coinmark_api.ingest.buckets import BucketDelta, BucketKey, floor_bucket_start_ms


class TradeAggregator:
    def __init__(self, buckets: Iterable[str]) -> None:
        self._buckets = list(buckets)
        self._lock = asyncio.Lock()
        self._deltas: dict[BucketKey, BucketDelta] = defaultdict(BucketDelta)

    async def add_trade(
        self,
        *,
        market: str,
        symbol: str,
        ts_ms: int,
        price: Decimal,
        taker_buy_notional: Decimal,
        taker_sell_notional: Decimal,
        quote_notional: Decimal,
        trade_count: int = 1,
    ) -> None:
        async with self._lock:
            for b in self._buckets:
                key = BucketKey(
                    market=market,
                    symbol=symbol,
                    bucket=b,
                    bucket_start_ms=floor_bucket_start_ms(ts_ms, b),
                )
                self._deltas[key].add(
                    ts_ms=ts_ms,
                    price=price,
                    buy=taker_buy_notional,
                    sell=taker_sell_notional,
                    total=quote_notional,
                    count=trade_count,
                )

    async def drain(self) -> list[tuple[BucketKey, BucketDelta]]:
        async with self._lock:
            items = list(self._deltas.items())
            self._deltas = defaultdict(BucketDelta)
            return items
