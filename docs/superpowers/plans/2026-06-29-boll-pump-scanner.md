# Low Volatility Bollinger Pump Scanner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Binance USDT perpetual Bollinger pump scanner from `docs/superpowers/specs/2026-06-29-boll-pump-scanner-design.md`.

**Architecture:** Implement the scanner in `apps/api-go/internal/service` as pure indicator/state logic plus a runtime scanner that uses Binance REST klines. Persist scanner state and signal history in SQLite, map Telegram-eligible signals to `anomaly_events`, expose Gin endpoints, and render a React workspace with ECharts detail views.

**Tech Stack:** Go, Gin, SQLite/sqlx, existing Binance client, existing hub runtime, existing Telegram notifier, React, Arco Design, ECharts, Vite.

---

## Scope Check

This feature touches backend scanning, persistence, notification, API, and frontend display. These pieces share the same `boll_pump` data contract and should ship as one vertical feature so every stored signal can be viewed, queried, and alerted consistently.

## File Structure

Create backend scanner files:

- `apps/api-go/internal/service/bollpump_types.go`: shared constants, config, bar, indicator, signal, state, and detail structs.
- `apps/api-go/internal/service/bollpump_indicators.go`: Bollinger, ATR, percentile, candle helpers.
- `apps/api-go/internal/service/bollpump_engine.go`: `WATCH`, pullback, confirmation, scoring, confluence logic.
- `apps/api-go/internal/service/bollpump_store.go`: SQLite persistence helpers and anomaly-event mapping.
- `apps/api-go/internal/service/bollpump_scanner.go`: Binance REST data source, worker pool, timeframe scan orchestration.

Create backend tests:

- `apps/api-go/internal/service/bollpump_indicators_test.go`
- `apps/api-go/internal/service/bollpump_engine_test.go`
- `apps/api-go/internal/service/bollpump_store_test.go`
- `apps/api-go/internal/service/bollpump_scanner_test.go`
- `apps/api-go/internal/handler/boll_pump_test.go`

Modify backend files:

- `apps/api-go/internal/model/model.go`: add `BollPumpState`, `BollPumpSignal`, and detail payload structs.
- `apps/api-go/internal/migration/migrate.go`: add `boll_pump_states` and `boll_pump_signals` tables and indexes.
- `apps/api-go/internal/migration/migrate_test.go`: verify migration idempotency and new table existence.
- `apps/api-go/internal/config/config.go`: add environment-backed Bollinger scanner config.
- `apps/api-go/internal/hub/runtime.go`: start the scanner loop and include cleanup.
- `apps/api-go/internal/handler/router.go`: register Bollinger scanner routes.
- `apps/api-go/internal/handler/boll_pump.go`: list/detail/stats handlers.
- `apps/api-go/internal/telegram/format.go`: label and severity for `boll_pump`.
- `apps/api-go/internal/telegram/notify.go`: detail line formatting for `boll_pump`.
- `apps/api-go/internal/telegram/notify_test.go`: poll and formatting coverage for `boll_pump`.

Create frontend files:

- `apps/web/src/pages/BollPumpPage.tsx`: workspace page.
- `apps/web/src/services/bollPump.ts`: API client and response types.
- `apps/web/src/components/BollPumpChart.tsx`: ECharts candlestick/BOLL/detail chart.

Modify frontend files:

- `apps/web/src/main.tsx`: add `/boll-pump` route.
- `apps/web/src/App.tsx`: add navigation item.
- `apps/web/src/styles/app.css`: add compact workspace layout styles.

## Task 1: Indicator Core

**Files:**
- Create: `apps/api-go/internal/service/bollpump_types.go`
- Create: `apps/api-go/internal/service/bollpump_indicators.go`
- Create: `apps/api-go/internal/service/bollpump_indicators_test.go`

- [ ] **Step 1: Write failing indicator tests**

Create `apps/api-go/internal/service/bollpump_indicators_test.go`:

```go
package service

import (
	"math"
	"testing"
)

func TestBollPumpIndicatorsUseClosedBars(t *testing.T) {
	bars := make([]BollPumpBar, 0, 25)
	for i := 0; i < 25; i++ {
		close := 100 + float64(i)
		bars = append(bars, BollPumpBar{
			OpenTimeMs: int64(i) * 60000,
			CloseTimeMs: int64(i+1)*60000 - 1,
			Open: close - 0.5,
			High: close + 1,
			Low: close - 1,
			Close: close,
			Volume: 100,
			QuoteVolume: 1000,
			Closed: true,
		})
	}

	out := ComputeBollPumpIndicators(bars, 20, 2, 14)
	last := out[len(out)-1]

	if math.Abs(last.Middle-114.5) > 1e-9 {
		t.Fatalf("middle = %.10f, want 114.5", last.Middle)
	}
	if math.Abs(last.Upper-126.0325625947) > 1e-6 {
		t.Fatalf("upper = %.10f, want 126.0325625947", last.Upper)
	}
	if math.Abs(last.Lower-102.9674374053) > 1e-6 {
		t.Fatalf("lower = %.10f, want 102.9674374053", last.Lower)
	}
	if math.Abs(last.Bandwidth-0.2014430148) > 1e-6 {
		t.Fatalf("bandwidth = %.10f, want 0.2014430148", last.Bandwidth)
	}
	if math.Abs(last.ATR14-2.0) > 1e-9 {
		t.Fatalf("atr = %.10f, want 2.0", last.ATR14)
	}
}

func TestBollPumpPercentile(t *testing.T) {
	got := bollPumpPercentile([]float64{5, 1, 3, 2, 4}, 0.30)
	if math.Abs(got-2.2) > 1e-9 {
		t.Fatalf("percentile = %.10f, want 2.2", got)
	}
}
```

- [ ] **Step 2: Run the indicator tests and observe the expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/service -run 'TestBollPump(Indicators|Percentile)' -count=1
```

Expected: compile failure mentioning missing `BollPumpBar`, `ComputeBollPumpIndicators`, or `bollPumpPercentile`.

- [ ] **Step 3: Implement indicator types and functions**

Create `apps/api-go/internal/service/bollpump_types.go` with these public fields and constants:

```go
package service

type BollPumpSignalLevel string
type BollPumpStateStatus string

const (
	BollPumpLevelWatch    BollPumpSignalLevel = "WATCH"
	BollPumpLevelConfirm1 BollPumpSignalLevel = "CONFIRM_1"
	BollPumpLevelConfirm2 BollPumpSignalLevel = "CONFIRM_2"

	BollPumpStatusIdle             BollPumpStateStatus = "IDLE"
	BollPumpStatusWatch            BollPumpStateStatus = "WATCH"
	BollPumpStatusPullback1Pending BollPumpStateStatus = "PULLBACK_1_PENDING"
	BollPumpStatusConfirm1         BollPumpStateStatus = "CONFIRM_1"
	BollPumpStatusPullback2Pending BollPumpStateStatus = "PULLBACK_2_PENDING"
	BollPumpStatusConfirm2         BollPumpStateStatus = "CONFIRM_2"
	BollPumpStatusCompleted        BollPumpStateStatus = "COMPLETED"
	BollPumpStatusExpired          BollPumpStateStatus = "EXPIRED"
	BollPumpStatusInvalidated      BollPumpStateStatus = "INVALIDATED"
)

