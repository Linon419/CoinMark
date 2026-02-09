"""ClickHouse query helpers for tables migrated from SQLite."""
from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import Any

from coinmark_api.services.clickhouse import get_clickhouse_client, ClickHouseClient

_BUCKET_MS: dict[str, int] = {
    "1m": 60_000,
    "15m": 900_000,
    "1h": 3_600_000,
    "4h": 14_400_000,
    "1d": 86_400_000,
}


def _ch() -> ClickHouseClient:
    c = get_clickhouse_client()
    if c is None:
        raise RuntimeError("CLICKHOUSE_URL not configured")
    return c


def _esc(v: str) -> str:
    return v.replace("\\", "\\\\").replace("'", "\\'")


def _in_clause(values: list[str]) -> str:
    return ", ".join(f"'{_esc(v)}'" for v in values)


# ---------------------------------------------------------------------------
# Lightweight row wrappers (attribute-compatible with SQLAlchemy ORM models)
# ---------------------------------------------------------------------------

@dataclass(slots=True)
class TradeBucketRow:
    market: str = ""
    symbol: str = ""
    bucket: str = ""
    bucket_start_ms: int = 0
    taker_buy_notional: Decimal = field(default_factory=lambda: Decimal("0"))
    taker_sell_notional: Decimal = field(default_factory=lambda: Decimal("0"))
    quote_notional: Decimal = field(default_factory=lambda: Decimal("0"))
    trade_count: int = 0
    first_trade_ms: int | None = None
    last_trade_ms: int | None = None
    open_price: Decimal | None = None
    close_price: Decimal | None = None
    high_price: Decimal | None = None
    low_price: Decimal | None = None


@dataclass(slots=True)
class OBFeatureRow:
    market: str = ""
    symbol: str = ""
    bucket: str = ""
    bucket_start_ms: int = 0
    spread_bps_sum: float = 0.0
    depth_imbalance_l5_sum: float = 0.0
    microprice_shift_bps_sum: float = 0.0
    wall_pressure_l5_sum: float = 0.0
    depth_imbalance_l20_sum: float = 0.0
    wall_pressure_l20_sum: float = 0.0
    sample_count: int = 0
    taker_buy_notional: float = 0.0
    taker_sell_notional: float = 0.0
    depletion_events: int = 0
    replenishment_events: int = 0


@dataclass(slots=True)
class FundingRow:
    symbol: str = ""
    last_funding_rate: Decimal = field(default_factory=lambda: Decimal("0"))
    mark_price: Decimal = field(default_factory=lambda: Decimal("0"))
    event_time_ms: int = 0


@dataclass(slots=True)
class OIRow:
    symbol: str = ""
    open_interest: Decimal = field(default_factory=lambda: Decimal("0"))
    mark_price: Decimal = field(default_factory=lambda: Decimal("0"))
    oi_notional_usd: Decimal = field(default_factory=lambda: Decimal("0"))
    event_time_ms: int = 0


@dataclass(slots=True)
class MarketCapRow:
    asset: str = ""
    price_usd: Decimal = field(default_factory=lambda: Decimal("0"))
    circulating_supply: Decimal = field(default_factory=lambda: Decimal("0"))
    market_cap_usd: Decimal = field(default_factory=lambda: Decimal("0"))
    source: str = ""
    event_time_ms: int = 0


# ---------------------------------------------------------------------------
# Row builders
# ---------------------------------------------------------------------------

def _d(v: Any) -> Decimal:
    if v is None:
        return Decimal("0")
    try:
        return Decimal(str(v))
    except Exception:
        return Decimal("0")


def _d_or_none(v: Any) -> Decimal | None:
    if v is None:
        return None
    try:
        return Decimal(str(v))
    except Exception:
        return None


