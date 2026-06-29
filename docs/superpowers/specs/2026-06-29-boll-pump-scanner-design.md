# Low Volatility Bollinger Pump Scanner Design

## Goal

Build a Binance USDT perpetual scanner that detects post-pump Bollinger lower-band rebound structures across `1m`, `3m`, `5m`, `15m`, `30m`, and `1h`.

The tool scans, scores, stores history, exposes API data, renders a frontend workspace, and sends Telegram alerts. Execution remains the user's responsibility.

## Core Strategy

The scanner uses an **up-move first** workflow:

1. Detect a volume-backed upward move.
2. Look back before the move and score whether the market had a quiet low-volatility base.
3. Enter `WATCH` after the up-move.
4. Wait for Bollinger lower-band pullback candidates.
5. Confirm the first and second valid rebounds as `CONFIRM_1` and `CONFIRM_2`.

Low-volatility context improves signal quality and ranking. The up-move is the trigger.

## Market Scope

- Market: Binance USDT perpetuals, represented by existing `market=swap`.
- Symbols: Binance trading USDT symbols after existing project exclusions such as stablecoin bases.
- 24h quote volume: retained as a data field and scoring input.
- Thin liquidity handling: retain the signal, subtract 15 points when `quote_volume_24h < 2,000,000 USDT`, and add a reason tag.

## Timeframes

Scan all of:

- `1m`
- `3m`
- `5m`
- `15m`
- `30m`
- `1h`

Each `symbol + timeframe` owns its own state machine.

## Indicator Parameters

Default Bollinger parameters:

```text
period = 20
stddev_multiplier = 2
middle = SMA(close, 20)
upper = middle + 2 * STD(close, 20)
lower = middle - 2 * STD(close, 20)
bandwidth = (upper - lower) / middle
```

ATR:

```text
ATR14 = simple rolling average of true range over 14 candles
ATR ratio = ATR14 / close
```

Use the same simple rolling ATR method in scanner logic, API detail payloads, and tests.

## WATCH Trigger

`WATCH` means the scanner has already seen an upward start and is waiting for a lower-band pullback.

The trigger uses a timeframe-specific startup window:

```text
1m  = 12 candles
3m  = 10 candles
5m  = 8 candles
15m = 6 candles
30m = 5 candles
1h  = 4 candles
```

Within the startup window:

- Cumulative gain reaches the timeframe threshold.
- At least one candle reaches the timeframe volume ratio threshold.
- Current close is at or above Bollinger middle.
- Current close is near upper band, or current high breaks upper band.
- Bollinger bandwidth expands.
- Bollinger direction is upward: `middle_slope >= 0` and `close >= middle`.
- Large bearish volume candle rules reject the trigger.

Gain thresholds:

```text
1m  >= 2.0%
3m  >= 2.5%
5m  >= 3.0%
15m >= 4.0%
30m >= 5.0%
1h  >= 6.0%
```

Volume thresholds:

```text
1m  >= 5.0x 20-candle average volume
3m  >= 3.0x
5m  >= 2.5x
15m >= 2.0x
30m >= 1.8x
1h  >= 1.5x
```

Upper-band proximity:

```text
close >= upper - 0.15 * (upper - lower)
OR high >= upper
```

Large bearish volume candle filter:

```text
close < open
AND abs(close - open) >= 0.6 * (high - low)
```

The confirmation candle for `WATCH` should close strong:

```text
close >= open
OR (close - low) / (high - low) >= 0.60
```

## Low-Volatility Background Score

The quiet-base check looks back from before the startup window:

```text
background_window = 80 candles ending before startup_window begins
```

This background adds up to 20 points:

```text
bandwidth low percentile: +5
low volume: +5
middle-area consolidation: +5
quiet ATR: +5
```

Bandwidth low percentile:

```text
current/background bandwidth <= 30th percentile of background bandwidth values
```

Low volume:

```text
In the latest 10 background candles, at least 7 have:
volume < 0.8 * rolling 20-candle average volume
```

Middle-area consolidation:

```text
abs(close - middle) / middle <= 0.35 * bandwidth
```

The same latest 10 background candles use the `7 of 10` rule.

Quiet ATR:

```text
ATR14 / close <= 40th percentile of background ATR ratios
```

## State Machine

