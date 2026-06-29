package service

import (
	"context"
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