def _trade_row(r: dict) -> TradeBucketRow:
    return TradeBucketRow(
        market=str(r.get("market") or ""),
        symbol=str(r.get("symbol") or ""),
        bucket=str(r.get("bucket") or ""),
        bucket_start_ms=int(r.get("bucket_start_ms") or 0),
        taker_buy_notional=_d(r.get("taker_buy_notional")),
        taker_sell_notional=_d(r.get("taker_sell_notional")),
        quote_notional=_d(r.get("quote_notional")),
        trade_count=int(r.get("trade_count") or 0),
        first_trade_ms=int(r["first_trade_ms"]) if r.get("first_trade_ms") is not None else None,
        last_trade_ms=int(r["last_trade_ms"]) if r.get("last_trade_ms") is not None else None,
        open_price=_d_or_none(r.get("open_price")),
        close_price=_d_or_none(r.get("close_price")),
        high_price=_d_or_none(r.get("high_price")),
        low_price=_d_or_none(r.get("low_price")),
    )


def _ob_row(r: dict) -> OBFeatureRow:
    return OBFeatureRow(
        market=str(r.get("market") or ""),
        symbol=str(r.get("symbol") or ""),
        bucket=str(r.get("bucket") or ""),
        bucket_start_ms=int(r.get("bucket_start_ms") or 0),
        spread_bps_sum=float(r.get("spread_bps_sum") or 0),
        depth_imbalance_l5_sum=float(r.get("depth_imbalance_l5_sum") or 0),
        microprice_shift_bps_sum=float(r.get("microprice_shift_bps_sum") or 0),
        wall_pressure_l5_sum=float(r.get("wall_pressure_l5_sum") or 0),
        depth_imbalance_l20_sum=float(r.get("depth_imbalance_l20_sum") or 0),
        wall_pressure_l20_sum=float(r.get("wall_pressure_l20_sum") or 0),
        sample_count=int(r.get("sample_count") or 0),
        taker_buy_notional=float(r.get("taker_buy_notional") or 0),
        taker_sell_notional=float(r.get("taker_sell_notional") or 0),
        depletion_events=int(r.get("depletion_events") or 0),
        replenishment_events=int(r.get("replenishment_events") or 0),
    )


# ---------------------------------------------------------------------------
# Query functions
# ---------------------------------------------------------------------------

async def query_trade_buckets(
    *,
    market: str | None = None,
    symbol: str | None = None,
    symbols: list[str] | None = None,
    bucket: str,
    start_ms: int,
    end_ms: int | None = None,
    order: str = "asc",
    limit: int | None = None,
) -> list[TradeBucketRow]:
    where = ["bucket = '1m'", f"bucket_start_ms >= {int(start_ms)}"]
    if market:
        where.append(f"market = '{_esc(market)}'")
    if symbol:
        where.append(f"symbol = '{_esc(symbol)}'")
    if symbols:
        where.append(f"symbol IN ({_in_clause(symbols)})")
    if end_ms is not None:
        where.append(f"bucket_start_ms <= {int(end_ms)}")
    direction = "ASC" if order == "asc" else "DESC"
    w = " AND ".join(where)

    if bucket == "1m":
        sql = f"SELECT * FROM trade_buckets FINAL WHERE {w} ORDER BY symbol ASC, bucket_start_ms {direction}"
    else:
        bms = _BUCKET_MS.get(bucket, 60_000)
        agg = f"intDiv(bucket_start_ms, {bms}) * {bms}"
        sql = (
            f"SELECT *, agg_start AS bucket_start_ms FROM ("
            f"SELECT market, symbol, '{_esc(bucket)}' AS bucket, "
            f"{agg} AS agg_start, "
            f"sum(taker_buy_notional) AS taker_buy_notional, "
            f"sum(taker_sell_notional) AS taker_sell_notional, "
            f"sum(quote_notional) AS quote_notional, "
            f"toInt64(sum(trade_count)) AS trade_count, "
            f"min(first_trade_ms) AS first_trade_ms, "
            f"max(last_trade_ms) AS last_trade_ms, "
            f"argMin(open_price, bucket_start_ms) AS open_price, "
            f"argMax(close_price, bucket_start_ms) AS close_price, "
            f"max(high_price) AS high_price, "
            f"min(low_price) AS low_price "
            f"FROM (SELECT * FROM trade_buckets FINAL WHERE {w}) "
            f"GROUP BY market, symbol, agg_start) "
            f"ORDER BY symbol ASC, bucket_start_ms {direction}"
        )
    if limit:
        sql += f" LIMIT {int(limit)}"
    return [_trade_row(r) for r in await _ch().query_json(sql)]