type BollPumpBar struct {
	OpenTimeMs   int64
	CloseTimeMs  int64
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       float64
	QuoteVolume  float64
	Closed       bool
}

type BollPumpIndicator struct {
	Middle      float64
	Upper       float64
	Lower       float64
	Bandwidth   float64
	ATR14       float64
	ATRRatio    float64
	ValidBoll   bool
	ValidATR    bool
}
```

Create `apps/api-go/internal/service/bollpump_indicators.go` with:

```go
package service

import (
	"math"
	"sort"
)

func ComputeBollPumpIndicators(bars []BollPumpBar, bollPeriod int, stdMult float64, atrPeriod int) []BollPumpIndicator {
	out := make([]BollPumpIndicator, len(bars))
	for i := range bars {
		if bollPeriod > 0 && i+1 >= bollPeriod {
			start := i + 1 - bollPeriod
			sum := 0.0
			for j := start; j <= i; j++ {
				sum += bars[j].Close
			}
			middle := sum / float64(bollPeriod)
			variance := 0.0
			for j := start; j <= i; j++ {
				d := bars[j].Close - middle
				variance += d * d
			}
			std := math.Sqrt(variance / float64(bollPeriod))
			upper := middle + stdMult*std
			lower := middle - stdMult*std
			bw := 0.0
			if middle != 0 {
				bw = (upper - lower) / middle
			}
			out[i].Middle = middle
			out[i].Upper = upper
			out[i].Lower = lower
			out[i].Bandwidth = bw
			out[i].ValidBoll = true
		}
		if atrPeriod > 0 && i+1 >= atrPeriod {
			start := i + 1 - atrPeriod
			sumTR := 0.0
			for j := start; j <= i; j++ {
				sumTR += bollPumpTrueRange(bars, j)
			}
			atr := sumTR / float64(atrPeriod)
			out[i].ATR14 = atr
			if bars[i].Close > 0 {
				out[i].ATRRatio = atr / bars[i].Close
			}
			out[i].ValidATR = true
		}
	}
	return out
}

func bollPumpTrueRange(bars []BollPumpBar, idx int) float64 {
	hl := bars[idx].High - bars[idx].Low
	if idx == 0 {
		return hl
	}
	prevClose := bars[idx-1].Close
	hc := math.Abs(bars[idx].High - prevClose)
	lc := math.Abs(bars[idx].Low - prevClose)
	return math.Max(hl, math.Max(hc, lc))
}

