package service

import (
	"context"
	"strings"
	"testing"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/model"
)

type fakeBollPumpSource struct {
	bars             map[string][]BollPumpBar
	quote            map[string]float64
	symbols          []string
	symbolLimit      int
	requestedSymbols []string
	requestedTFs     []string
}

func (f *fakeBollPumpSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	f.symbolLimit = limit
	if len(f.symbols) > 0 {
		return f.symbols, nil
	}
	return []string{"XYZUSDT"}, nil
}

func (f *fakeBollPumpSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	f.requestedSymbols = append(f.requestedSymbols, symbol)
	f.requestedTFs = append(f.requestedTFs, timeframe)
	return f.bars[timeframe], nil
}

func (f *fakeBollPumpSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	return f.quote[symbol], nil
}

func TestBollPumpScannerScansOneTimeframe(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	cfg.MinimumTrendCheckCandles = 3
	cfg.MinimumTrendGainPct = 0.001
	cfg.MinimumTrendRisingRatio = 0.1
	source := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenResistanceBreakout("15m")},
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

func TestBollPumpScannerUsesRuntimeSymbolLimit(t *testing.T) {
	cfg := BollPumpConfigFromRuntime(&config.Config{
		BollPumpEnabled:        true,
		BollPumpMarket:         "swap",
		BollPumpTimeframes:     "15m",
		BollPumpSymbolLimit:    42,
		BollPumpScanTimeoutSec: 7,
	})
	source := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenResistanceBreakout("15m")},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, nil, cfg)

	scanner.ScanTimeframe(context.Background(), "15m")
	if source.symbolLimit != 42 {
		t.Fatalf("symbol limit = %d, want 42", source.symbolLimit)
	}
	if scanner.cfg.ScanTimeoutSec != 7 {
		t.Fatalf("scan timeout = %d, want 7", scanner.cfg.ScanTimeoutSec)
	}
}

func TestBollPumpScannerRefreshesSavedSettings(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.SymbolLimit = 42
	if _, err := SaveBollPumpConfig(ctx, store, cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	source := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenResistanceBreakout("15m")},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, DefaultBollPumpConfig())

	scanner.ScanTimeframe(ctx, "15m")
	if source.symbolLimit != 42 {
		t.Fatalf("symbol limit = %d, want saved 42", source.symbolLimit)
	}
}

func TestBollPumpScannerOnlyScansUSDTSymbols(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	source := &fakeBollPumpSource{
		symbols: []string{"XYZUSDT", "ABCUSDC", "USDCUSDT"},
		bars:    map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenResistanceBreakout("15m")},
		quote:   map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, nil, cfg)

	result := scanner.ScanTimeframe(context.Background(), "15m")
	if result.SymbolsScanned != 1 {
		t.Fatalf("symbols scanned = %d, want 1", result.SymbolsScanned)
	}
	for _, symbol := range source.requestedSymbols {
		if symbol != "XYZUSDT" {
			t.Fatalf("requested symbols = %v, want only XYZUSDT", source.requestedSymbols)
		}
	}
}

func TestBollPumpScannerAddsFourHourResistanceBreakoutScore(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	cfg.Resistance4HBreakoutBonus = 15
	cfg.MinimumTrendCheckCandles = 3
	cfg.MinimumTrendGainPct = 0.001
	cfg.MinimumTrendRisingRatio = 0.1
	signalBars := bollPumpFixtureUntilFirstWatch("15m", cfg)
	source := &fakeBollPumpSource{
		bars: map[string][]BollPumpBar{
			"15m": signalBars,
			"4h":  bollPumpFixtureFourHourResistanceBreakout(),
		},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	result := scanner.ScanTimeframe(ctx, "15m")
	if result.SignalsFound == 0 {
		t.Fatalf("signals found = 0, want > 0")
	}
	rows, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Limit: 10})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("signals = %d, want 1", len(rows))
	}
	base := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", signalBars, ComputeBollPumpIndicators(signalBars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod), 3_000_000, cfg)
	if rows[0].Score != base.Signal.Score+15 {
		t.Fatalf("score = %.2f, want %.2f", rows[0].Score, base.Signal.Score+15)
	}
	if !strings.Contains(rows[0].Reason, "4h resistance breakout") {
		t.Fatalf("reason = %q, want 4h resistance breakout", rows[0].Reason)
	}
}