async def query_trade_agg_volume(
    *,
    market: str,
    bucket: str,
    start_ms: int,
    limit: int = 200,
) -> list[tuple[str, float]]:
    sql = (
        f"SELECT symbol, sum(quote_notional) AS qv FROM trade_buckets FINAL "
        f"WHERE market = '{_esc(market)}' AND bucket = '1m' AND bucket_start_ms >= {int(start_ms)} "
        f"GROUP BY symbol ORDER BY qv DESC LIMIT {int(limit)}"
    )
    return [(str(r["symbol"]), float(r["qv"])) for r in await _ch().query_json(sql)]


async def query_trade_flow_agg(
    *,
    market: str,
    symbols: list[str],
    bucket: str,
    start_ms: int,
) -> list[tuple[str, float, float]]:
    if not symbols:
        return []
    sql = (
        f"SELECT symbol, sum(taker_buy_notional) AS buy_sum, sum(taker_sell_notional) AS sell_sum "
        f"FROM (SELECT * FROM trade_buckets FINAL WHERE market = '{_esc(market)}' AND bucket = '1m' "
        f"AND symbol IN ({_in_clause(symbols)}) AND bucket_start_ms >= {int(start_ms)}) "
        f"GROUP BY symbol"
    )
    return [(str(r["symbol"]), float(r["buy_sum"]), float(r["sell_sum"])) for r in await _ch().query_json(sql)]


async def query_orderbook_features(
    *,
    market: str | None = None,
    symbol: str | None = None,
    symbols: list[str] | None = None,
    bucket: str,
    start_ms: int,
    end_ms: int | None = None,
    order: str = "asc",
) -> list[OBFeatureRow]:
    where = [f"bucket = '{_esc(bucket)}'", f"bucket_start_ms >= {int(start_ms)}"]
    if market:
        where.append(f"market = '{_esc(market)}'")
    if symbol:
        where.append(f"symbol = '{_esc(symbol)}'")
    if symbols:
        where.append(f"symbol IN ({_in_clause(symbols)})")
    if end_ms is not None:
        where.append(f"bucket_start_ms <= {int(end_ms)}")
    direction = "ASC" if order == "asc" else "DESC"
    sql = f"SELECT * FROM orderbook_feature_buckets FINAL WHERE {' AND '.join(where)} ORDER BY symbol ASC, bucket_start_ms {direction}"
    return [_ob_row(r) for r in await _ch().query_json(sql)]


async def query_funding_snapshots() -> list[FundingRow]:
    rows = await _ch().query_json(
        "SELECT symbol, argMax(last_funding_rate, version) AS last_funding_rate, "
        "argMax(mark_price, version) AS mark_price, max(event_time_ms) AS event_time_ms "
        "FROM funding_rate_snapshots GROUP BY symbol"
    )
    return [
        FundingRow(
            symbol=str(r.get("symbol") or ""),
            last_funding_rate=_d(r.get("last_funding_rate")),
            mark_price=_d(r.get("mark_price")),
            event_time_ms=int(r.get("event_time_ms") or 0),
        )
        for r in rows
    ]


async def query_funding_by_symbol(symbol: str) -> FundingRow | None:
    rows = await _ch().query_json(
        f"SELECT * FROM funding_rate_snapshots WHERE symbol = '{_esc(symbol)}' ORDER BY version DESC LIMIT 1"
    )
    if not rows:
        return None
    r = rows[0]
    return FundingRow(
        symbol=str(r.get("symbol") or ""),
        last_funding_rate=_d(r.get("last_funding_rate")),
        mark_price=_d(r.get("mark_price")),
        event_time_ms=int(r.get("event_time_ms") or 0),
    )


async def query_oi_snapshots() -> list[OIRow]:
    rows = await _ch().query_json(
        "SELECT symbol, argMax(open_interest, version) AS open_interest, "
        "argMax(mark_price, version) AS mark_price, argMax(oi_notional_usd, version) AS oi_notional_usd, "
        "max(event_time_ms) AS event_time_ms "
        "FROM open_interest_snapshots GROUP BY symbol"
    )
    return [
        OIRow(
            symbol=str(r.get("symbol") or ""),
            open_interest=_d(r.get("open_interest")),
            mark_price=_d(r.get("mark_price")),
            oi_notional_usd=_d(r.get("oi_notional_usd")),
            event_time_ms=int(r.get("event_time_ms") or 0),
        )
        for r in rows
    ]


