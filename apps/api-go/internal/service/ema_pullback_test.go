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
