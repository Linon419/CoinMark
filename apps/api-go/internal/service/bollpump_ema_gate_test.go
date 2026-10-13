package service

import (
	"context"
	"testing"

	"coinmark/api-go/internal/model"
)

// bollPumpWithHistory 在 bars 前面补 n 根从 start 线性涨到 bars[0].Open 的 K 线（时间整体后移），让 EMA200 有足够历史。
func bollPumpWithHistory(bars []BollPumpBar, n int, start float64) []BollPumpBar {
	if len(bars) < 2 {
		return bars
	}
	step := bars[1].OpenTimeMs - bars[0].OpenTimeMs
	out := make([]BollPumpBar, 0, n+len(bars))
	for i := 0; i < n; i++ {
		p := start + (bars[0].Open-start)*float64(i)/float64(n)
		out = append(out, BollPumpBar{OpenTimeMs: int64(i) * step, CloseTimeMs: int64(i+1)*step - 1,
			Open: p, High: p, Low: p, Close: p, Volume: 100, QuoteVolume: 100 * p, Closed: true})
	}
	for _, b := range bars {
		b.OpenTimeMs += int64(n) * step
		b.CloseTimeMs += int64(n) * step
		out = append(out, b)
	}
	return out
}

func bollPumpLineBars(n int, from, to float64, stepMs int64) []BollPumpBar {
	out := make([]BollPumpBar, n)
	for i := range out {
		p := from + (to-from)*float64(i)/float64(n-1)
		out[i] = BollPumpBar{OpenTimeMs: int64(i) * stepMs, CloseTimeMs: int64(i+1)*stepMs - 1, Open: p, High: p, Low: p, Close: p, Volume: 100, QuoteVolume: 100 * p, Closed: true}
	}
	return out
}

func TestBollPumpAboveEMA100And200(t *testing.T) {
	if !bollPumpAboveEMA100And200(bollPumpLineBars(250, 50, 100, 60_000)) {
		t.Fatal("一路上涨，收盘价应在 EMA100/200 之上")
	}
	if bollPumpAboveEMA100And200(bollPumpLineBars(250, 100, 50, 60_000)) {
		t.Fatal("一路下跌，收盘价在 EMA 之下，不应通过")
	}
	if bollPumpAboveEMA100And200(bollPumpLineBars(150, 50, 100, 60_000)) {
		t.Fatal("不足 200 根，EMA200 不可靠，不应通过")
	}
	// 跌破 EMA100 但仍在 EMA200 之上：要求两条都在上方，不通过
	bars := bollPumpLineBars(250, 50, 100, 60_000)
	e100, e200 := bollSqueezeEMA(bars, 100), bollSqueezeEMA(bars, 200)
	last := len(bars) - 1
	bars[last].Close = (e100[last-1] + e200[last-1]) / 2
	if bollPumpAboveEMA100And200(bars) {
		t.Fatal("只在 EMA200 之上、在 EMA100 之下，不应通过")
	}
}

func TestBollPumpDominantTimeframe(t *testing.T) {
	row := model.BollPumpState{Timeframe: "5m", PriorityScore: 80}
	peers := []model.BollPumpState{
		row,
		{Timeframe: "1h", PriorityScore: 90},
		{Timeframe: "15m", PriorityScore: 75},
		{Timeframe: "30m", PriorityScore: 60}, // 分数不够
	}
	if got := BollPumpDominantTimeframe(row, peers); got != "15m" {
		t.Fatalf("dominant = %s, want 15m（比自己大的周期里最近的一个活跃周期）", got)
	}
	if got := BollPumpDominantTimeframe(row, []model.BollPumpState{row}); got != "5m" {
		t.Fatalf("没有更大的活跃周期时，主导就是自己: %s", got)
	}
}

func TestBollPumpScannerRequiresTriggerAndDominantAboveEMA(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	cfg.MinimumTrendCheckCandles = 3
	cfg.MinimumTrendGainPct = 0.001
	cfg.MinimumTrendRisingRatio = 0.1
	cfg.Resistance4HBreakoutBonus = 0
	trigger := bollPumpWithHistory(bollPumpFixtureQuietBaseThenResistanceBreakout("15m"), 150, 80)
	trigger = trigger[:len(trigger)-1] // 观察信号在倒数第二根上；有 store 时只发最新一根的信号

	run := func(hourBars []BollPumpBar, withDominant bool) int {
		store := openBollPumpTestStore(t)
		defer store.Close()
		if withDominant {
			if err := SaveBollPumpState(ctx, store, model.BollPumpState{Market: "swap", Symbol: "XYZUSDT", Timeframe: "1h",
				Status: string(BollPumpStatusWatch), CurrentScore: 85, PriorityScore: 85, WatchScore: 85}); err != nil {
				t.Fatalf("save state: %v", err)
			}
		}
		source := &fakeBollPumpSource{
			bars:  map[string][]BollPumpBar{"15m": trigger, "1h": hourBars},
			quote: map[string]float64{"XYZUSDT": 3_000_000},
		}
		return NewBollPumpScanner(source, store, cfg).ScanTimeframe(ctx, "15m").SignalsFound
	}

	if n := run(nil, false); n == 0 {
		t.Fatal("触发周期在 EMA 之上、没有更大的主导周期：应有信号")
	}
	if n := run(bollPumpLineBars(250, 50, 100, 3_600_000), true); n == 0 {
		t.Fatal("主导周期 1h 也在 EMA 之上：应有信号")
	}
	if n := run(bollPumpLineBars(250, 100, 50, 3_600_000), true); n != 0 {
		t.Fatalf("主导周期 1h 在 EMA 之下：不应有信号，得到 %d", n)
	}
	if n := NewBollPumpScanner(&fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"15m": bollPumpFixtureQuietBaseThenResistanceBreakout("15m")},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}, nil, cfg).ScanTimeframe(ctx, "15m").SignalsFound; n != 0 {
		t.Fatalf("触发周期不足 200 根、算不出 EMA200：不应有信号，得到 %d", n)
	}
}