State is persisted per `market + symbol + timeframe`.

```text
IDLE
WATCH
PULLBACK_1_PENDING
CONFIRM_1
PULLBACK_2_PENDING
CONFIRM_2
COMPLETED
EXPIRED
INVALIDATED
```

Public signal levels:

```text
WATCH
CONFIRM_1
CONFIRM_2
```

Transitions:

```text
IDLE -> WATCH
WATCH -> PULLBACK_1_PENDING -> CONFIRM_1
CONFIRM_1 -> PULLBACK_2_PENDING -> CONFIRM_2
CONFIRM_2 -> COMPLETED
WATCH/CONFIRM_1/PENDING -> EXPIRED after 60 candles without next confirmation
Any active state -> INVALIDATED when structural failure rules trigger
```

`WATCH -> CONFIRM_1` expiry:

```text
60 candles
```

`CONFIRM_1 -> CONFIRM_2` expiry:

```text
60 candles
```

States older than 7 days without updates should be marked `EXPIRED`.

## Pullback and Rebound Confirmation

A pullback candidate uses that candle's own Bollinger lower band:

```text
low <= lower
close > lower
```

Before the first pullback candidate, after `WATCH`, at least 3 candles must close at or above `middle`.

Confirmation waits until one later candle satisfies:

```text
high > pullback_candidate.high
```

The wait can continue until phase expiry. During the wait:

- If `close < lower`, the candidate is invalid.
- If another candle satisfies `low <= lower` and `close > lower`, it replaces the previous pullback candidate.

`CONFIRM_2` must use a fresh pullback candidate after `CONFIRM_1` has already confirmed.

Second rebound low filter:

```text
second_low < first_low - max(ATR14, first_low * 0.015)
```

When this condition holds, the second rebound is invalid.

## Scoring

Score is capped at 100.

`WATCH` score:

```text
base = 55
volume ratio reached: +10
cumulative gain reached: +10
BOLL expands upward: +10
upper-band proximity/break: +5
low-volatility background: +0 to +20
thin 24h quote volume: -15
```

`CONFIRM_1` and `CONFIRM_2` inherit the `WATCH` score:

```text
CONFIRM_1 = min(100, watch_score + 10)
CONFIRM_2 = min(100, watch_score + 20)
```

Second low is weak but still valid:

```text
subtract 10 points
```

Priority score:

```text
priority_score = min(100, score + confluence_score)
```

## Multi-Timeframe Handling

State remains independent per `symbol + timeframe`.

History stores every signal from every timeframe.

Telegram alerts aggregate by symbol:

- First qualifying high-priority signal sends immediately.
- A 10-minute aggregation window tracks same-symbol signals.
- A confluence upgrade alert sends when the same symbol receives:
  - a higher timeframe signal at the same level, or
  - a higher signal level.

Timeframe priority:

```text
1h > 30m > 15m > 5m > 3m > 1m
```

Signal level priority:

```text
CONFIRM_2 > CONFIRM_1 > WATCH
```

Confluence score:

```text
1 additional timeframe with same/higher level: +3
2 additional timeframes: +6
3 or more additional timeframes: +8
highest confluence timeframe >= 30m: +2
cap = +10
```

`WATCH` participates in frontend confluence display. Telegram uses score thresholds.

## Telegram Rules

Telegram push thresholds:

```text
WATCH >= 70
CONFIRM_1 >= 75
CONFIRM_2 >= 80
```

Message format:

```text
CONFIRM_2 XYZUSDT 15m price=0.1234 vol=2.7x bw=0.034 score=92 confluence=6
reason: volume-backed pump, quiet base +20, second lower-band confirm
```

The scanner stores all signals in `boll_pump_signals`. Telegram-eligible signals also insert mapped `anomaly_events` with event type `boll_pump`, so the existing Telegram polling loop can deliver them through the current notification path.

## Persistence

Create dedicated tables for scanner state and signal history.

State table:

```text
boll_pump_states
- id
- market
- symbol
- timeframe
- status
- watch_started_ms
- watch_candle_start_ms
- watch_score
- current_score
- confluence_score
- priority_score
- bounce_count
- first_pullback_low
- second_pullback_low
- pending_pullback_candle_ms
- pending_pullback_high
- last_checked_candle_ms
- last_signal_level
- last_alert_ms
- expires_at_candle_ms
- details_json
- created_at
- updated_at
unique(market, symbol, timeframe)
```

