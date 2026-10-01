package service

import (
	"testing"

	"coinmark/api-go/internal/marketstate"
)

const archT0 = int64(1790812800000) // 2026-10-01 00:00 UTC

// archBars 从 archT0 起按分钟生成 K 线，closes[i] 同时作为该分钟的开高低收。
func archBars(closes ...float64) []marketstate.Bar {
	out := make([]marketstate.Bar, len(closes))
	for i, c := range closes {
		out[i] = marketstate.Bar{StartMs: archT0 + int64(i)*yidongMinuteMs, Open: c, High: c, Low: c, Close: c, QuoteNotional: 1, Trades: 1}
	}
	return out
}

func repeatF(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestArchDetectPriceShock(t *testing.T) {
	cases := []struct {
		name    string
		bars    []marketstate.Bar
		price   float64
		want    bool
		up      bool
		minutes int
	}{
		{"2 分钟涨 3%", archBars(100, 100, 100, 100, 103), 103.1, true, true, 2},
		{"5 分钟内不足 3%，10 分钟不足 5%", archBars(100, 101, 101, 102, 102, 102, 102, 102, 102, 102), 104, false, false, 0},
		{"8 分钟涨 5%", archBars(100, 101, 102.5, 102.5, 103, 103, 104, 104.5), 105.1, true, true, 8},
		{"1 分钟跌 3%", archBars(100, 100, 100), 96.9, true, false, 1},
		{"20 分钟涨 10%（16~45 分钟档）", archBars(append([]float64{100}, repeatF(109, 19)...)...), 110.5, true, true, 20},
	}
	for _, tc := range cases {
		got, ok := archDetectPriceShock(tc.bars, tc.price)
		if ok != tc.want {
			t.Fatalf("%s: ok=%v want %v (%+v)", tc.name, ok, tc.want, got)
		}
		if ok && (got.Up != tc.up || got.Minutes != tc.minutes) {
			t.Fatalf("%s: got %+v, want up=%v minutes=%d", tc.name, got, tc.up, tc.minutes)
		}
	}
}

func TestArchMessageFormat(t *testing.T) {
	head := archHead("MOVRUSDT", 2.4733, 13.3617)
	if head != "MOVR - ₮2.4733 (13.36%)" {
		t.Fatalf("head=%q", head)
	}
	if got := archHead("USUSDT", 0.023065, -14.014); got != "US - ₮0.023065 (-14.01%)" {
		t.Fatalf("负涨幅 head=%q", got)
	}
	if got := head + archShockBody(archShock{Up: true, Minutes: 5, Pct: 5.0234}); got != "MOVR - ₮2.4733 (13.36%) , 5分钟涨 5.02%" {
		t.Fatalf("shock=%q", got)
	}
	if got := archShockBody(archShock{Up: false, Minutes: 2, Pct: -3.678}); got != " , 2分钟跌 -3.68%" {
		t.Fatalf("down=%q", got)
	}
	if got := archFundingBody(0.005019); got != " , 资金费异常: +0.5019%" {
		t.Fatalf("funding=%q", got)
	}
	if got := archFundingBody(-0.00505); got != " , 资金费异常: -0.5050%" {
		t.Fatalf("funding neg=%q", got)
	}
	if got := archVolumeBody("中继", 41, 999800, 1219268, -262800, 9.4512); got != " (中继) , 41分钟成交99.98万 = 昨日82% , 今日净流出-26.28万 , 量能9.45x " {
		t.Fatalf("volume=%q", got)
	}
	if got := archVolumeBody("底部", 60, 2260600, 2825750, 359800, 7.07); got != " (底部) , 60分钟成交226.06万 = 昨日80% , 今日净流入35.98万 , 量能7.07x " {
		t.Fatalf("volume inflow=%q", got)
	}
	if got := archSuffix(33, 14); got != " (33天首次) [14]" {
		t.Fatalf("suffix=%q", got)
	}
	if got := archSuffix(0, 3); got != " [3]" {
		t.Fatalf("suffix no first=%q", got)
	}
}

func TestArchVolumeWindow(t *testing.T) {
	// 每分钟成交 10，共 60 分钟；昨日 400 → 80% = 320，需 32 分钟
	bars := make([]marketstate.Bar, 60)
	for i := range bars {
		bars[i] = marketstate.Bar{StartMs: archT0 + int64(i)*yidongMinuteMs, QuoteNotional: 10, Trades: 1}
	}
	now := archT0 + 59*yidongMinuteMs
	n, vol, ok := archVolumeWindow(bars, now, 400, false)
	if !ok || n != 32 || vol != 320 {
		t.Fatalf("首次应取达到 80%% 的最短窗口: n=%d vol=%v ok=%v", n, vol, ok)
	}
	n, vol, ok = archVolumeWindow(bars, now, 400, true)
	if !ok || n != 60 || vol != 600 {
		t.Fatalf("持续期间应报告完整 60 分钟: n=%d vol=%v ok=%v", n, vol, ok)
	}
	if _, _, ok := archVolumeWindow(bars, now, 1000, false); ok {
		t.Fatal("60 分钟不足昨日 80% 不应触发")
	}
	if _, _, ok := archVolumeWindow(bars, now, 0, false); ok {
		t.Fatal("没有昨日成交不应触发")
	}
}

func TestArchDailyCounterResetsAtUTCDay(t *testing.T) {
	c := newArchDailyCounter()
	if c.next("AUSDT", archT0) != 1 || c.next("AUSDT", archT0) != 2 || c.next("BUSDT", archT0) != 1 {
		t.Fatal("同一天应按币累计")
	}
	c.seed(archT0, map[string]int{"CUSDT": 7})
	if c.next("CUSDT", archT0) != 8 {
		t.Fatal("应从重启前的计数继续")
	}
	if c.next("AUSDT", archT0+yidongDayMs) != 1 {
		t.Fatal("新的一天应清零")
	}
}

func TestArchIntradayExtra(t *testing.T) {
	// 当日上涨、下跌提醒、距日内高点回撤 ≥5%：附加日内回调
	if got, ok := archIntradayExtra(false, 4.52, 0.04095, 0.0453, 0.038); !ok || got != " , 日内回调 -9.60%" {
		t.Fatalf("回调=%q ok=%v", got, ok)
	}
	// 当日下跌、上涨提醒、距日内低点反弹 ≥5%：附加日内反弹
	if got, ok := archIntradayExtra(true, -14.01, 0.023065, 0.03, 0.021354); !ok || got != " , 日内反弹 8.01%" {
		t.Fatalf("反弹=%q ok=%v", got, ok)
	}
	if _, ok := archIntradayExtra(false, 4.0, 98, 100, 90); ok {
		t.Fatal("回撤不足 5% 不附加")
	}
	if _, ok := archIntradayExtra(true, 4.0, 110, 120, 100); ok {
		t.Fatal("当日上涨时的上涨提醒不附加")
	}
}
