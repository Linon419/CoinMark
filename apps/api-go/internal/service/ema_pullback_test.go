package service

import (
	"strings"
	"testing"
)

// emaPullbackTestBars 450 根缓涨（EMA100 > EMA200 且向上）的 1h K 线，最后一根的收盘、最低由 adjust 设定。
func emaPullbackTestBars(adjust func(bars []BollPumpBar, e100, e200 float64)) []BollPumpBar {
	bars := potentialTestBars(450, potentialTestHour, 0, func(i int) float64 { return 100 + 0.1*float64(i) })
	e100 := bollSqueezeEMA(bars, 100)
	e200 := bollSqueezeEMA(bars, 200)
	adjust(bars, e100[len(bars)-2], e200[len(bars)-2])
	return bars
}

func TestEvaluateEMAPullbackTouchesEMA100(t *testing.T) {
	bars := emaPullbackTestBars(func(bars []BollPumpBar, e100, _ float64) {
		last := &bars[len(bars)-1]
		last.Close = e100 * 1.02
		last.Low = e100 * 0.99 // 插到 EMA100 下方，收回线上
	})
	hit, ok := EvaluateEMAPullback(bars)
	if !ok || hit.Line != "EMA100" || hit.DistancePct <= 0 {
		t.Fatalf("hit=%+v ok=%v, want EMA100 pullback", hit, ok)
	}
}

func TestEvaluateEMAPullbackPrefersDeeperEMA200(t *testing.T) {
	bars := emaPullbackTestBars(func(bars []BollPumpBar, _, e200 float64) {
		last := &bars[len(bars)-1]
		last.Close = e200 * 1.02 // 收在 EMA200 上方、EMA100 下方
		last.Low = e200 * 1.002  // 离 EMA200 不到 0.3%
	})
	hit, ok := EvaluateEMAPullback(bars)
	if !ok || hit.Line != "EMA200" {
		t.Fatalf("hit=%+v ok=%v, want EMA200 pullback", hit, ok)
	}
	if got := formatEMAPullbackNotify("ZECUSDT", EMAPullbackHit{Timeframe: "4h", Line: hit.Line, Close: hit.Close, Low: hit.Low, EMA100: hit.EMA100, EMA200: hit.EMA200, DistancePct: hit.DistancePct}); !strings.HasPrefix(got, "EMA 回踩 · ZEC 4h\n回踩 EMA200") {
		t.Fatalf("title = %q", got)
	}
}

func TestEvaluateEMAPullbackSkips(t *testing.T) {
	cases := map[string][]BollPumpBar{
		// 收盘跌破了线：不算回踩
		"close below line": emaPullbackTestBars(func(bars []BollPumpBar, e100, e200 float64) {
			bars[len(bars)-1].Close = e200 * 0.98
			bars[len(bars)-1].Low = e200 * 0.97
		}),
		// 低点离线还远
		"not touched": emaPullbackTestBars(func(bars []BollPumpBar, e100, _ float64) {
			bars[len(bars)-1].Close = e100 * 1.05
			bars[len(bars)-1].Low = e100 * 1.02
		}),
		// 之前几根已经在 EMA100 下方来回：不是从上方回落
		"chop around line": emaPullbackTestBars(func(bars []BollPumpBar, e100, e200 float64) {
			for i := len(bars) - 6; i < len(bars)-1; i++ {
				bars[i].Close = e200 * 1.01
			}
			bars[len(bars)-1].Close = e100 * 1.01
			bars[len(bars)-1].Low = e100 * 0.995
		}),
	}
	for name, bars := range cases {
		if hit, ok := EvaluateEMAPullback(bars); ok {
			t.Fatalf("%s: unexpected hit %+v", name, hit)
		}
	}
	// 下跌趋势（EMA100 < EMA200）
	down := potentialTestBars(450, potentialTestHour, 0, func(i int) float64 { return 200 - 0.1*float64(i) })
	e100 := bollSqueezeEMA(down, 100)
	down[449].Close, down[449].Low = e100[448]*1.01, e100[448]*0.99
	if _, ok := EvaluateEMAPullback(down); ok {
		t.Fatal("downtrend: unexpected hit")
	}
	if _, ok := EvaluateEMAPullback(down[:300]); ok {
		t.Fatal("too few bars: unexpected hit")
	}
}