Signal table:

```text
boll_pump_signals
- id
- market
- symbol
- timeframe
- signal_level
- price
- volume_ratio
- boll_bandwidth
- bounce_count
- score
- confluence_score
- priority_score
- signal_time_ms
- candle_start_ms
- watch_candle_start_ms
- pullback_candle_start_ms
- quote_volume_24h
- reason
- details_json
- created_at
```

Indexes:

```text
boll_pump_signals(market, signal_time_ms desc)
boll_pump_signals(market, symbol, signal_time_ms desc)
boll_pump_signals(market, signal_level, signal_time_ms desc)
boll_pump_states(market, status, updated_at desc)
boll_pump_states(market, symbol, timeframe)
```

Retention:

- Signal history: 30 days.
- State rows: retained, with 7-day stale active states marked `EXPIRED`.

## Data Source and Scheduling

First implementation uses Binance REST klines through existing `binance.Client.GetKlines`.

Use a data-source interface so a future implementation can switch selected timeframes to ClickHouse 1m aggregation.

Scheduler:

- Run inside existing `hub.Runtime`.
- Scan each timeframe after its closed candle boundary.
- Use global token bucket and bounded worker pool.
- Defaults: 20 workers, 20 requests per second, 45 second scan timeout, small boundary jitter.

The scanner should use only closed candles.

## API Surface

Add endpoints under the existing API service:

```text
GET /boll-pump/signals
GET /boll-pump/states
GET /boll-pump/stats
GET /boll-pump/signals/:id/detail
```

`signals` supports:

- market
- symbol
- timeframe
- signal_level
- min_score
- since
- limit

`states` supports:

- market
- symbol
- timeframe
- status
- min_priority_score

`stats` returns:

- scanner run status
- last scan time per timeframe
- symbol count
- error count
- duration per timeframe
- signal counts by level/timeframe
- performance windows for 1h, 4h, 24h

Performance stats use signal confirmation close as `entry_ref_price`:

```text
max_gain
max_drawdown
close_return
```

`detail` returns:

- signal record
- state snapshot
- candles from 120 before to 120 after the signal
- Bollinger values
- ATR values
- markers for `WATCH`, pullback candidate, `CONFIRM_1`, `CONFIRM_2`

## Frontend Workspace

Add a full workspace page with:

- Recent signals table.
- Active states table.
- Stats panel.
- Detail drawer.
- Kline chart with Bollinger upper/middle/lower, ATR, and markers.
- Multi-timeframe confluence display per symbol.

Default detail view opens the highest-priority timeframe. Other active timeframes for the same symbol are listed alongside it.

## Configuration

Expose parameters through environment-backed config and display them read-only in the frontend.

Core config groups:

- scanner enabled
- scan market
- timeframes
- startup windows
- gain thresholds
- volume thresholds
- Bollinger period and multiplier
- ATR period
- low-volatility scoring thresholds
- score thresholds
- confluence window
- request rate limit and worker count
- retention days

## Validation Plan

Unit tests:

- Bollinger calculation.
- ATR calculation.
- Startup trigger per timeframe.
- Low-volatility scoring.
- Pullback candidate replacement.
- Confirmation wait until invalidation or expiry.
- `CONFIRM_2` second-low invalidation.
- Score cap and confluence score.

Service tests:

- State transition from `IDLE` to `WATCH`.
- State transition to `CONFIRM_1`.
- State transition to `CONFIRM_2`.
- Expiry after 60 candles.
- Stale state marking after 7 days.
- Signal insert and dedupe behavior.

API tests:

- Signals list filters.
- States list filters.
- Stats shape.
- Detail candle payload shape.

Frontend verification:

- Workspace renders tables and stats.
- Detail drawer renders Bollinger chart and markers.
- Mobile and desktop layouts avoid overlapping controls.

## Key Risks

- REST request pressure across many symbols and six timeframes.
- Thin-liquidity symbols can produce visually valid but fragile patterns.
- 3m, 5m, and 30m scanning requires careful closed-candle boundary handling.
- Performance statistics may lag until enough future candles exist.
- Telegram aggregation must preserve timely first alerts while still issuing confluence upgrades.
