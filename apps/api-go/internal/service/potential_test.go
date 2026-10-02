package service

import (
	"context"
	"math"
	"testing"
)

const potentialTestHour = int64(3600 * 1000)

func potentialTestBars(n int, stepMs int64, startMs int64, closeAt func(i int) float64) []BollPumpBar {
	bars := make([]BollPumpBar, n)
	for i := range bars {
		c := closeAt(i)
		bars[i] = BollPumpBar{OpenTimeMs: startMs + int64(i)*stepMs, Open: c, High: c, Low: c, Close: c, Closed: true}
	}
	return bars
}

func TestPotentialHourlyMetricsAccumulatesThreeDaysOfNetInflow(t *testing.T) {
	h1 := potentialTestBars(100, potentialTestHour, 0, func(i int) float64 { return 1 })
	for i := range h1 {
		h1[i].QuoteVolume = 1000
		h1[i].TakerBuyQuote = 500 // 买卖相等，净流入 0
	}
	for i := 100 - 72; i < 100; i++ {
		h1[i].TakerBuyQuote = 600 // 最近 72 根每根净流入 2*600-1000 = 200
	}
	h1[90].TakerBuyQuote = 1000 // 这一根净流入 1000（替换上面的 200）
	h1[20].TakerBuyQuote = 1000 // 3 天之外，不算
	h1[95].Close = 1.0
	h1[99].Close = 1.2

	it, ok := potentialHourlyMetrics(h1)
	if !ok {
		t.Fatal("expected metrics")
	}
	if it.Acc3d != 71*200+1000 {
		t.Fatalf("acc3d = %v", it.Acc3d)
	}
	// 拉升前（截到 4 根前）：第 24~95 根，其中 28~95 每根 200、第 90 根 1000
	if it.Acc3dPre4h != 67*200+1000 {
		t.Fatalf("acc3d pre4h = %v", it.Acc3dPre4h)
	}
	if it.Vol24 != 24000 {
		t.Fatalf("vol24 = %v", it.Vol24)
	}
	if math.Abs(it.Ret4h-0.2) > 1e-9 {
		t.Fatalf("ret4h = %v", it.Ret4h)
	}
	if it.DecisionMs != 100*potentialTestHour {
		t.Fatalf("decision = %v, want close of last bar", it.DecisionMs)
	}
	if _, ok := potentialHourlyMetrics(h1[:71]); ok {
		t.Fatal("expected no metrics with < 72 bars")
	}
}

func TestPotentialRiseFromLow30IgnoresOlderLows(t *testing.T) {
	const fourH = 4 * potentialTestHour
	h4 := potentialTestBars(300, fourH, 0, func(i int) float64 { return 2 })
	h4[10].Low = 0.5  // 30 天以前，不算
	h4[250].Low = 1.6 // 30 天内最低
	decision := int64(300) * fourH
	rise, ok := potentialRiseFromLow30(h4, nil, decision, 2.0)
	if !ok || math.Abs(rise-0.25) > 1e-9 {
		t.Fatalf("rise = %v ok=%v, want 0.25", rise, ok)
	}
	h1 := potentialTestBars(10, potentialTestHour, decision-10*potentialTestHour, func(i int) float64 { return 2 })
	h1[9].Low = 1.0 // 最近 1h 的新低也要算上
	if rise, _ := potentialRiseFromLow30(h4, h1, decision, 2.0); math.Abs(rise-1.0) > 1e-9 {
		t.Fatalf("rise with recent hourly low = %v, want 1.0", rise)
	}
	if _, ok := potentialRiseFromLow30(h4[:100], nil, decision, 2.0); ok {
		t.Fatal("expected not ok with < 25 days of 4h bars")
	}
}

func TestPotentialEMACrossRecent(t *testing.T) {
	// 长期缓涨（EMA100 > EMA200），倒数第 4~10 根跌到 EMA 下方，倒数第 3 根收回上方
	bars := potentialTestBars(450, potentialTestHour, 0, func(i int) float64 { return 100 + 0.1*float64(i) })
	for i := 440; i < 447; i++ {
		bars[i].Close = 120
	}
	for i := 447; i < 450; i++ {
		bars[i].Close = 160
	}
	if !potentialEMACrossRecent(bars, 4) {
		t.Fatal("expected cross within last 4 bars")
	}
	if potentialEMACrossRecent(bars, 2) {
		t.Fatal("cross was 3 bars ago, not within last 2")
	}
	// 下跌趋势（EMA100 < EMA200）里的上穿不算
	down := potentialTestBars(450, potentialTestHour, 0, func(i int) float64 { return 200 - 0.1*float64(i) })
	for i := 440; i < 447; i++ {
		down[i].Close = 100
	}
	for i := 447; i < 450; i++ {
		down[i].Close = 250
	}
	if potentialEMACrossRecent(down, 4) {
		t.Fatal("expected no cross when EMA100 < EMA200")
	}
}