func bollPumpPercentile(values []float64, p float64) float64 {
	clean := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return 0
	}
	sort.Float64s(clean)
	if p <= 0 {
		return clean[0]
	}
	if p >= 1 {
		return clean[len(clean)-1]
	}
	pos := p * float64(len(clean)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return clean[lo]
	}
	frac := pos - float64(lo)
	return clean[lo]*(1-frac) + clean[hi]*frac
}
```

- [ ] **Step 4: Run indicator tests**

Run:

```bash
cd apps/api-go
go test ./internal/service -run 'TestBollPump(Indicators|Percentile)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/internal/service/bollpump_types.go apps/api-go/internal/service/bollpump_indicators.go apps/api-go/internal/service/bollpump_indicators_test.go
git commit -m "feat: 添加BOLL扫描指标计算"
```

## Task 2: SQLite Schema and Store Layer

**Files:**
- Modify: `apps/api-go/internal/migration/migrate.go`
- Modify: `apps/api-go/internal/migration/migrate_test.go`
- Modify: `apps/api-go/internal/model/model.go`
- Create: `apps/api-go/internal/service/bollpump_store.go`
- Create: `apps/api-go/internal/service/bollpump_store_test.go`

- [ ] **Step 1: Write migration test**

Append to `apps/api-go/internal/migration/migrate_test.go`:

```go
func TestMigrateCreatesBollPumpTables(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate sqlite twice: %v", err)
	}

	for _, table := range []string{"boll_pump_states", "boll_pump_signals"} {
		var count int
		if err := store.GetContext(ctx, &count, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
}
```

- [ ] **Step 2: Run migration test and observe the expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/migration -run TestMigrateCreatesBollPumpTables -count=1
```

Expected: FAIL because the new tables are absent.

- [ ] **Step 3: Add schema DDL**

Append these DDL entries to `schemaDDL` in `apps/api-go/internal/migration/migrate.go`:

```go
`CREATE TABLE IF NOT EXISTS boll_pump_states (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	market VARCHAR(8) NOT NULL,
	symbol VARCHAR(32) NOT NULL,
	timeframe VARCHAR(8) NOT NULL,
	status VARCHAR(32) NOT NULL,
	watch_started_ms BIGINT,
	watch_candle_start_ms BIGINT,
	watch_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	current_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	confluence_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	priority_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	bounce_count INTEGER NOT NULL DEFAULT 0,
	first_pullback_low NUMERIC(38,18),
	second_pullback_low NUMERIC(38,18),
	pending_pullback_candle_ms BIGINT,
	pending_pullback_high NUMERIC(38,18),
	last_checked_candle_ms BIGINT,
	last_signal_level VARCHAR(16),
	last_alert_ms BIGINT,
	expires_at_candle_ms BIGINT,
	details JSON NOT NULL DEFAULT '{}',
	created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (market, symbol, timeframe)
)`,
`CREATE INDEX IF NOT EXISTS ix_boll_pump_states_status ON boll_pump_states (market, status, updated_at DESC)`,
`CREATE INDEX IF NOT EXISTS ix_boll_pump_states_symbol ON boll_pump_states (market, symbol, timeframe)`,
`CREATE TABLE IF NOT EXISTS boll_pump_signals (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	market VARCHAR(8) NOT NULL,
	symbol VARCHAR(32) NOT NULL,
	timeframe VARCHAR(8) NOT NULL,
	signal_level VARCHAR(16) NOT NULL,
	price NUMERIC(38,18) NOT NULL,
	volume_ratio NUMERIC(18,8) NOT NULL DEFAULT 0,
	boll_bandwidth NUMERIC(18,8) NOT NULL DEFAULT 0,
	bounce_count INTEGER NOT NULL DEFAULT 0,
	score NUMERIC(10,4) NOT NULL DEFAULT 0,
	confluence_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	priority_score NUMERIC(10,4) NOT NULL DEFAULT 0,
	signal_time_ms BIGINT NOT NULL,
	candle_start_ms BIGINT NOT NULL,
	watch_candle_start_ms BIGINT,
	pullback_candle_start_ms BIGINT,
	quote_volume_24h NUMERIC(38,18) NOT NULL DEFAULT 0,
	perf_1h_max_gain NUMERIC(18,8),
	perf_1h_max_drawdown NUMERIC(18,8),
	perf_1h_close_return NUMERIC(18,8),
	perf_4h_max_gain NUMERIC(18,8),
	perf_4h_max_drawdown NUMERIC(18,8),
	perf_4h_close_return NUMERIC(18,8),
	perf_24h_max_gain NUMERIC(18,8),
	perf_24h_max_drawdown NUMERIC(18,8),
	perf_24h_close_return NUMERIC(18,8),
	performance_updated_ms BIGINT,
	reason VARCHAR(512) NOT NULL,
	details JSON NOT NULL DEFAULT '{}',
	created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
)`,
`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_time ON boll_pump_signals (market, signal_time_ms DESC)`,
`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_symbol ON boll_pump_signals (market, symbol, signal_time_ms DESC)`,
`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_level ON boll_pump_signals (market, signal_level, signal_time_ms DESC)`,
```

- [ ] **Step 4: Add model structs**

Add to `apps/api-go/internal/model/model.go`:

```go
type BollPumpState struct {
	ID                       int64   `db:"id" json:"id"`
	Market                   string  `db:"market" json:"market"`
	Symbol                   string  `db:"symbol" json:"symbol"`
	Timeframe                string  `db:"timeframe" json:"timeframe"`
	Status                   string  `db:"status" json:"status"`
	WatchStartedMs           *int64  `db:"watch_started_ms" json:"watch_started_ms"`
	WatchCandleStartMs       *int64  `db:"watch_candle_start_ms" json:"watch_candle_start_ms"`
	WatchScore               float64 `db:"watch_score" json:"watch_score"`
	CurrentScore             float64 `db:"current_score" json:"current_score"`
	ConfluenceScore          float64 `db:"confluence_score" json:"confluence_score"`
	PriorityScore            float64 `db:"priority_score" json:"priority_score"`
	BounceCount              int     `db:"bounce_count" json:"bounce_count"`
	FirstPullbackLow         *float64 `db:"first_pullback_low" json:"first_pullback_low"`
	SecondPullbackLow        *float64 `db:"second_pullback_low" json:"second_pullback_low"`
	PendingPullbackCandleMs  *int64  `db:"pending_pullback_candle_ms" json:"pending_pullback_candle_ms"`
	PendingPullbackHigh      *float64 `db:"pending_pullback_high" json:"pending_pullback_high"`
	LastCheckedCandleMs      *int64  `db:"last_checked_candle_ms" json:"last_checked_candle_ms"`
	LastSignalLevel          *string `db:"last_signal_level" json:"last_signal_level"`
	LastAlertMs              *int64  `db:"last_alert_ms" json:"last_alert_ms"`
	ExpiresAtCandleMs        *int64  `db:"expires_at_candle_ms" json:"expires_at_candle_ms"`
	Details                  JSONB   `db:"details" json:"details"`
	CreatedAt                *string `db:"created_at" json:"-"`
	UpdatedAt                *string `db:"updated_at" json:"-"`
}

type BollPumpSignal struct {
	ID                    int64   `db:"id" json:"id"`
	Market                string  `db:"market" json:"market"`
	Symbol                string  `db:"symbol" json:"symbol"`
	Timeframe             string  `db:"timeframe" json:"timeframe"`
	SignalLevel           string  `db:"signal_level" json:"signal_level"`
	Price                 float64 `db:"price" json:"price"`
	VolumeRatio           float64 `db:"volume_ratio" json:"volume_ratio"`
	BollBandwidth         float64 `db:"boll_bandwidth" json:"boll_bandwidth"`
	BounceCount           int     `db:"bounce_count" json:"bounce_count"`
	Score                 float64 `db:"score" json:"score"`
	ConfluenceScore       float64 `db:"confluence_score" json:"confluence_score"`
	PriorityScore         float64 `db:"priority_score" json:"priority_score"`
	SignalTimeMs          int64   `db:"signal_time_ms" json:"signal_time_ms"`
	CandleStartMs         int64   `db:"candle_start_ms" json:"candle_start_ms"`
	WatchCandleStartMs    *int64  `db:"watch_candle_start_ms" json:"watch_candle_start_ms"`
	PullbackCandleStartMs *int64  `db:"pullback_candle_start_ms" json:"pullback_candle_start_ms"`
	QuoteVolume24h        float64 `db:"quote_volume_24h" json:"quote_volume_24h"`
	Perf1hMaxGain         *float64 `db:"perf_1h_max_gain" json:"perf_1h_max_gain"`
	Perf1hMaxDrawdown     *float64 `db:"perf_1h_max_drawdown" json:"perf_1h_max_drawdown"`
	Perf1hCloseReturn     *float64 `db:"perf_1h_close_return" json:"perf_1h_close_return"`
	Perf4hMaxGain         *float64 `db:"perf_4h_max_gain" json:"perf_4h_max_gain"`
	Perf4hMaxDrawdown     *float64 `db:"perf_4h_max_drawdown" json:"perf_4h_max_drawdown"`
	Perf4hCloseReturn     *float64 `db:"perf_4h_close_return" json:"perf_4h_close_return"`
	Perf24hMaxGain        *float64 `db:"perf_24h_max_gain" json:"perf_24h_max_gain"`
	Perf24hMaxDrawdown    *float64 `db:"perf_24h_max_drawdown" json:"perf_24h_max_drawdown"`
	Perf24hCloseReturn    *float64 `db:"perf_24h_close_return" json:"perf_24h_close_return"`
	PerformanceUpdatedMs  *int64   `db:"performance_updated_ms" json:"performance_updated_ms"`
	Reason                string  `db:"reason" json:"reason"`
	Details               JSONB   `db:"details" json:"details"`
	CreatedAt             *string `db:"created_at" json:"-"`
}
```

- [ ] **Step 5: Write store round-trip test**

Create `apps/api-go/internal/service/bollpump_store_test.go` with:

```go
package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestSaveAndListBollPumpSignal(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	sig := model.BollPumpSignal{
		Market: "swap", Symbol: "XYZUSDT", Timeframe: "15m",
		SignalLevel: "WATCH", Price: 0.1234, VolumeRatio: 2.7,
		BollBandwidth: 0.034, Score: 75, PriorityScore: 75,
		SignalTimeMs: time.Now().UnixMilli(), CandleStartMs: 60000,
		Reason: "volume-backed pump",
		Details: model.JSONB(`{"score":75}`),
	}
	if _, err := SaveBollPumpSignal(ctx, store, sig, false); err != nil {
		t.Fatalf("save signal: %v", err)
	}
	rows, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Limit: 10})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(rows) != 1 || rows[0].Symbol != "XYZUSDT" || rows[0].SignalLevel != "WATCH" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func openBollPumpTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migration.Migrate(context.Background(), store); err != nil {
		store.Close()
		t.Fatalf("migrate sqlite: %v", err)
	}
	return store
}
```

- [ ] **Step 6: Implement store helpers**

Create `apps/api-go/internal/service/bollpump_store.go` with these exported functions:

```go
type BollPumpSignalFilter struct {
	Market string
	Symbol string
	Timeframe string
	SignalLevel string
	MinScore float64
	SinceMs int64
	Limit int
}

type BollPumpStateFilter struct {
	Market string
	Symbol string
	Timeframe string
	Status string
	MinPriorityScore float64
	Limit int
}

func SaveBollPumpState(ctx context.Context, store *sqlite.Store, st model.BollPumpState) error
func GetBollPumpState(ctx context.Context, store *sqlite.Store, market, symbol, timeframe string) (*model.BollPumpState, error)
func ListBollPumpStates(ctx context.Context, store *sqlite.Store, f BollPumpStateFilter) ([]model.BollPumpState, error)
func SaveBollPumpSignal(ctx context.Context, store *sqlite.Store, sig model.BollPumpSignal, insertAnomaly bool) (int64, error)
func ListBollPumpSignals(ctx context.Context, store *sqlite.Store, f BollPumpSignalFilter) ([]model.BollPumpSignal, error)
func CleanupBollPumpSignals(ctx context.Context, store *sqlite.Store, retentionDays int) (int64, error)
func ExpireStaleBollPumpStates(ctx context.Context, store *sqlite.Store, staleDays int) (int64, error)
```

Use `store.Write` for writes. Build SELECT filters by appending fixed clauses and args. Clamp `Limit` to `200` when unset and `1000` max.

- [ ] **Step 7: Run store and migration tests**

Run:

```bash
cd apps/api-go
go test ./internal/migration ./internal/service -run 'TestMigrateCreatesBollPumpTables|TestSaveAndListBollPumpSignal' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add apps/api-go/internal/migration/migrate.go apps/api-go/internal/migration/migrate_test.go apps/api-go/internal/model/model.go apps/api-go/internal/service/bollpump_store.go apps/api-go/internal/service/bollpump_store_test.go
git commit -m "feat: 添加BOLL扫描存储表"
```

## Task 3: Startup Detection and Scoring Engine

**Files:**
- Modify: `apps/api-go/internal/service/bollpump_types.go`
- Create: `apps/api-go/internal/service/bollpump_engine.go`
- Create: `apps/api-go/internal/service/bollpump_engine_test.go`

- [ ] **Step 1: Write startup scoring test**

Create `apps/api-go/internal/service/bollpump_engine_test.go` with:

```go
package service

import (
	"testing"

	"coinmark/api-go/internal/model"
)

func TestBollPumpWatchTriggerScoresQuietBase(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if !got.Triggered {
		t.Fatalf("Triggered = false, want true; reasons=%v", got.Reasons)
	}
	if got.Signal.SignalLevel != string(BollPumpLevelWatch) {
		t.Fatalf("level = %s, want WATCH", got.Signal.SignalLevel)
	}
	if got.Signal.Score < 85 {
		t.Fatalf("score = %.2f, want >= 85", got.Signal.Score)
	}
}

func TestBollPumpWatchTriggerRejectsLargeBearishStart(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	last := len(bars) - 1
	bars[last].Open = bars[last].High
	bars[last].Close = bars[last].Low
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if got.Triggered {
		t.Fatalf("Triggered = true, want false")
	}
}
```

Add fixture helpers in the same test file:

```go
func bollPumpFixtureQuietBaseThenPump(tf string) []BollPumpBar {
	bars := make([]BollPumpBar, 0, 140)
	for i := 0; i < 120; i++ {
		close := 100 + float64(i%3-1)*0.05
		bars = append(bars, BollPumpBar{
			OpenTimeMs: int64(i) * 60000,
			CloseTimeMs: int64(i+1)*60000 - 1,
			Open: close - 0.01, High: close + 0.08, Low: close - 0.08, Close: close,
			Volume: 80, QuoteVolume: 8000, Closed: true,
		})
	}
	pumps := []float64{100.5, 101.2, 102.4, 103.6, 104.2, 104.5}
	for i, close := range pumps {
		vol := 120.0
		if i == 2 {
			vol = 600
		}
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs: int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open: close - 0.4, High: close + 0.6, Low: close - 0.5, Close: close,
			Volume: vol, QuoteVolume: vol * close, Closed: true,
		})
	}
	return bars
}
```

- [ ] **Step 2: Run startup tests and observe expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/service -run TestBollPumpWatchTrigger -count=1
```

Expected: compile failure for missing `DefaultBollPumpConfig` and `EvaluateBollPumpWatch`.

- [ ] **Step 3: Implement config and startup evaluation**

Extend `apps/api-go/internal/service/bollpump_types.go`:

```go
type BollPumpConfig struct {
	Enabled bool
	Market string
	Timeframes []string
	BollPeriod int
	BollStdDev float64
	ATRPeriod int
	StartupWindows map[string]int
	GainThresholds map[string]float64
	VolumeThresholds map[string]float64
	BackgroundLookback int
	BackgroundRecentWindow int
	BackgroundRecentMinPass int
	LowVolumeFactor float64
	MiddleNearBandwidthFactor float64
	ThinQuoteVolume24h float64
	WatchTelegramThreshold float64
	Confirm1TelegramThreshold float64
	Confirm2TelegramThreshold float64
	ConfluenceWindowMs int64
}

type BollPumpWatchResult struct {
	Triggered bool
	Signal model.BollPumpSignal
	BackgroundScore float64
	Reasons []string
}
```

Create `apps/api-go/internal/service/bollpump_engine.go` with:

```go
func DefaultBollPumpConfig() BollPumpConfig
func EvaluateBollPumpWatch(market, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64, cfg BollPumpConfig) BollPumpWatchResult
func bollPumpStartupWindow(timeframe string, cfg BollPumpConfig) int
func bollPumpGainThreshold(timeframe string, cfg BollPumpConfig) float64
func bollPumpVolumeThreshold(timeframe string, cfg BollPumpConfig) float64
func bollPumpScoreCap(v float64) float64
func bollPumpCloseStrong(b BollPumpBar) bool
func bollPumpLargeBearish(b BollPumpBar) bool
func bollPumpUpperProximity(b BollPumpBar, in BollPumpIndicator) bool
func bollPumpBackgroundScore(bars []BollPumpBar, ind []BollPumpIndicator, startupStart int, cfg BollPumpConfig) (float64, []string)
```

Default values must match the design spec:

```text
timeframes: 1m,3m,5m,15m,30m,1h
startup windows: 12,10,8,6,5,4
gain thresholds: 0.020,0.025,0.030,0.040,0.050,0.060
volume thresholds: 5.0,3.0,2.5,2.0,1.8,1.5
background lookback: 80
recent background window: 10
recent min pass: 7
low volume factor: 0.8
middle near factor: 0.35
thin quote volume: 2000000
```

- [ ] **Step 4: Run startup tests**

Run:

```bash
cd apps/api-go
go test ./internal/service -run TestBollPumpWatchTrigger -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/internal/service/bollpump_types.go apps/api-go/internal/service/bollpump_engine.go apps/api-go/internal/service/bollpump_engine_test.go
git commit -m "feat: 实现BOLL启动评分"
```

## Task 4: Stateful Confirmation Engine

**Files:**
- Modify: `apps/api-go/internal/service/bollpump_engine.go`
- Modify: `apps/api-go/internal/service/bollpump_engine_test.go`

- [ ] **Step 1: Write state transition tests**

Append to `apps/api-go/internal/service/bollpump_engine_test.go`:

```go
func TestBollPumpConfirmFlowWaitsUntilBreaksPullbackHigh(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureWatchThenTwoConfirms()
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "15m")
	var signals []model.BollPumpSignal
	for i := 0; i < len(bars); i++ {
		out := AdvanceBollPumpState(&state, bars[:i+1], ind[:i+1], 3_000_000, cfg)
		signals = append(signals, out.Signals...)
	}

	if len(signals) < 3 {
		t.Fatalf("signals = %d, want at least WATCH, CONFIRM_1, CONFIRM_2", len(signals))
	}
	if signals[len(signals)-1].SignalLevel != string(BollPumpLevelConfirm2) {
		t.Fatalf("last signal = %s, want CONFIRM_2", signals[len(signals)-1].SignalLevel)
	}
	if state.Status != string(BollPumpStatusCompleted) {
		t.Fatalf("status = %s, want COMPLETED", state.Status)
	}
}

