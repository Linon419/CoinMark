package service

import (
	"context"
	"strconv"
	"testing"

	"coinmark/api-go/internal/model"
)

func TestBollPumpKlineCacheSeedUpsertAndTrim(t *testing.T) {
	cache := newBollPumpKlineCache()
	cache.Seed("swap", "XYZUSDT", "3m", []BollPumpBar{
		{OpenTimeMs: 3000, Close: 3, Closed: true},
		{OpenTimeMs: 1000, Close: 1, Closed: true},
		{OpenTimeMs: 2000, Close: 2, Closed: true},
	}, 2)
	cache.Upsert("swap", "XYZUSDT", "3m", BollPumpBar{OpenTimeMs: 2000, Close: 22, Closed: true}, 2)
	cache.Upsert("swap", "XYZUSDT", "3m", BollPumpBar{OpenTimeMs: 4000, Close: 4, Closed: true}, 2)

	got := cache.Klines("swap", "XYZUSDT", "3m", 10)
	if len(got) != 2 {
		t.Fatalf("bars = %d, want 2", len(got))
	}
	if got[0].OpenTimeMs != 3000 || got[1].OpenTimeMs != 4000 {
		t.Fatalf("open times = %d,%d want 3000,4000", got[0].OpenTimeMs, got[1].OpenTimeMs)
	}
	if got[1].Close != 4 {
		t.Fatalf("last close = %.2f, want 4", got[1].Close)
	}
}

func TestBollPumpLiveKlineSourceUsesCacheForKlines(t *testing.T) {
	base := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"3m": {{OpenTimeMs: 1000, Close: 1, Closed: true}}},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	source := NewBollPumpLiveKlineSource(base, BollPumpLiveKlineSourceConfig{
		Market:         "swap",
		SymbolLimit:    10,
		Intervals:      []string{"3m"},
		BootstrapLimit: 120,
	})
	bars := make([]BollPumpBar, 90)
	for i := range bars {
		bars[i] = BollPumpBar{OpenTimeMs: int64(i+1) * 180000, CloseTimeMs: int64(i+2)*180000 - 1, Close: float64(i + 1), Closed: true}
	}
	source.cache.Seed("swap", "XYZUSDT", "3m", bars, 120)

	got, err := source.Klines(context.Background(), "swap", "XYZUSDT", "3m", 80)
	if err != nil {
		t.Fatalf("klines: %v", err)
	}
	if len(got) != 80 {
		t.Fatalf("bars = %d, want 80", len(got))
	}
	if len(base.requestedTFs) != 0 {
		t.Fatalf("base requested timeframes = %v, want none", base.requestedTFs)
	}
}

func TestBollPumpLiveKlineSourceHandlesClosedCombinedKline(t *testing.T) {
	source := NewBollPumpLiveKlineSource(&fakeBollPumpSource{}, BollPumpLiveKlineSourceConfig{
		Market:         "swap",
		SymbolLimit:    10,
		Intervals:      []string{"1m", "3m"},
		BootstrapLimit: 120,
	})
	source.handleWSMessage([]byte(`{
		"stream":"xyzusdt@kline_1m",
		"data":{
			"e":"kline",
			"E":1638747660000,
			"s":"XYZUSDT",
			"k":{
				"t":1638747600000,
				"T":1638747659999,
				"s":"XYZUSDT",
				"i":"1m",
				"o":"1.0000",
				"c":"1.2000",
				"h":"1.2500",
				"l":"0.9500",
				"v":"1000",
				"q":"1200",
				"x":true
			}
		}
	}`))

	got := source.cache.Klines("swap", "XYZUSDT", "1m", 10)
	if len(got) != 1 {
		t.Fatalf("bars = %d, want 1", len(got))
	}
	if got[0].OpenTimeMs != 1638747600000 || got[0].Close != 1.2 || got[0].QuoteVolume != 1200 {
		t.Fatalf("bar = %#v, want parsed closed kline", got[0])
	}
}

func TestBollPumpLiveKlineSourceAggregatesOneMinuteToThreeMinute(t *testing.T) {
	source := NewBollPumpLiveKlineSource(&fakeBollPumpSource{}, BollPumpLiveKlineSourceConfig{
		Market:         "swap",
		SymbolLimit:    10,
		Intervals:      []string{"1m", "3m"},
		BootstrapLimit: 120,
	})
	baseOpen := int64(180000)
	for i, closePrice := range []string{"1.1000", "1.0500", "1.2000"} {
		openTime := baseOpen + int64(i)*60000
		closeTime := openTime + 59999
		source.handleWSMessage([]byte(`{
			"e":"kline",
			"s":"XYZUSDT",
			"k":{
				"t":` + strconv.FormatInt(openTime, 10) + `,
				"T":` + strconv.FormatInt(closeTime, 10) + `,
				"s":"XYZUSDT",
				"i":"1m",
				"o":"1.0000",
				"c":"` + closePrice + `",
				"h":"1.3000",
				"l":"0.9000",
				"v":"10",
				"q":"12",
				"x":true
			}
		}`))
	}

	got := source.cache.Klines("swap", "XYZUSDT", "3m", 10)
	if len(got) != 1 {
		t.Fatalf("3m bars = %d, want 1", len(got))
	}
	bar := got[0]
	if bar.OpenTimeMs != baseOpen || bar.CloseTimeMs != baseOpen+179999 || bar.Open != 1 || bar.Close != 1.2 {
		t.Fatalf("3m bar = %#v, want aggregated OHLC", bar)
	}
	if bar.High != 1.3 || bar.Low != 0.9 || bar.Volume != 30 || bar.QuoteVolume != 36 {
		t.Fatalf("3m volume/range = %#v, want aggregated volume and range", bar)
	}
}

func TestBollPumpScannerKeepsStateWhenTrendCacheIsWarming(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1m"}
	cfg.Resistance4HBreakoutBonus = 0
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
	source := &warmingTrendSource{bars: bollPumpFixtureQuietBaseThenPump("1m")}
	scanner := NewBollPumpScanner(source, store, cfg)

	result := scanner.ScanTimeframe(ctx, "1m")
	if result.Errors != 0 {
		t.Fatalf("errors = %d, want 0 for warming cache", result.Errors)
	}
	st, err := GetBollPumpState(ctx, store, "swap", "XYZUSDT", "1m")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st == nil || st.Status != string(BollPumpStatusWatch) {
		t.Fatalf("state = %#v, want WATCH preserved", st)
	}
}

type warmingTrendSource struct {
	bars []BollPumpBar
}

func (s *warmingTrendSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	return []string{"XYZUSDT"}, nil
}

func (s *warmingTrendSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	if timeframe == "15m" {
		return nil, &bollPumpKlineCacheWarmingError{Symbol: symbol, Timeframe: timeframe, Have: 0, Want: 60}
	}
	return s.bars, nil
}

func (s *warmingTrendSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	return 3_000_000, nil
}
