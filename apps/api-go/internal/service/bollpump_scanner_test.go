package service

import (
	"context"
	"testing"
)

type fakeBollPumpSource struct {
	bars  map[string][]BollPumpBar
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