func TestBollPumpSecondLowInvalidation(t *testing.T) {
	firstLow := 100.0
	atr := 2.0
	if !bollPumpSecondLowInvalid(firstLow, 97.5, atr) {
		t.Fatalf("second low should be invalid")
	}
	if bollPumpSecondLowInvalid(firstLow, 98.2, atr) {
		t.Fatalf("second low should remain valid")
	}
}
```

Add a fixture that creates:

```text
quiet base -> WATCH pump -> 3 closes above middle -> pullback candidate -> later high break -> second pullback candidate -> later high break
```

Use deterministic values. Set each pullback candle low to `90`, close to `103`, and high to `104`; set the later confirmation candle high to `104.5`. With the quiet-base fixture, this creates `low <= lower`, `close > lower`, then `high > pullback_candidate.high`.

- [ ] **Step 2: Run confirmation tests and observe expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/service -run 'TestBollPump(ConfirmFlow|SecondLow)' -count=1
```

Expected: compile failure for missing state functions.

- [ ] **Step 3: Implement runtime state and transition functions**

Add these structs and functions:

```go
type BollPumpRuntimeState struct {
	Market string
	Symbol string
	Timeframe string
	Status string
	WatchScore float64
	CurrentScore float64
	WatchCandleStartMs int64
	WatchStartedMs int64
	BounceCount int
	FirstPullbackLow float64
	SecondPullbackLow float64
	PendingPullbackCandleMs int64
	PendingPullbackHigh float64
	ExpiresAtCandleMs int64
	LastCheckedCandleMs int64
}

type BollPumpAdvanceResult struct {
	State BollPumpRuntimeState
	Signals []model.BollPumpSignal
}

func NewBollPumpRuntimeState(market, symbol, timeframe string) BollPumpRuntimeState
func AdvanceBollPumpState(state *BollPumpRuntimeState, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64, cfg BollPumpConfig) BollPumpAdvanceResult
func bollPumpSecondLowInvalid(firstLow, secondLow, atr14 float64) bool
func bollPumpStageExpired(state BollPumpRuntimeState, latest BollPumpBar) bool
func bollPumpHasThreeMiddleClosesAfterWatch(state BollPumpRuntimeState, bars []BollPumpBar, ind []BollPumpIndicator) bool
```

