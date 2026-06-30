package service

import (
	"strings"
	"testing"

	"coinmark/api-go/internal/model"
)

func TestBollPumpWatchTriggerScoresQuietBase(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if !got.Triggered {
		t.Fatalf("Triggered = false, want true; reasons=%v", got.Reasons)
	}
	if got.Signal.SignalLevel != string(BollPumpLevelWatch) {
		t.Fatalf("level = %s, want WATCH", got.Signal.SignalLevel)
	}
	if got.Signal.Score <= 100 {
		t.Fatalf("score = %.2f, want > 100 without score ceiling", got.Signal.Score)
	}
}

func TestBollPumpWatchTriggerRejectsLargeBearishStart(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	last := len(bars) - 1
	bars[last].Open = bars[last].High
	bars[last].Close = bars[last].Low
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if got.Triggered {
		t.Fatalf("Triggered = true, want false")
	}
}

func TestBollPumpWatchTrendScorePenalizesWickHeavyStartup(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureWickHeavyPump()
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if !got.Triggered {
		t.Fatalf("Triggered = false, want true; reasons=%v", got.Reasons)
	}
	if got.Signal.Score > 85 {
		t.Fatalf("score = %.2f, want <= 85 after wick penalty", got.Signal.Score)
	}
	if !strings.Contains(got.Signal.Reason, "wick-heavy startup") {
		t.Fatalf("reason = %q, want wick-heavy startup", got.Signal.Reason)
	}
}

func TestBollPumpStartupTrendScoreOnlyPenalizesWicks(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := []BollPumpBar{
		{Open: 100.0, High: 101.0, Low: 99.8, Close: 100.8},
		{Open: 100.8, High: 101.0, Low: 98.0, Close: 98.6},
		{Open: 98.6, High: 101.6, Low: 98.4, Close: 101.0},
		{Open: 101.0, High: 101.2, Low: 99.0, Close: 99.4},
		{Open: 99.4, High: 102.2, Low: 99.2, Close: 101.4},
		{Open: 101.4, High: 101.6, Low: 100.0, Close: 100.2},
		{Open: 100.2, High: 102.8, Low: 100.0, Close: 102.0},
	}

	score, reasons := bollPumpStartupTrendScore(bars, 0, len(bars)-1, cfg)
	if score < 0 {
		t.Fatalf("score = %.2f, want no non-wick penalty; reasons=%v", score, reasons)
	}
	if strings.Contains(strings.Join(reasons, ","), "low trend efficiency") {
		t.Fatalf("reasons = %v, want no low trend efficiency penalty", reasons)
	}
}

func TestBollPumpConfirmFlowWaitsUntilBreaksPullbackHigh(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureWatchThenTwoConfirms()
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "15m")
	var signals []model.BollPumpSignal
	for i := 0; i < len(bars); i++ {
		out := AdvanceBollPumpState(&state, bars[:i+1], ind[:i+1], 3_000_000, cfg)
		signals = append(signals, out.Signals...)
	}

	if len(signals) < 3 {
		t.Fatalf("signals = %d, want at least WATCH, CONFIRM_1, CONFIRM_2", len(signals))
	}
	if signals[len(signals)-1].SignalLevel != string(BollPumpLevelConfirm2) {
		t.Fatalf("last signal = %s, want CONFIRM_2", signals[len(signals)-1].SignalLevel)
	}
	if signals[0].Score <= 100 {
		t.Fatalf("watch score = %.2f, want > 100 without score ceiling", signals[0].Score)
	}
	if signals[len(signals)-1].Score <= signals[0].Score {
		t.Fatalf("final score = %.2f, want above watch score %.2f", signals[len(signals)-1].Score, signals[0].Score)
	}
	if state.Status != string(BollPumpStatusCompleted) {
		t.Fatalf("status = %s, want COMPLETED", state.Status)
	}
}

func TestBollPumpWatchInvalidatesWhenTrendFails(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	for _, closePrice := range []float64{104.0, 103.4, 102.8, 102.2, 101.8, 101.4, 101.0} {
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open:        closePrice + 0.2,
			High:        closePrice + 0.3,
			Low:         closePrice - 0.2,
			Close:       closePrice,
			Volume:      100,
			QuoteVolume: 100 * closePrice,
			Closed:      true,
		})
	}
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "15m")
	for i := 0; i < len(bars); i++ {
		AdvanceBollPumpState(&state, bars[:i+1], ind[:i+1], 3_000_000, cfg)
	}

	if state.Status != string(BollPumpStatusInvalidated) {
		t.Fatalf("status = %s, want INVALIDATED; watch=%d checked=%d score=%.2f", state.Status, state.WatchCandleStartMs, state.LastCheckedCandleMs, state.CurrentScore)
	}
}