async def query_oi_by_symbol(symbol: str) -> OIRow | None:
    rows = await _ch().query_json(
        f"SELECT * FROM open_interest_snapshots WHERE symbol = '{_esc(symbol)}' ORDER BY version DESC LIMIT 1"
    )
    if not rows:
        return None
    r = rows[0]
    return OIRow(
        symbol=str(r.get("symbol") or ""),
        open_interest=_d(r.get("open_interest")),
        mark_price=_d(r.get("mark_price")),
        oi_notional_usd=_d(r.get("oi_notional_usd")),
        event_time_ms=int(r.get("event_time_ms") or 0),
    )


async def query_market_caps(assets: list[str] | None = None) -> list[MarketCapRow]:
    where = ""
    if assets:
        where = f" WHERE asset IN ({_in_clause(assets)})"
    sql = (
        "SELECT asset, argMax(price_usd, version) AS price_usd, "
        "argMax(circulating_supply, version) AS circulating_supply, "
        "argMax(market_cap_usd, version) AS market_cap_usd, "
        "argMax(source, version) AS source, max(event_time_ms) AS event_time_ms "
        f"FROM asset_market_caps{where} GROUP BY asset"
    )
    return [
        MarketCapRow(
            asset=str(r.get("asset") or ""),
            price_usd=_d(r.get("price_usd")),
            circulating_supply=_d(r.get("circulating_supply")),
            market_cap_usd=_d(r.get("market_cap_usd")),
            source=str(r.get("source") or ""),
            event_time_ms=int(r.get("event_time_ms") or 0),
        )
        for r in await _ch().query_json(sql)
    ]


async def query_market_cap_by_asset(asset: str) -> MarketCapRow | None:
    rows = await _ch().query_json(
        f"SELECT * FROM asset_market_caps WHERE asset = '{_esc(asset)}' ORDER BY version DESC LIMIT 1"
    )
    if not rows:
        return None
    r = rows[0]
    return MarketCapRow(
        asset=str(r.get("asset") or ""),
        price_usd=_d(r.get("price_usd")),
        circulating_supply=_d(r.get("circulating_supply")),
        market_cap_usd=_d(r.get("market_cap_usd")),
        source=str(r.get("source") or ""),
        event_time_ms=int(r.get("event_time_ms") or 0),
    )


async def insert_trade_buckets(rows: list[dict]) -> int:
    """Write trade buckets to ClickHouse (for backfill)."""
    if not rows:
        return 0
    import time as _time
    version = int(_time.time() * 1000)
    values_parts: list[str] = []
    for r in rows:
        vals = (
            f"('{_esc(str(r['market']))}','{_esc(str(r['symbol']))}','{_esc(str(r['bucket']))}',{int(r['bucket_start_ms'])},"
            f"{float(r.get('taker_buy_notional') or 0)},{float(r.get('taker_sell_notional') or 0)},"
            f"{float(r.get('quote_notional') or 0)},{int(r.get('trade_count') or 0)},"
            f"{int(r.get('first_trade_ms') or 0)},{int(r.get('last_trade_ms') or 0)},"
            f"{float(r.get('open_price') or 0)},{float(r.get('close_price') or 0)},"
            f"{float(r.get('high_price') or 0)},{float(r.get('low_price') or 0)},{version})"
        )
        values_parts.append(vals)
    sql = (
        "INSERT INTO trade_buckets "
        "(market,symbol,bucket,bucket_start_ms,"
        "taker_buy_notional,taker_sell_notional,quote_notional,trade_count,"
        "first_trade_ms,last_trade_ms,open_price,close_price,high_price,low_price,version) VALUES "
        + ",".join(values_parts)
    )
    import httpx
    c = _ch()
    timeout = httpx.Timeout(30.0)
    async with httpx.AsyncClient(timeout=timeout) as client:
        auth = (c.user, c.password) if c.user else None
        resp = await client.post(
            c.base_url,
            params={"database": c.database, "query": sql},
            auth=auth,
        )
        resp.raise_for_status()
    return len(rows)