func TestPotentialIsAvoid(t *testing.T) {
	cases := []struct {
		name string
		it   PotentialItem
		want bool
	}{
		{"没积累的暴涨", PotentialItem{Ret4h: 0.12, Acc3d: -3e5, Acc3dPre4h: -3e5}, true},
		// SAND 2026-10-02：拉升前 3 天净流出，拉升这一小时放量进了 125 万
		{"拉升前没积累、拉升当下进钱", PotentialItem{Ret4h: 0.27, Acc3d: 9e5, Acc3dPre4h: -3.4e5}, true},
		{"拉升前有积累、拉升当下流出", PotentialItem{Ret4h: 0.15, Acc3d: 2e5, Acc3dPre4h: 8e5}, true},
		{"前后都有积累", PotentialItem{Ret4h: 0.15, Acc3d: 9e5, Acc3dPre4h: 8e5}, false},
		{"涨得不够", PotentialItem{Ret4h: 0.076, Acc3d: -3.4e5, Acc3dPre4h: -3.4e5}, false},
	}
	for _, c := range cases {
		if got := potentialIsAvoid(c.it); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPotentialIsSmallCoin(t *testing.T) {
	if !potentialIsSmallCoin(5e8, 1e9) || potentialIsSmallCoin(2e9, 1e6) {
		t.Fatal("market cap decides when known")
	}
	if !potentialIsSmallCoin(0, 4e7) || potentialIsSmallCoin(0, 6e7) {
		t.Fatal("open interest decides when market cap unknown")
	}
}

func TestPotentialOutcome(t *testing.T) {
	const m15 = 15 * 60 * 1000
	entered := int64(100) * potentialTestHour
	bars := potentialTestBars(120, m15, entered, func(i int) float64 { return 10 })
	bars[15].Close = 10.4 // 4h 收盘（第 16 根）
	bars[95].Close = 9.0  // 24h 收盘（第 96 根）
	bars[30].High = 10.6  // 先涨到 +6%
	bars[40].Low = 9.4    // 之后才跌到 -6%

	o := potentialOutcome(bars, entered, 10, entered+30*potentialTestHour)
	if o.Ret4h == nil || math.Abs(*o.Ret4h-0.04) > 1e-9 {
		t.Fatalf("ret4h = %v", o.Ret4h)
	}
	if o.Ret24h == nil || math.Abs(*o.Ret24h+0.1) > 1e-9 {
		t.Fatalf("ret24h = %v", o.Ret24h)
	}
	if o.FirstTouch5 == nil || *o.FirstTouch5 != 1 {
		t.Fatalf("touch = %v, want 1", o.FirstTouch5)
	}

	bars[30].Low = 9.4 // 同一根两边都碰到，按先跌算
	if o := potentialOutcome(bars, entered, 10, entered+30*potentialTestHour); *o.FirstTouch5 != -1 {
		t.Fatalf("touch = %d, want -1 when both hit in one bar", *o.FirstTouch5)
	}

	early := potentialOutcome(bars, entered, 10, entered+5*potentialTestHour)
	if early.Ret4h == nil || early.Ret24h != nil || early.FirstTouch5 != nil {
		t.Fatal("before 24h only ret4h should be filled")
	}
}

func TestPotentialRecordFillAndSummarize(t *testing.T) {
	store := openBollPumpTestStore(t)
	defer store.Close()
	ctx := context.Background()
	const m15 = 15 * 60 * 1000
	entered := int64(1000) * potentialTestHour
	bars := potentialTestBars(120, m15, entered, func(i int) float64 { return 10 })
	bars[95].Close = 11
	bars[20].High = 10.6
	src := &fakeBollPumpSource{bars: map[string][]BollPumpBar{"15m": bars}}
	s := NewPotentialScanner(src, nil, nil, store)

	item := PotentialItem{Symbol: "XYZUSDT", DecisionMs: entered, Price: 10, Acc3d: 6e6}
	if err := s.record(ctx, PotentialListWatch, []PotentialItem{item}); err != nil {
		t.Fatal(err)
	}
	item.DecisionMs += 5 * potentialTestHour // 24h 内再次上榜，不重复记
	if err := s.record(ctx, PotentialListWatch, []PotentialItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := s.fillOutcomes(ctx, entered+25*potentialTestHour); err != nil {
		t.Fatal(err)
	}
	picks, stats, err := GetPotentialHistory(ctx, store, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(picks) != 1 {
		t.Fatalf("picks = %d, want 1 (deduped within 24h)", len(picks))
	}
	p := picks[0]
	if p.Ret24h == nil || math.Abs(*p.Ret24h-0.1) > 1e-9 || p.FirstTouch5 == nil || *p.FirstTouch5 != 1 {
		t.Fatalf("pick outcome = %+v", p)
	}
	if stats[0].List != PotentialListWatch || stats[0].Done != 1 || stats[0].TouchUp5 != 1 || math.Abs(stats[0].Median24h-0.1) > 1e-9 {
		t.Fatalf("stats = %+v", stats[0])
	}
}