Transition rules:

```text
IDLE -> WATCH when EvaluateBollPumpWatch triggers
WATCH -> PULLBACK_1_PENDING when low <= lower and close > lower after three middle closes
PULLBACK_1_PENDING -> CONFIRM_1 when later high > pending high
CONFIRM_1 -> PULLBACK_2_PENDING on fresh lower-band candidate
PULLBACK_2_PENDING -> COMPLETED after CONFIRM_2 signal
close < lower while pending clears the pending candidate
stage expires after 60 timeframe candles
```

- [ ] **Step 4: Run state tests**

Run:

```bash
cd apps/api-go
go test ./internal/service -run 'TestBollPump(ConfirmFlow|SecondLow|WatchTrigger|Indicators|Percentile)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api-go/internal/service/bollpump_engine.go apps/api-go/internal/service/bollpump_engine_test.go
git commit -m "feat: 实现BOLL确认状态机"
```

## Task 5: Scanner Data Source, Config, and Runtime Loop

**Files:**
- Modify: `apps/api-go/internal/config/config.go`
- Modify: `apps/api-go/internal/hub/runtime.go`
- Create: `apps/api-go/internal/service/bollpump_scanner.go`
- Create: `apps/api-go/internal/service/bollpump_scanner_test.go`

- [ ] **Step 1: Write scanner orchestration test**

Create `apps/api-go/internal/service/bollpump_scanner_test.go`:

```go
package service

import (
	"context"
	"testing"
)

type fakeBollPumpSource struct {
	bars map[string][]BollPumpBar
	quote map[string]float64
}

func (f fakeBollPumpSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	return []string{"XYZUSDT"}, nil
}

func (f fakeBollPumpSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	return f.bars[timeframe], nil
}

func (f fakeBollPumpSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	return f.quote[symbol], nil
}

func TestBollPumpScannerScansOneTimeframe(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	source := fakeBollPumpSource{
		bars: map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenPump("15m")},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, nil, cfg)

	result := scanner.ScanTimeframe(context.Background(), "15m")
	if result.SymbolsScanned != 1 {
		t.Fatalf("symbols scanned = %d, want 1", result.SymbolsScanned)
	}
	if result.SignalsFound == 0 {
		t.Fatalf("signals found = 0, want > 0")
	}
}
```

- [ ] **Step 2: Run scanner test and observe expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/service -run TestBollPumpScannerScansOneTimeframe -count=1
```

Expected: compile failure for missing scanner types.

- [ ] **Step 3: Add config fields**

Add fields to `config.Config`:

```go
BollPumpEnabled bool
BollPumpMarket string
BollPumpTimeframes string
BollPumpSymbolLimit int
BollPumpWorkers int
BollPumpRateLimitRPS int
BollPumpScanTimeoutSec int
BollPumpJitterSec int
BollPumpRetentionDays int
```

Load values:

```go
BollPumpEnabled: getenvBool("BOLL_PUMP_ENABLED", true),
BollPumpMarket: getenv("BOLL_PUMP_MARKET", "swap"),
BollPumpTimeframes: getenv("BOLL_PUMP_TIMEFRAMES", "1m,3m,5m,15m,30m,1h"),
BollPumpSymbolLimit: getenvInt("BOLL_PUMP_SYMBOL_LIMIT", 200),
BollPumpWorkers: getenvInt("BOLL_PUMP_WORKERS", 20),
BollPumpRateLimitRPS: getenvInt("BOLL_PUMP_RATE_LIMIT_RPS", 20),
BollPumpScanTimeoutSec: getenvInt("BOLL_PUMP_SCAN_TIMEOUT_SEC", 45),
BollPumpJitterSec: getenvInt("BOLL_PUMP_JITTER_SEC", 5),
BollPumpRetentionDays: getenvInt("BOLL_PUMP_RETENTION_DAYS", 30),
```

- [ ] **Step 4: Implement scanner**

Create `apps/api-go/internal/service/bollpump_scanner.go` with:

```go
type BollPumpSource interface {
	Symbols(ctx context.Context, market string, limit int) ([]string, error)
	Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error)
	QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error)
}

type BollPumpScanner struct {
	source BollPumpSource
	store *sqlite.Store
	cfg BollPumpConfig
}

type BollPumpScanResult struct {
	Timeframe string
	SymbolsScanned int
	SignalsFound int
	Errors int
	StartedAtMs int64
	FinishedAtMs int64
}

