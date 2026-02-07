from __future__ import annotations

import time
from dataclasses import dataclass
from decimal import Decimal


def bucket_ms(bucket: str) -> int:
    if bucket == "1m":
        return 60 * 1000
    if bucket == "15m":
        return 15 * 60 * 1000
    if bucket == "1h":
        return 60 * 60 * 1000
    if bucket == "4h":
        return 4 * 60 * 60 * 1000
    if bucket == "1d":
        return 24 * 60 * 60 * 1000
    raise ValueError("unsupported bucket")


def floor_bucket_start_ms(ts_ms: int, bucket: str) -> int:
    size = bucket_ms(bucket)
    return (ts_ms // size) * size


@dataclass(frozen=True, slots=True)
class BucketKey:
    market: str
    symbol: str
    bucket: str
    bucket_start_ms: int


@dataclass(slots=True)
class BucketDelta:
    taker_buy_notional: Decimal = Decimal("0")
    taker_sell_notional: Decimal = Decimal("0")
    quote_notional: Decimal = Decimal("0")
    trade_count: int = 0
    first_trade_ms: int | None = None
    last_trade_ms: int | None = None
    open_price: Decimal | None = None
    close_price: Decimal | None = None
    high_price: Decimal | None = None
    low_price: Decimal | None = None

    def add(
        self,
        *,
        ts_ms: int,
        price: Decimal,
        buy: Decimal,
        sell: Decimal,
        total: Decimal,
        count: int = 1,
    ) -> None:
        self.taker_buy_notional += buy
        self.taker_sell_notional += sell
        self.quote_notional += total
        self.trade_count += count

        if self.first_trade_ms is None or ts_ms < self.first_trade_ms:
            self.first_trade_ms = ts_ms
            self.open_price = price
        if self.last_trade_ms is None or ts_ms > self.last_trade_ms:
            self.last_trade_ms = ts_ms
            self.close_price = price
        if self.high_price is None or price > self.high_price:
            self.high_price = price
        if self.low_price is None or price < self.low_price:
            self.low_price = price


def utc_now_ms() -> int:
    return int(time.time() * 1000)
