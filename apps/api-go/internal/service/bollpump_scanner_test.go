package service

import (
	"context"
	"strings"
	"testing"

	"coinmark/api-go/internal/config"
)

type fakeBollPumpSource struct {
	bars        map[string][]BollPumpBar
	quote       map[string]float64
	symbolLimit int
}

func (f *fakeBollPumpSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	f.symbolLimit = limit
	return []string{"XYZUSDT"}, nil
}

func (f *fakeBollPumpSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	return f.bars[timeframe], nil
}

func (f *fakeBollPumpSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	return f.quote[symbol], nil
}

func TestBollPumpScannerScansOneTimeframe(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	source := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenPump("15m")},
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
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenPump("15m")},
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
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenPump("15m")},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	scanner := NewBollPumpScanner(source, store, DefaultBollPumpConfig())

	scanner.ScanTimeframe(ctx, "15m")
	if source.symbolLimit != 42 {
		t.Fatalf("symbol limit = %d, want saved 42", source.symbolLimit)
	}
}

func TestBollPumpScannerAddsFourHourResistanceBreakoutScore(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	cfg.Resistance4HBreakoutBonus = 15
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

func bollPumpFixtureUntilFirstWatch(tf string, cfg BollPumpConfig) []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenPump(tf)
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