func NewBollPumpScanner(source BollPumpSource, store *sqlite.Store, cfg BollPumpConfig) *BollPumpScanner
func (s *BollPumpScanner) ScanTimeframe(ctx context.Context, timeframe string) BollPumpScanResult
func (s *BollPumpScanner) Run(ctx context.Context, stopCh <-chan struct{})
func NewBinanceBollPumpSource(bn *binance.Client, symbolLimit int) BollPumpSource
```

The Binance source converts `[][]interface{}` klines into `BollPumpBar` using existing Binance array positions:

```text
0 open time
1 open
2 high
3 low
4 close
5 volume
6 close time
7 quote volume
```

Use `limit = background lookback + max startup window + 20 BOLL + 60 confirmation + 10 buffer`, with minimum `200`.

- [ ] **Step 5: Wire runtime**

In `apps/api-go/internal/hub/runtime.go`, inside `Start` after other scanner gates:

```go
if rt.cfg.BollPumpEnabled && rt.bn != nil {
	source := service.NewBinanceBollPumpSource(rt.bn, rt.cfg.BollPumpSymbolLimit)
	scanner := service.NewBollPumpScanner(source, rt.store, service.DefaultBollPumpConfig())
	go scanner.Run(ctx, rt.stopCh)
}
```

Pass config-derived scanner settings into `DefaultBollPumpConfig()` through a helper such as `service.BollPumpConfigFromRuntime(rt.cfg)` to avoid hard-coded runtime values.

- [ ] **Step 6: Run scanner tests**

Run:

```bash
cd apps/api-go
go test ./internal/service ./internal/hub -run 'TestBollPumpScannerScansOneTimeframe|TestRuntime' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/api-go/internal/config/config.go apps/api-go/internal/hub/runtime.go apps/api-go/internal/service/bollpump_scanner.go apps/api-go/internal/service/bollpump_scanner_test.go
git commit -m "feat: 接入BOLL扫描运行循环"
```

## Task 6: API Endpoints and Performance Detail

**Files:**
- Create: `apps/api-go/internal/handler/boll_pump.go`
- Create: `apps/api-go/internal/handler/boll_pump_test.go`
- Modify: `apps/api-go/internal/handler/router.go`
- Modify: `apps/api-go/internal/service/bollpump_store.go`

- [ ] **Step 1: Write handler tests**

Create `apps/api-go/internal/handler/boll_pump_test.go`:

```go
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
)

func TestBollPumpSignalsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := openHandlerBollPumpStore(t)
	defer store.Close()

	_, err := service.SaveBollPumpSignal(context.Background(), store, model.BollPumpSignal{
		Market: "swap", Symbol: "XYZUSDT", Timeframe: "15m", SignalLevel: "WATCH",
		Price: 0.1234, Score: 75, PriorityScore: 75,
		SignalTimeMs: time.Now().UnixMilli(), CandleStartMs: 60000,
		Reason: "volume-backed pump", Details: model.JSONB(`{"score":75}`),
	}, false)
	if err != nil {
		t.Fatalf("save signal: %v", err)
	}

	r := gin.New()
	RegisterRoutes(r, &Deps{Cfg: &config.Config{}, Store: store})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/boll-pump/signals?market=swap&limit=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "XYZUSDT") {
		t.Fatalf("response missing symbol: %s", w.Body.String())
	}
}

func openHandlerBollPumpStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migration.Migrate(context.Background(), store); err != nil {
		store.Close()
		t.Fatalf("migrate sqlite: %v", err)
	}
	return store
}
```

- [ ] **Step 2: Run handler test and observe expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/handler -run TestBollPumpSignalsAPI -count=1
```

Expected: 404 or compile failure because routes are absent.

- [ ] **Step 3: Implement routes**

Create `apps/api-go/internal/handler/boll_pump.go`:

```go
func registerBollPumpRoutes(g *gin.RouterGroup, d *Deps) {
	r := g.Group("/boll-pump")
	r.GET("/signals", handleBollPumpSignals(d))
	r.GET("/states", handleBollPumpStates(d))
	r.GET("/stats", handleBollPumpStats(d))
	r.GET("/signals/:id/detail", handleBollPumpSignalDetail(d))
}
```

In `RegisterRoutes`, add:

```go
registerBollPumpRoutes(api, d)
```

Handlers:

```go
func handleBollPumpSignals(d *Deps) gin.HandlerFunc
func handleBollPumpStates(d *Deps) gin.HandlerFunc
func handleBollPumpStats(d *Deps) gin.HandlerFunc
func handleBollPumpSignalDetail(d *Deps) gin.HandlerFunc
```

Return JSON keys:

```text
signals: { items, limit }
states: { items, limit }
stats: { market, generatedAtMs, countsByLevel, countsByTimeframe, performance }
detail: { signal, state, candles, indicators, markers }
```

- [ ] **Step 4: Add stats and detail service helpers**

In `apps/api-go/internal/service/bollpump_store.go`, add:

```go
func GetBollPumpSignal(ctx context.Context, store *sqlite.Store, id int64) (*model.BollPumpSignal, error)
func BollPumpStats(ctx context.Context, store *sqlite.Store, market string, sinceMs int64) (map[string]interface{}, error)
func UpdateBollPumpPerformance(ctx context.Context, store *sqlite.Store, signalID int64, perf BollPumpPerformance) error
```

Add a service type for performance windows:

```go
type BollPumpPerformance struct {
	Perf1hMaxGain float64
	Perf1hMaxDrawdown float64
	Perf1hCloseReturn float64
	Perf4hMaxGain float64
	Perf4hMaxDrawdown float64
	Perf4hCloseReturn float64
	Perf24hMaxGain float64
	Perf24hMaxDrawdown float64
	Perf24hCloseReturn float64
	UpdatedMs int64
}
```

`/stats` aggregates stored performance columns and returns counts for missing windows so incomplete future-candle coverage is visible.

- [ ] **Step 5: Run handler tests**

Run:

```bash
cd apps/api-go
go test ./internal/handler -run TestBollPumpSignalsAPI -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/api-go/internal/handler/router.go apps/api-go/internal/handler/boll_pump.go apps/api-go/internal/handler/boll_pump_test.go apps/api-go/internal/service/bollpump_store.go
git commit -m "feat: 添加BOLL扫描API"
```

## Task 7: Telegram Mapping

**Files:**
- Modify: `apps/api-go/internal/telegram/format.go`
- Modify: `apps/api-go/internal/telegram/notify.go`
- Modify: `apps/api-go/internal/telegram/notify_test.go`
- Modify: `apps/api-go/internal/service/bollpump_store.go`

- [ ] **Step 1: Write Telegram format test**

Append to `apps/api-go/internal/telegram/notify_test.go`:

```go
func TestFormatBatchUsesBollPumpDetails(t *testing.T) {
	n := &AnomalyNotifier{}
	got := n.formatBatch([]model.AnomalyEvent{
		{
			Market: "swap", Symbol: "XYZUSDT", EventType: "boll_pump",
			TfSignal: "15m", EventTimeMs: time.Now().UnixMilli(),
			Title: "CONFIRM_2 XYZUSDT 15m price=0.1234 score=92",
			Details: model.JSONB(`{"signalLevel":"CONFIRM_2","score":92,"volumeRatio":2.7,"bollBandwidth":0.034,"bounceCount":2,"confluenceScore":6}`),
		},
	})

	for _, want := range []string{"BOLL泵盘", "XYZUSDT", "周期: 15m", "强度: 92", "量能: 2.70x", "带宽: 0.0340", "反弹: 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format output missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run Telegram test and observe expected failure**

Run:

```bash
cd apps/api-go
go test ./internal/telegram -run TestFormatBatchUsesBollPumpDetails -count=1
```

Expected: FAIL because label/detail formatting is absent.

- [ ] **Step 3: Add label and severity**

In `apps/api-go/internal/telegram/format.go`:

```go
labels["boll_pump"] = "BOLL泵盘"
```

In `eventSeverityScore`, use:

```go
case "boll_pump":
	if score, ok := detailFloat(details, "priorityScore"); ok && score > 0 {
		base = score
	} else if score, ok := detailFloat(details, "score"); ok && score > 0 {
		base = score
	}
