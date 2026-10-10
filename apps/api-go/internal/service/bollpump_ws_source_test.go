package service

import (
	"context"
	"strconv"
	"testing"
	"time"

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
	timeframe := "3m"
	bars := bollPumpLiveKlineTestBars(timeframe, 90)
	base := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{timeframe: {{OpenTimeMs: 1000, Close: 1, Closed: true}}},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	source := NewBollPumpLiveKlineSource(base, BollPumpLiveKlineSourceConfig{
		Market:         "swap",
		SymbolLimit:    10,
		Intervals:      []string{timeframe},
		BootstrapLimit: 120,
	})
	source.cache.Seed("swap", "XYZUSDT", timeframe, bars, 120)

	got, err := source.Klines(context.Background(), "swap", "XYZUSDT", timeframe, 80)
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

func TestBollPumpLiveKlineSourceRefreshesStaleCacheForKlines(t *testing.T) {
	timeframe := "1m"
	freshBars := bollPumpLiveKlineTestBars(timeframe, 90)
	staleBars := bollPumpLiveKlineTestBarsEndingAt(timeframe, 90, time.Now().Add(-24*time.Hour).UnixMilli())
	base := &fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{timeframe: freshBars},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}
	source := NewBollPumpLiveKlineSource(base, BollPumpLiveKlineSourceConfig{
		Market:         "swap",
		SymbolLimit:    10,
		Intervals:      []string{timeframe},
		BootstrapLimit: 120,
	})
	source.cache.Seed("swap", "XYZUSDT", timeframe, staleBars, 120)

	got, err := source.Klines(context.Background(), "swap", "XYZUSDT", timeframe, 80)
	if err != nil {
		t.Fatalf("klines: %v", err)
	}
	if len(base.requestedTFs) != 1 || base.requestedTFs[0] != timeframe {
		t.Fatalf("base requested timeframes = %v, want [%s]", base.requestedTFs, timeframe)
	}
	wantLatest := freshBars[len(freshBars)-1].OpenTimeMs
	if got[len(got)-1].OpenTimeMs != wantLatest {
		t.Fatalf("latest open = %d, want refreshed %d", got[len(got)-1].OpenTimeMs, wantLatest)
	}
	cached := source.cache.Klines("swap", "XYZUSDT", timeframe, 1)
	if len(cached) != 1 || cached[0].OpenTimeMs != wantLatest {
		t.Fatalf("cached latest = %#v, want refreshed latest %d", cached, wantLatest)
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
				"Q":"700",
				"x":true
			}
		}
	}`))

	got := source.cache.Klines("swap", "XYZUSDT", "1m", 10)
	if len(got) != 1 {
		t.Fatalf("bars = %d, want 1", len(got))
	}
	if got[0].OpenTimeMs != 1638747600000 || got[0].Close != 1.2 || got[0].QuoteVolume != 1200 || got[0].TakerBuyQuote != 700 {
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
				"Q":"5",
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
	if bar.High != 1.3 || bar.Low != 0.9 || bar.Volume != 30 || bar.QuoteVolume != 36 || bar.TakerBuyQuote != 15 {
		t.Fatalf("3m volume/range = %#v, want aggregated volume and range", bar)
	}
}

func bollPumpLiveKlineTestBars(timeframe string, count int) []BollPumpBar {
	return bollPumpLiveKlineTestBarsEndingAt(timeframe, count, time.Now().UnixMilli())
}

func bollPumpLiveKlineTestBarsEndingAt(timeframe string, count int, endMs int64) []BollPumpBar {
	intervalMs := bollPumpWSIntervalMs(timeframe)
	latestOpen := (endMs/intervalMs - 1) * intervalMs
	bars := make([]BollPumpBar, count)
	start := latestOpen - int64(count-1)*intervalMs
	for i := range bars {
		openTime := start + int64(i)*intervalMs
		closePrice := float64(i + 1)
		bars[i] = BollPumpBar{
			OpenTimeMs:  openTime,
			CloseTimeMs: openTime + intervalMs - 1,
			Open:        closePrice - 0.1,
			High:        closePrice + 0.2,
			Low:         closePrice - 0.2,
			Close:       closePrice,
			Volume:      100,
			QuoteVolume: 1000,
			Closed:      true,
		}
	}
	return bars
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

// 补拉还是拿不到新数据（例如币安限流、或这个币没有新 K 线）时，5 分钟内不再请求币安。
func TestBollPumpLiveKlineSourceBacksOffStaleRefresh(t *testing.T) {
	timeframe := "1m"
	staleBars := bollPumpLiveKlineTestBarsEndingAt(timeframe, 90, time.Now().Add(-24*time.Hour).UnixMilli())
	base := &fakeBollPumpSource{bars: map[string][]BollPumpBar{timeframe: staleBars}} // 补拉回来的也是旧数据
	source := NewBollPumpLiveKlineSource(base, BollPumpLiveKlineSourceConfig{
		Market: "swap", SymbolLimit: 10, Intervals: []string{timeframe}, BootstrapLimit: 120,
	})
	source.cache.Seed("swap", "XYZUSDT", timeframe, staleBars, 120)

	for i := 0; i < 5; i++ {
		if _, err := source.Klines(context.Background(), "swap", "XYZUSDT", timeframe, 80); err == nil {
			t.Fatal("stale data should not be returned as fresh")
		}
	}
	if len(base.requestedTFs) != 1 {
		t.Fatalf("REST requests = %d, want 1 within backoff", len(base.requestedTFs))
	}
	// 别的币不受影响
	source.cache.Seed("swap", "ABCUSDT", timeframe, staleBars, 120)
	_, _ = source.Klines(context.Background(), "swap", "ABCUSDT", timeframe, 80)
	if len(base.requestedTFs) != 2 {
		t.Fatalf("REST requests = %d, want 2 (other symbol refreshed)", len(base.requestedTFs))
	}
}

// 币安真实的 kline 推送带 "f"/"L"（首末成交 ID，数字）和 "V"/"B"。encoding/json 的字段匹配不区分大小写，
// "L" 会落到 Low（"l"，字符串）上导致整条解析报错、收盘事件被丢掉；"V" 会覆盖 Volume（"v"）。
func TestBollPumpLiveKlineSourceParsesRealBinanceKlinePayload(t *testing.T) {
	source := NewBollPumpLiveKlineSource(&fakeBollPumpSource{}, BollPumpLiveKlineSourceConfig{
		Market: "swap", SymbolLimit: 10, Intervals: []string{"1m"}, BootstrapLimit: 120,
	})
	msg := `{"stream":"btcusdt@kline_1m","data":{"e":"kline","E":1790935260012,"s":"BTCUSDT","k":{"t":1790935200000,"T":1790935259999,"s":"BTCUSDT","i":"1m","f":7130470413,"L":7130471247,"o":"122650.10","c":"122688.00","h":"122700.00","l":"122640.00","v":"85.123","n":835,"x":true,"q":"10441234.56","V":"40.111","Q":"4920000.12","B":"0"}}}`
	if closeMs := source.handleWSMessage([]byte(msg)); closeMs != 1790935259999 {
		t.Fatalf("closeMs = %d, want closed kline processed", closeMs)
	}
	got := source.cache.Klines("swap", "BTCUSDT", "1m", 1)
	if len(got) != 1 || got[0].Low != 122640 || got[0].Volume != 85.123 || got[0].TakerBuyQuote != 4920000.12 {
		t.Fatalf("bar = %+v", got)
	}
}

func TestBollPumpLiveKlineSourceCacheLimitPerInterval(t *testing.T) {
	source := NewBollPumpLiveKlineSource(&fakeBollPumpSource{}, BollPumpLiveKlineSourceConfig{
		Market: "swap", SymbolLimit: 10, Intervals: []string{"1m", "3m", "5m", "15m", "4h"}, BootstrapLimit: 499,
	})
	for tf, want := range map[string]int{"1m": 260, "3m": 260, "5m": 499, "15m": 499, "4h": 499} {
		if got := source.cacheLimit(tf); got != want {
			t.Fatalf("%s limit = %d, want %d", tf, got, want)
		}
	}
	// 1m 缓存要够聚合一根 4h（240 根 + 余量）
	if bollPumpShortIntervalLimit < 4*60+4 {
		t.Fatal("1m cache too short to aggregate 4h")
	}
}
