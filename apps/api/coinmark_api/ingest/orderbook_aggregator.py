from __future__ import annotations

import asyncio
from dataclasses import dataclass
from decimal import Decimal

from coinmark_api.ingest.buckets import bucket_ms, floor_bucket_start_ms


@dataclass(frozen=True, slots=True)
class OrderbookBucketKey:
    market: str
    symbol: str
    bucket: str
    bucket_start_ms: int


@dataclass(slots=True)
class OrderbookBucketDelta:
    spread_bps_sum: Decimal = Decimal("0")
    depth_imbalance_l5_sum: Decimal = Decimal("0")
    microprice_shift_bps_sum: Decimal = Decimal("0")
    wall_pressure_l5_sum: Decimal = Decimal("0")
    sample_count: int = 0

    taker_buy_notional: Decimal = Decimal("0")
    taker_sell_notional: Decimal = Decimal("0")

    depletion_events: int = 0
    replenishment_events: int = 0

    l1_depth_baseline: Decimal | None = None
    depletion_active: bool = False

    def add_orderbook_sample(
        self,
        *,
        spread_bps: Decimal,
        depth_imbalance_l5: Decimal,
        microprice_shift_bps: Decimal,
        wall_pressure_l5: Decimal,
        l1_depth_notional: Decimal,
    ) -> None:
        self.spread_bps_sum += spread_bps
        self.depth_imbalance_l5_sum += depth_imbalance_l5
        self.microprice_shift_bps_sum += microprice_shift_bps
        self.wall_pressure_l5_sum += wall_pressure_l5
        self.sample_count += 1

        if self.l1_depth_baseline is None:
            self.l1_depth_baseline = l1_depth_notional
            return

        baseline = self.l1_depth_baseline
        if baseline <= 0:
            self.l1_depth_baseline = l1_depth_notional
            return

        drop_threshold = baseline * Decimal("0.60")
        recover_threshold = baseline * Decimal("0.85")

        if (not self.depletion_active) and l1_depth_notional < drop_threshold:
            self.depletion_events += 1
            self.depletion_active = True
        elif self.depletion_active and l1_depth_notional >= recover_threshold:
            self.replenishment_events += 1
            self.depletion_active = False

        self.l1_depth_baseline = baseline * Decimal("0.98") + l1_depth_notional * Decimal("0.02")

    def add_trade(self, *, taker_buy_notional: Decimal, taker_sell_notional: Decimal) -> None:
        self.taker_buy_notional += taker_buy_notional
        self.taker_sell_notional += taker_sell_notional


class OrderbookAggregator:
    def __init__(self, bucket: str = "1m") -> None:
        self._bucket = bucket
        self._bucket_size_ms = bucket_ms(bucket)
        self._lock = asyncio.Lock()
        self._deltas: dict[OrderbookBucketKey, OrderbookBucketDelta] = {}

    async def add_orderbook_sample(
        self,
        *,
        market: str,
        symbol: str,
        ts_ms: int,
        spread_bps: Decimal,
        depth_imbalance_l5: Decimal,
        microprice_shift_bps: Decimal,
        wall_pressure_l5: Decimal,
        l1_depth_notional: Decimal,
    ) -> None:
        key = OrderbookBucketKey(
            market=market,
            symbol=symbol,
            bucket=self._bucket,
            bucket_start_ms=floor_bucket_start_ms(ts_ms, self._bucket),
        )
        async with self._lock:
            delta = self._deltas.get(key)
            if delta is None:
                delta = OrderbookBucketDelta()
                self._deltas[key] = delta
            delta.add_orderbook_sample(
                spread_bps=spread_bps,
                depth_imbalance_l5=depth_imbalance_l5,
                microprice_shift_bps=microprice_shift_bps,
                wall_pressure_l5=wall_pressure_l5,
                l1_depth_notional=l1_depth_notional,
            )

    async def add_trade(
        self,
        *,
        market: str,
        symbol: str,
        ts_ms: int,
        taker_buy_notional: Decimal,
        taker_sell_notional: Decimal,
    ) -> None:
        key = OrderbookBucketKey(
            market=market,
            symbol=symbol,
            bucket=self._bucket,
            bucket_start_ms=floor_bucket_start_ms(ts_ms, self._bucket),
        )
        async with self._lock:
            delta = self._deltas.get(key)
            if delta is None:
                delta = OrderbookBucketDelta()
                self._deltas[key] = delta
            delta.add_trade(taker_buy_notional=taker_buy_notional, taker_sell_notional=taker_sell_notional)

    async def drain_closed(self, now_ms: int) -> list[tuple[OrderbookBucketKey, OrderbookBucketDelta]]:
        cutoff = floor_bucket_start_ms(now_ms, self._bucket)
        async with self._lock:
            closed_keys = [k for k in self._deltas.keys() if k.bucket_start_ms < cutoff]
            out: list[tuple[OrderbookBucketKey, OrderbookBucketDelta]] = []
            for key in closed_keys:
                delta = self._deltas.pop(key)
                out.append((key, delta))
            return out