```

- [ ] **Step 4: Add detail line**

In `notifyDetailLine`, before the default branch handles generic events:

```go
case "boll_pump":
	if score, ok := detailFloat(details, "score"); ok {
		parts = append(parts, fmt.Sprintf("强度: %.0f", score))
	}
	if volumeRatio, ok := detailFloat(details, "volumeRatio"); ok {
		parts = append(parts, "量能: "+fmtFactor(volumeRatio))
	}
	if bw, ok := detailFloat(details, "bollBandwidth"); ok {
		parts = append(parts, fmt.Sprintf("带宽: %.4f", bw))
	}
	if bounceCount, ok := detailFloat(details, "bounceCount"); ok {
		parts = append(parts, fmt.Sprintf("反弹: %.0f", bounceCount))
	}
	if confluence, ok := detailFloat(details, "confluenceScore"); ok && confluence > 0 {
		parts = append(parts, fmt.Sprintf("共振: +%.0f", confluence))
	}
```

- [ ] **Step 5: Ensure eligible scanner signals create `anomaly_events`**

In `SaveBollPumpSignal`, when `insertAnomaly` is true, call `insertAnomalyEvents` with:

```go
map[string]interface{}{
	"market": sig.Market,
	"symbol": sig.Symbol,
	"event_type": "boll_pump",
	"tf_signal": sig.Timeframe,
	"tf_level": sig.SignalLevel,
	"event_time_ms": sig.SignalTimeMs,
	"title": fmt.Sprintf("%s %s %s price=%.6g score=%.0f", sig.SignalLevel, sig.Symbol, sig.Timeframe, sig.Price, sig.PriorityScore),
	"details": map[string]interface{}{
		"signalLevel": sig.SignalLevel,
		"score": sig.Score,
		"confluenceScore": sig.ConfluenceScore,
		"priorityScore": sig.PriorityScore,
		"volumeRatio": sig.VolumeRatio,
		"bollBandwidth": sig.BollBandwidth,
		"bounceCount": sig.BounceCount,
		"quoteVolume24h": sig.QuoteVolume24h,
		"reason": sig.Reason,
	},
}
```

- [ ] **Step 6: Run Telegram tests**

Run:

```bash
cd apps/api-go
go test ./internal/telegram ./internal/service -run 'TestFormatBatchUsesBollPumpDetails|TestSaveAndListBollPumpSignal' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/api-go/internal/telegram/format.go apps/api-go/internal/telegram/notify.go apps/api-go/internal/telegram/notify_test.go apps/api-go/internal/service/bollpump_store.go
git commit -m "feat: 添加BOLL扫描通知格式"
```

## Task 8: Frontend Workspace

**Files:**
- Create: `apps/web/src/services/bollPump.ts`
- Create: `apps/web/src/components/BollPumpChart.tsx`
- Create: `apps/web/src/pages/BollPumpPage.tsx`
- Modify: `apps/web/src/main.tsx`
- Modify: `apps/web/src/App.tsx`
- Modify: `apps/web/src/styles/app.css`

- [ ] **Step 1: Add API service**

Create `apps/web/src/services/bollPump.ts`:

```ts
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

export type BollPumpSignal = {
  id: number;
  market: string;
  symbol: string;
  timeframe: string;
  signal_level: "WATCH" | "CONFIRM_1" | "CONFIRM_2";
  price: number;
  volume_ratio: number;
  boll_bandwidth: number;
  bounce_count: number;
  score: number;
  confluence_score: number;
  priority_score: number;
  signal_time_ms: number;
  reason: string;
};

export type BollPumpState = {
  id: number;
  market: string;
  symbol: string;
  timeframe: string;
  status: string;
  current_score: number;
  confluence_score: number;
  priority_score: number;
  bounce_count: number;
  updated_at?: string;
};

export type BollPumpDetail = {
  signal: BollPumpSignal;
  state?: BollPumpState;
  candles: Array<{ time: number; open: number; high: number; low: number; close: number; volume: number }>;
  indicators: Array<{ time: number; middle: number; upper: number; lower: number; atr14: number; bandwidth: number }>;
  markers: Array<{ time: number; label: string; price: number; kind: string }>;
};

async function req<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

export function fetchBollPumpSignals(params = "market=swap&limit=100") {
  return req<{ items: BollPumpSignal[]; limit: number }>(`/api/boll-pump/signals?${params}`);
}

export function fetchBollPumpStates(params = "market=swap&limit=100") {
  return req<{ items: BollPumpState[]; limit: number }>(`/api/boll-pump/states?${params}`);
}

export function fetchBollPumpStats(params = "market=swap") {
  return req<any>(`/api/boll-pump/stats?${params}`);
}

export function fetchBollPumpDetail(id: number) {
  return req<BollPumpDetail>(`/api/boll-pump/signals/${id}/detail`);
}
```

- [ ] **Step 2: Add chart component**

Create `apps/web/src/components/BollPumpChart.tsx` that wraps existing `EChart`:

```tsx
import { useMemo } from "react";
import EChart from "./EChart";
import type { BollPumpDetail } from "../services/bollPump";

export default function BollPumpChart({ detail }: { detail: BollPumpDetail | null }) {
  const option = useMemo(() => {
    const candles = detail?.candles || [];
    const indicators = detail?.indicators || [];
    const x = candles.map((c) => new Date(c.time).toLocaleString());
    const markData = (detail?.markers || []).map((m) => {
      const idx = candles.findIndex((c) => c.time === m.time);
      return idx >= 0 ? { coord: [idx, m.price], value: m.label, itemStyle: { color: m.kind === "confirm" ? "#ef4444" : "#3b82f6" } } : null;
    }).filter(Boolean);
    return {
      tooltip: { trigger: "axis" },
      legend: { data: ["K", "upper", "middle", "lower"] },
      grid: [{ left: 48, right: 24, top: 36, height: 260 }],
      xAxis: [{ type: "category", data: x }],
      yAxis: [{ scale: true }],
      dataZoom: [{ type: "inside" }, { type: "slider", height: 18 }],
      series: [
        { name: "K", type: "candlestick", data: candles.map((c) => [c.open, c.close, c.low, c.high]), markPoint: { data: markData } },
        { name: "upper", type: "line", showSymbol: false, data: indicators.map((i) => i.upper) },
        { name: "middle", type: "line", showSymbol: false, data: indicators.map((i) => i.middle) },
        { name: "lower", type: "line", showSymbol: false, data: indicators.map((i) => i.lower) },
      ],
    };
  }, [detail]);

  return <EChart option={option as any} height={360} />;
}
```

- [ ] **Step 3: Add page**

Create `apps/web/src/pages/BollPumpPage.tsx` with:

```tsx
import { useEffect, useMemo, useState } from "react";
import { Button, Drawer, Space, Table, Tag, Typography } from "@arco-design/web-react";
import BollPumpChart from "../components/BollPumpChart";
import { fetchBollPumpDetail, fetchBollPumpSignals, fetchBollPumpStates, fetchBollPumpStats, type BollPumpDetail, type BollPumpSignal, type BollPumpState } from "../services/bollPump";