func TestBollPumpScannerPersistsFourHourKeyKSignal(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.SymbolLimit = 20
	cfg.KeyK4HEnabled = true
	source := &fakeBollPumpSource{
		symbols: []string{"XYZUSDT", "ABCUSDC"},
		bars: map[string][]BollPumpBar{
			"4h": bollPumpFixtureFourHourKeyK(),
		},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	result := scanner.ScanKeyK4H(ctx)
	if result.SymbolsScanned != 1 {
		t.Fatalf("symbols scanned = %d, want 1", result.SymbolsScanned)
	}
	if result.SignalsFound != 1 {
		t.Fatalf("signals found = %d, want 1", result.SignalsFound)
	}
	rows, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", SignalLevel: string(BollPumpLevelKeyK4H), Limit: 10})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("signals = %d, want 1", len(rows))
	}
	if rows[0].Timeframe != "4h" || rows[0].SignalLevel != string(BollPumpLevelKeyK4H) {
		t.Fatalf("signal = %#v, want 4h KEY_K_4H", rows[0])
	}

	result = scanner.ScanKeyK4H(ctx)
	if result.SignalsFound != 0 {
		t.Fatalf("second scan signals found = %d, want 0 after dedupe", result.SignalsFound)
	}
	rows, err = ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", SignalLevel: string(BollPumpLevelKeyK4H), Limit: 10})
	if err != nil {
		t.Fatalf("list signals after dedupe: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("signals after dedupe = %d, want 1", len(rows))
	}
}

func TestBollPumpScannerRequiresFifteenMinuteClearUptrend(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1m"}
	cfg.Resistance4HBreakoutBonus = 0
	source := &fakeBollPumpSource{
		bars: map[string][]BollPumpBar{
			"1m":  bollPumpFixtureQuietBaseThenPump("1m"),
			"15m": bollPumpFixtureChoppyTrend(100, cfg.MinimumTrendCheckCandles),
		},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "1m",
		Status:        string(BollPumpStatusWatch),
		CurrentScore:  88,
		PriorityScore: 88,
		WatchScore:    88,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	result := scanner.ScanTimeframe(ctx, "1m")
	if result.SignalsFound != 0 {
		t.Fatalf("signals found = %d, want 0 when 15m trend is unclear", result.SignalsFound)
	}
	states, err := ListBollPumpStates(ctx, store, BollPumpStateFilter{Market: "swap", MinPriorityScore: 60, Limit: 10})
	if err != nil {
		t.Fatalf("list states: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("active states = %d, want 0 after 15m trend invalidation", len(states))
	}
	if !strings.Contains(strings.Join(source.requestedTFs, ","), "15m") {
		t.Fatalf("requested timeframes = %v, want 15m trend check", source.requestedTFs)
	}
}

func TestBollPumpScannerKeepsScanningActiveStateSymbolsOutsideRuntimeLimit(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1h"}
	cfg.Resistance4HBreakoutBonus = 0
	bars := bollPumpFixtureLatestCloseBelowLowerBand("1h")
	prevOpen := bars[len(bars)-2].OpenTimeMs
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:              "swap",
		Symbol:              "JTOUSDT",
		Timeframe:           "1h",
		Status:              string(BollPumpStatusConfirm1),
		WatchStartedMs:      ptrInt64(60_000),
		WatchCandleStartMs:  ptrInt64(0),
		WatchScore:          95,
		CurrentScore:        105,
		PriorityScore:       105,
		BounceCount:         1,
		FirstPullbackLow:    ptrFloat64(99),
		LastCheckedCandleMs: ptrInt64(prevOpen),
		LastSignalLevel:     ptrString(string(BollPumpLevelConfirm1)),
		ExpiresAtCandleMs:   ptrInt64(bars[len(bars)-1].OpenTimeMs + 10*60*60*1000),
		Details:             model.JSONB(`{}`),
	}); err != nil {
		t.Fatalf("save active state: %v", err)
	}
	source := &fakeBollPumpSource{
		symbols: []string{"AAAUSDT"},
		bars:    map[string][]BollPumpBar{"1h": bars},
		quote:   map[string]float64{"AAAUSDT": 3_000_000, "JTOUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	scanner.ScanTimeframe(ctx, "1h")

	if !strings.Contains(strings.Join(source.requestedSymbols, ","), "JTOUSDT") {
		t.Fatalf("requested symbols = %v, want active JTOUSDT included", source.requestedSymbols)
	}
	st, err := GetBollPumpState(ctx, store, "swap", "JTOUSDT", "1h")
	if err != nil {
		t.Fatalf("get active state: %v", err)
	}
	if st == nil || st.Status != string(BollPumpStatusInvalidated) {
		t.Fatalf("state = %#v, want INVALIDATED after latest close below lower band", st)
	}
}

func TestBollPumpScannerContinuesActiveStateWithoutReplayReset(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	cfg.Resistance4HBreakoutBonus = 0
	bars := bollPumpFixtureClearTrend(100, 45)
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:              "swap",
		Symbol:              "XYZUSDT",
		Timeframe:           "15m",
		Status:              string(BollPumpStatusWatch),
		WatchStartedMs:      ptrInt64(bars[30].CloseTimeMs),
		WatchCandleStartMs:  ptrInt64(bars[30].OpenTimeMs),
		WatchScore:          105,
		CurrentScore:        105,
		PriorityScore:       105,
		LastCheckedCandleMs: ptrInt64(bars[len(bars)-2].OpenTimeMs),
		LastSignalLevel:     ptrString(string(BollPumpLevelWatch)),
		ExpiresAtCandleMs:   ptrInt64(bars[len(bars)-1].OpenTimeMs + 20*15*60*1000),
		Details:             model.JSONB(`{}`),
	}); err != nil {
		t.Fatalf("save active state: %v", err)
	}
	source := &fakeBollPumpSource{
		symbols: []string{"XYZUSDT"},
		bars:    map[string][]BollPumpBar{"15m": bars},
		quote:   map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	scanner.ScanTimeframe(ctx, "15m")

	st, err := GetBollPumpState(ctx, store, "swap", "XYZUSDT", "15m")
	if err != nil {
		t.Fatalf("get active state: %v", err)
	}
	if st == nil || st.Status != string(BollPumpStatusWatch) {
		t.Fatalf("state = %#v, want WATCH to continue after latest candle", st)
	}
	if st.PriorityScore != 105 {
		t.Fatalf("priority score = %.0f, want preserved active score", st.PriorityScore)
	}
}

func TestBollPumpScannerInvalidatesActiveStateWhenTrendFailsCurrentRules(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1h"}
	cfg.Resistance4HBreakoutBonus = 0
	bars := bollPumpFixtureFlatNoSignal("1h")
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:              "swap",
		Symbol:              "JTOUSDT",
		Timeframe:           "1h",
		Status:              string(BollPumpStatusConfirm1),
		WatchStartedMs:      ptrInt64(60_000),
		WatchCandleStartMs:  ptrInt64(0),
		WatchScore:          95,
		CurrentScore:        105,
		PriorityScore:       105,
		BounceCount:         1,
		FirstPullbackLow:    ptrFloat64(99),
		LastCheckedCandleMs: ptrInt64(bars[len(bars)-1].OpenTimeMs),
		LastSignalLevel:     ptrString(string(BollPumpLevelConfirm1)),
		ExpiresAtCandleMs:   ptrInt64(bars[len(bars)-1].OpenTimeMs + 10*60*60*1000),
		Details:             model.JSONB(`{}`),
	}); err != nil {
		t.Fatalf("save active state: %v", err)
	}
	source := &fakeBollPumpSource{
		symbols: []string{"AAAUSDT"},
		bars:    map[string][]BollPumpBar{"1h": bars},
		quote:   map[string]float64{"AAAUSDT": 3_000_000, "JTOUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, cfg)

	scanner.ScanTimeframe(ctx, "1h")

	st, err := GetBollPumpState(ctx, store, "swap", "JTOUSDT", "1h")
	if err != nil {
		t.Fatalf("get active state: %v", err)
	}
	if st == nil || st.Status != string(BollPumpStatusInvalidated) {
		t.Fatalf("state = %#v, want INVALIDATED when active state fails current trend gate", st)
	}
}

func bollPumpFixtureUntilFirstWatch(tf string, cfg BollPumpConfig) []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout(tf)
	for i := range bars {
		if i < cfg.BollPeriod {
			continue
		}
		window := bars[:i+1]
		ind := ComputeBollPumpIndicators(window, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)
		got := EvaluateBollPumpWatch("swap", "XYZUSDT", tf, window, ind, 3_000_000, cfg)
		if got.Triggered {
			return window
		}
	}
	return bars
}

func bollPumpFixtureFlatNoSignal(tf string) []BollPumpBar {
	step := int64(60 * 60 * 1000)
	if tf == "15m" {
		step = int64(15 * 60 * 1000)
	}
	bars := make([]BollPumpBar, 0, 40)
	for i := 0; i < 40; i++ {
		openTime := int64(i) * step
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  openTime,
			CloseTimeMs: openTime + step - 1,
			Open:        100,
			High:        100.2,
			Low:         99.8,
			Close:       100,
			Volume:      100,
			QuoteVolume: 10_000,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureLatestCloseBelowLowerBand(tf string) []BollPumpBar {
	step := int64(60 * 60 * 1000)
	if tf == "15m" {
		step = int64(15 * 60 * 1000)
	}
	bars := make([]BollPumpBar, 0, 30)
	for i := 0; i < 29; i++ {
		openTime := int64(i) * step
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  openTime,
			CloseTimeMs: openTime + step - 1,
			Open:        100,
			High:        100.2,
			Low:         99.8,
			Close:       100,
			Volume:      100,
			QuoteVolume: 10_000,
			Closed:      true,
		})
	}
	openTime := int64(len(bars)) * step
	bars = append(bars, BollPumpBar{
		OpenTimeMs:  openTime,
		CloseTimeMs: openTime + step - 1,
		Open:        100,
		High:        100.1,
		Low:         89,
		Close:       90,
		Volume:      120,
		QuoteVolume: 10_800,
		Closed:      true,
	})
	return bars
}

func ptrFloat64(v float64) *float64 {
	return &v
}

func ptrInt64(v int64) *int64 {
	return &v
}
