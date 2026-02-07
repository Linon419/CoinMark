from __future__ import annotations

import time

from sqlalchemy import text

from coinmark_api.config import settings
from coinmark_api.db import SessionLocal


def _bucket_ms(bucket: str) -> int:
    if bucket == "15m":
        return 15 * 60 * 1000
    if bucket == "1h":
        return 60 * 60 * 1000
    if bucket == "1d":
        return 24 * 60 * 60 * 1000
    raise ValueError("unsupported bucket")


def _floor_ms(ts_ms: int, bucket: str) -> int:
    size = _bucket_ms(bucket)
    return (ts_ms // size) * size


async def get_long_short_volume_rank(market: str, bucket: str, limit: int) -> dict:
    """
    返回多头/空头量能倍数排行。

    口径（严格可复算）：
    - taker_buy_notional / taker_sell_notional 来自 aggTrade 聚合落库
    - factor = current_bucket_notional / avg_notional(past_k_buckets)
    """
    now_ms = int(time.time() * 1000)
    cur_start = _floor_ms(now_ms, bucket)
    hist_start = cur_start - _bucket_ms(bucket) * settings.rank_history_buckets

    sql = text(
        """
WITH cur AS (
  SELECT symbol, taker_buy_notional, taker_sell_notional
  FROM trade_buckets
  WHERE market = :market
    AND bucket = :bucket
    AND bucket_start_ms = :cur_start
),
hist AS (
  SELECT symbol,
         AVG(taker_buy_notional) AS avg_buy,
         AVG(taker_sell_notional) AS avg_sell
  FROM trade_buckets
  WHERE market = :market
    AND bucket = :bucket
    AND bucket_start_ms >= :hist_start
    AND bucket_start_ms < :cur_start
  GROUP BY symbol
),
j AS (
  SELECT cur.symbol,
         cur.taker_buy_notional,
         cur.taker_sell_notional,
         hist.avg_buy,
         hist.avg_sell,
         CASE WHEN hist.avg_buy > 0 THEN cur.taker_buy_notional / hist.avg_buy ELSE NULL END AS long_factor,
         CASE WHEN hist.avg_sell > 0 THEN cur.taker_sell_notional / hist.avg_sell ELSE NULL END AS short_factor
  FROM cur
  JOIN hist ON hist.symbol = cur.symbol
  WHERE hist.avg_buy >= :min_avg
     OR hist.avg_sell >= :min_avg
)
SELECT symbol,
       taker_buy_notional, avg_buy, long_factor,
       taker_sell_notional, avg_sell, short_factor
FROM j
"""
    )

    async with SessionLocal() as session:
        rows = (
            await session.execute(
                sql,
                {
                    "market": market,
                    "bucket": bucket,
                    "cur_start": cur_start,
                    "hist_start": hist_start,
                    "min_avg": float(settings.rank_min_avg_notional),
                },
            )
        ).mappings().all()

    if not rows:
        # 冷启动：历史窗口不足时，先返回“当前桶绝对成交额排行”，factor 为空（不做近似）
        fallback_sql = text(
            """
SELECT symbol, taker_buy_notional, taker_sell_notional
FROM trade_buckets
WHERE market = :market
  AND bucket = :bucket
  AND bucket_start_ms = :cur_start
"""
        )
        async with SessionLocal() as session:
            cur_rows = (
                await session.execute(
                    fallback_sql,
                    {"market": market, "bucket": bucket, "cur_start": cur_start},
                )
            ).mappings().all()

        longs = sorted(cur_rows, key=lambda r: r["taker_buy_notional"], reverse=True)[:limit]
        shorts = sorted(cur_rows, key=lambda r: r["taker_sell_notional"], reverse=True)[:limit]
        return {
            "market": market,
            "bucket": bucket,
            "bucketStartMs": cur_start,
            "long": [
                {
                    "symbol": r["symbol"],
                    "factor": None,
                    "currentNotional": float(r["taker_buy_notional"]),
                    "avgNotional": None,
                }
                for r in longs
            ],
            "short": [
                {
                    "symbol": r["symbol"],
                    "factor": None,
                    "currentNotional": float(r["taker_sell_notional"]),
                    "avgNotional": None,
                }
                for r in shorts
            ],
        }

    longs = []
    shorts = []
    for r in rows:
        if r["long_factor"] is not None:
            longs.append(
                {
                    "symbol": r["symbol"],
                    "factor": float(r["long_factor"]),
                    "currentNotional": float(r["taker_buy_notional"]),
                    "avgNotional": float(r["avg_buy"]),
                }
            )
        if r["short_factor"] is not None:
            shorts.append(
                {
                    "symbol": r["symbol"],
                    "factor": float(r["short_factor"]),
                    "currentNotional": float(r["taker_sell_notional"]),
                    "avgNotional": float(r["avg_sell"]),
                }
            )

    longs.sort(key=lambda x: x["factor"], reverse=True)
    shorts.sort(key=lambda x: x["factor"], reverse=True)

    return {
        "market": market,
        "bucket": bucket,
        "bucketStartMs": cur_start,
        "long": longs[:limit],
        "short": shorts[:limit],
    }