function levelColor(level: string) {
  if (level === "CONFIRM_2") return "red";
  if (level === "CONFIRM_1") return "orange";
  return "arcoblue";
}

export default function BollPumpPage() {
  const { Title, Text } = Typography;
  const [signals, setSignals] = useState<BollPumpSignal[]>([]);
  const [states, setStates] = useState<BollPumpState[]>([]);
  const [stats, setStats] = useState<any>(null);
  const [detail, setDetail] = useState<BollPumpDetail | null>(null);
  const [open, setOpen] = useState(false);

  const refresh = async () => {
    const [sig, st, stat] = await Promise.all([
      fetchBollPumpSignals(),
      fetchBollPumpStates("market=swap&limit=100&min_priority_score=60"),
      fetchBollPumpStats(),
    ]);
    setSignals(sig.items || []);
    setStates(st.items || []);
    setStats(stat);
  };

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 15000);
    return () => clearInterval(timer);
  }, []);

  const signalColumns = useMemo(() => [
    { title: "时间", render: (_: any, r: BollPumpSignal) => new Date(r.signal_time_ms).toLocaleString() },
    { title: "Symbol", dataIndex: "symbol" },
    { title: "周期", dataIndex: "timeframe" },
    { title: "等级", render: (_: any, r: BollPumpSignal) => <Tag color={levelColor(r.signal_level)}>{r.signal_level}</Tag> },
    { title: "价格", render: (_: any, r: BollPumpSignal) => Number(r.price).toPrecision(6) },
    { title: "量能", render: (_: any, r: BollPumpSignal) => `${Number(r.volume_ratio || 0).toFixed(2)}x` },
    { title: "分数", render: (_: any, r: BollPumpSignal) => Number(r.priority_score || r.score || 0).toFixed(0) },
    { title: "操作", render: (_: any, r: BollPumpSignal) => <Button size="small" onClick={async () => { setDetail(await fetchBollPumpDetail(r.id)); setOpen(true); }}>详情</Button> },
  ], []);

  const stateColumns = useMemo(() => [
    { title: "Symbol", dataIndex: "symbol" },
    { title: "周期", dataIndex: "timeframe" },
    { title: "状态", dataIndex: "status" },
    { title: "反弹", dataIndex: "bounce_count" },
    { title: "优先级", render: (_: any, r: BollPumpState) => Number(r.priority_score || 0).toFixed(0) },
  ], []);

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>BOLL 泵盘扫描器</Title>
          <Button onClick={refresh}>刷新</Button>
        </div>
        <Space wrap>
          <span className="cm-pill">信号 {signals.length}</span>
          <span className="cm-pill">活跃 {states.length}</span>
          <span className="cm-pill">更新时间 {stats?.generatedAtMs ? new Date(stats.generatedAtMs).toLocaleTimeString() : "-"}</span>
        </Space>
      </div>
      <div className="cm-grid-2">
        <div className="cm-card cm-bollPanel"><Table rowKey="id" size="small" columns={signalColumns as any} data={signals as any} pagination={{ pageSize: 20 }} /></div>
        <div className="cm-card cm-bollPanel"><Table rowKey="id" size="small" columns={stateColumns as any} data={states as any} pagination={{ pageSize: 20 }} /></div>
      </div>
      <Drawer width="80%" visible={open} onCancel={() => setOpen(false)} footer={null} title={detail?.signal ? `${detail.signal.symbol} ${detail.signal.timeframe}` : "BOLL detail"}>
        <Text className="cm-muted">{detail?.signal?.reason || ""}</Text>
        <BollPumpChart detail={detail} />
      </Drawer>
    </div>
  );
}
```

- [ ] **Step 4: Register route and nav**

In `apps/web/src/main.tsx` import `BollPumpPage` and add:

```tsx
<Route path="boll-pump" element={<BollPumpPage />} />
```

In `apps/web/src/App.tsx`, add a nav link:

```tsx
<NavLink to="/boll-pump" className={({ isActive }) => `cm-navLink ${isActive ? "cm-navLink--active" : ""}`}>
  BOLL扫描
</NavLink>
```

- [ ] **Step 5: Add workspace CSS**

Append to `apps/web/src/styles/app.css`:

```css
.cm-bollPanel {
  padding: 12px;
  min-width: 0;
}

.cm-bollPanel .arco-table {
  font-size: 12px;
}

@media (max-width: 900px) {
  .cm-bollPanel {
    overflow-x: auto;
  }
}
```

- [ ] **Step 6: Build frontend**

Run:

```bash
cd apps/web
npm run build
```

Expected: Vite build succeeds.

- [ ] **Step 7: Commit**

```bash
git add apps/web/src/services/bollPump.ts apps/web/src/components/BollPumpChart.tsx apps/web/src/pages/BollPumpPage.tsx apps/web/src/main.tsx apps/web/src/App.tsx apps/web/src/styles/app.css
git commit -m "feat: 添加BOLL扫描工作台"
```

## Task 9: End-to-End Validation and Cleanup

**Files:**
- Modify as needed only in files touched by earlier tasks.

- [ ] **Step 1: Run backend tests**

```bash
cd apps/api-go
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run frontend build**

```bash
cd apps/web
npm run build
```

Expected: PASS.

- [ ] **Step 3: Run a local API smoke check**

Start API with a local SQLite DSN and existing Redis/ClickHouse settings used by this workspace:

```bash
cd apps/api-go
go run ./cmd/api
```

In another terminal:

```bash
curl -s 'http://localhost:8000/api/boll-pump/signals?market=swap&limit=5'
curl -s 'http://localhost:8000/api/boll-pump/states?market=swap&limit=5'
curl -s 'http://localhost:8000/api/boll-pump/stats?market=swap'
```

Expected: each response is valid JSON with no server error.

- [ ] **Step 4: Run frontend smoke check**

```bash
cd apps/web
npm run dev -- --host 0.0.0.0
```

Open `/boll-pump`, confirm:

- recent signals table renders
- active states table renders
- stats pills render
- detail drawer opens from a signal row
- chart renders with candlesticks and BOLL lines when detail data exists

- [ ] **Step 5: Verify retention cleanup**

Add one old `boll_pump_signals` row in a local database, run `rt.doCleanup` through an existing cleanup test or a focused service test, and verify rows older than `BOLL_PUMP_RETENTION_DAYS` are deleted.

- [ ] **Step 6: Final status**

Run:

```bash
git status --short
```

Expected: clean working tree after the final commit, or only intentionally untracked local runtime files.

- [ ] **Step 7: Final commit if Step 5 changed code**

```bash
git add apps/api-go/internal/hub/runtime.go apps/api-go/internal/hub/runtime_cleanup_test.go apps/api-go/internal/service/bollpump_store.go apps/api-go/internal/service/bollpump_store_test.go
git commit -m "test: 补充BOLL扫描清理验证"
```