// emaPullbackDownBars 450 根缓跌（EMA100 < EMA200 且向下）的 1h K 线，最后一根由 adjust 设定。
func emaPullbackDownBars(adjust func(bars []BollPumpBar, e100, e200 float64)) []BollPumpBar {
	bars := potentialTestBars(450, potentialTestHour, 0, func(i int) float64 { return 200 - 0.1*float64(i) })
	e100 := bollSqueezeEMA(bars, 100)
	e200 := bollSqueezeEMA(bars, 200)
	adjust(bars, e100[len(bars)-2], e200[len(bars)-2])
	return bars
}

func TestEvaluateEMAPullbackShort(t *testing.T) {
	bars := emaPullbackDownBars(func(bars []BollPumpBar, e100, _ float64) {
		last := &bars[len(bars)-1]
		last.Open, last.Close = e100*0.985, e100*0.98
		last.High = e100 * 1.01 // 冲到 EMA100 上方，收回线下
		last.Low = e100 * 0.979
	})
	hit, ok := EvaluateEMAPullbackShort(bars)
	if !ok || hit.Side != "short" || hit.Line != "EMA100" || hit.DistancePct >= 0 {
		t.Fatalf("hit=%+v ok=%v, want short EMA100", hit, ok)
	}
	if hit.WickRatio < 0.5 {
		t.Fatalf("上影线占整根 %.2f，应 ≥ 0.5", hit.WickRatio)
	}
	if got := formatEMAPullbackNotify("ZECUSDT", EMAPullbackHit{Side: "short", Timeframe: "1h", Line: hit.Line, Close: hit.Close, High: hit.High, EMA100: hit.EMA100, EMA200: hit.EMA200, DistancePct: hit.DistancePct}); !strings.HasPrefix(got, "EMA 反抽 · ZEC 1h\n反抽 EMA100") {
		t.Fatalf("title = %q", got)
	}
	if _, ok := EvaluateEMAPullback(bars); ok {
		t.Fatal("下跌趋势不应判为多头回踩")
	}

	skips := map[string][]BollPumpBar{
		"close above line": emaPullbackDownBars(func(bars []BollPumpBar, e100, _ float64) {
			bars[len(bars)-1].Close, bars[len(bars)-1].High = e100*1.01, e100*1.02
		}),
		"not touched": emaPullbackDownBars(func(bars []BollPumpBar, e100, _ float64) {
			bars[len(bars)-1].Close, bars[len(bars)-1].High = e100*0.95, e100*0.98
		}),
		"uptrend": emaPullbackTestBars(func(bars []BollPumpBar, e100, _ float64) {
			bars[len(bars)-1].Close, bars[len(bars)-1].High = e100*0.99, e100*1.01
		}),
	}
	for name, b := range skips {
		if hit, ok := EvaluateEMAPullbackShort(b); ok {
			t.Fatalf("%s: unexpected short hit %+v", name, hit)
		}
	}
	if hit, ok := EvaluateEMAPullback(emaPullbackTestBars(func(bars []BollPumpBar, e100, _ float64) {
		bars[len(bars)-1].Close, bars[len(bars)-1].Low = e100*1.02, e100*0.99
	})); !ok || hit.Side != "long" {
		t.Fatalf("多头命中应标 side=long: %+v", hit)
	}
}

func TestScoreEMAPullbackHits(t *testing.T) {
	hits := []EMAPullbackHit{
		{Side: "long", Timeframe: "15m", WickRatio: 0.6},
		{Side: "long", Timeframe: "1h", WickRatio: 0.1},
		{Side: "short", Timeframe: "4h", WickRatio: 0.7},
	}
	trends := map[string]int{"30m": 1, "4h": -1}
	scoreEMAPullbackHits(hits, trends)
	// 15m 多：60 + 共振 1 个周期 10 + 上一级 30m 向上 10 + 影线 10
	// 1h 多：60 + 共振 10（上一级 4h 向下，不加；影线短，不加）
	// 4h 空：60 + 影线 10（没有同向的其他周期；4h 没有上一级）
	for i, want := range []float64{90, 70, 70} {
		if hits[i].Score != want {
			t.Fatalf("%s %s 分数 = %v, want %v (%v)", hits[i].Side, hits[i].Timeframe, hits[i].Score, want, hits[i].Reasons)
		}
	}
	many := []EMAPullbackHit{{Side: "long", Timeframe: "15m"}, {Side: "long", Timeframe: "30m"}, {Side: "long", Timeframe: "1h"}, {Side: "long", Timeframe: "4h"}}
	scoreEMAPullbackHits(many, nil)
	if many[0].Score != 80 {
		t.Fatalf("共振最多加 20: %v", many[0].Score)
	}
}