func TestBollPumpRejectsWeakLowerBandBounce(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureWeakLowerBandBounce()
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "15m")
	var signals []model.BollPumpSignal
	for i := 0; i < len(bars); i++ {
		out := AdvanceBollPumpState(&state, bars[:i+1], ind[:i+1], 3_000_000, cfg)
		signals = append(signals, out.Signals...)
	}

	for _, sig := range signals {
		if sig.SignalLevel == string(BollPumpLevelConfirm1) || sig.SignalLevel == string(BollPumpLevelConfirm2) {
			t.Fatalf("weak bounce emitted %s, want no confirm", sig.SignalLevel)
		}
	}
}

func TestBollPumpSecondLowInvalidation(t *testing.T) {
	firstLow := 100.0
	atr := 2.0
	if !bollPumpSecondLowInvalid(firstLow, 97.5, atr) {
		t.Fatalf("second low should be invalid")
	}
	if bollPumpSecondLowInvalid(firstLow, 98.2, atr) {
		t.Fatalf("second low should remain valid")
	}
}

func bollPumpFixtureQuietBaseThenPump(tf string) []BollPumpBar {
	_ = tf
	bars := make([]BollPumpBar, 0, 140)
	for i := 0; i < 120; i++ {
		closePrice := 100 + float64(i%3-1)*0.05
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(i) * 60000,
			CloseTimeMs: int64(i+1)*60000 - 1,
			Open:        closePrice - 0.01,
			High:        closePrice + 0.08,
			Low:         closePrice - 0.08,
			Close:       closePrice,
			Volume:      80,
			QuoteVolume: 8000,
			Closed:      true,
		})
	}
	pumps := []float64{100.5, 101.2, 102.4, 103.6, 104.2, 104.8}
	for i, closePrice := range pumps {
		vol := 120.0
		if i == 2 {
			vol = 600
		}
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open:        closePrice - 0.4,
			High:        closePrice + 0.6,
			Low:         closePrice - 0.5,
			Close:       closePrice,
			Volume:      vol,
			QuoteVolume: vol * closePrice,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureWickHeavyPump() []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenPump("15m")[:120]
	pumps := []float64{100.8, 101.5, 102.2, 103.0, 104.0, 105.0}
	for i, closePrice := range pumps {
		vol := 120.0
		if i == 2 {
			vol = 600
		}
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open:        closePrice - 0.02,
			High:        closePrice + 2.6,
			Low:         closePrice - 2.1,
			Close:       closePrice,
			Volume:      vol,
			QuoteVolume: vol * closePrice,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureWatchThenTwoConfirms() []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	extra := []struct {
		open  float64
		high  float64
		low   float64
		close float64
		vol   float64
	}{
		{104.8, 105.3, 104.4, 105.0, 130},
		{105.0, 105.4, 104.5, 105.1, 125},
		{105.1, 105.5, 104.7, 105.2, 120},
		{104.0, 104.0, 90.0, 103.0, 140},
		{103.2, 104.5, 102.8, 104.2, 150},
		{104.2, 105.0, 103.8, 104.8, 130},
		{103.8, 104.0, 90.5, 103.0, 140},
		{103.2, 104.6, 102.9, 104.3, 150},
	}
	for _, x := range extra {
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open:        x.open,
			High:        x.high,
			Low:         x.low,
			Close:       x.close,
			Volume:      x.vol,
			QuoteVolume: x.vol * x.close,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureWeakLowerBandBounce() []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	extra := []struct {
		open  float64
		high  float64
		low   float64
		close float64
		vol   float64
	}{
		{104.8, 105.3, 104.4, 105.0, 130},
		{105.0, 105.4, 104.5, 105.1, 125},
		{105.1, 105.5, 104.7, 105.2, 120},
		{104.0, 104.0, 90.0, 103.0, 140},
		{103.2, 104.5, 100.0, 100.5, 150},
	}
	for _, x := range extra {
		idx := len(bars)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(idx) * 60000,
			CloseTimeMs: int64(idx+1)*60000 - 1,
			Open:        x.open,
			High:        x.high,
			Low:         x.low,
			Close:       x.close,
			Volume:      x.vol,
			QuoteVolume: x.vol * x.close,
			Closed:      true,
		})
	}
	return bars
}
