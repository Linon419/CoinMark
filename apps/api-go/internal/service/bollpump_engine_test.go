package service

import (
	"strings"
	"testing"

	"coinmark/api-go/internal/model"
)

func TestBollPumpWatchTriggerScoresQuietBase(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout("15m")
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
	if !strings.Contains(got.Signal.Reason, "15m resistance breakout") {
		t.Fatalf("reason = %q, want current timeframe resistance breakout", got.Signal.Reason)
	}
}

func TestBollPumpWatchRequiresCurrentTimeframeResistanceBreakout(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenPump("15m")
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if got.Triggered {
		t.Fatalf("Triggered = true, want false without current timeframe resistance breakout")
	}
	if !strings.Contains(strings.Join(got.Reasons, ","), "current timeframe resistance breakout") {
		t.Fatalf("reasons = %v, want current timeframe resistance breakout reason", got.Reasons)
	}
}

func TestBollPumpWatchTriggerRejectsLargeBearishStart(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout("15m")
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
	bars := bollPumpFixtureWickHeavyResistanceBreakout()
	ind := ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)

	got := EvaluateBollPumpWatch("swap", "XYZUSDT", "15m", bars, ind, 3_000_000, cfg)
	if !got.Triggered {
		t.Fatalf("Triggered = false, want true; reasons=%v", got.Reasons)
	}
	if got.Signal.Score > 95 {
		t.Fatalf("score = %.2f, want <= 95 after wick penalty and resistance breakout score", got.Signal.Score)
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

func TestBollPumpMinimumTrendRequiresClearFifteenMinuteUptrend(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	clear := bollPumpFixtureClearTrend(100, 8)
	got := bollPumpMinimumTrendGate(clear, cfg)
	if !got.Pass {
		t.Fatalf("clear trend pass = false, reason=%q", got.Reason)
	}

	choppy := bollPumpFixtureChoppyTrend(100, 8)
	got = bollPumpMinimumTrendGate(choppy, cfg)
	if got.Pass {
		t.Fatalf("choppy trend pass = true, want false")
	}
	if !strings.Contains(got.Reason, "15m trend") {
		t.Fatalf("reason = %q, want 15m trend context", got.Reason)
	}
}

func TestBollPumpFourHourResistanceBreakoutFindsKeySwingCluster(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Resistance4HLookback = 40
	cfg.Resistance4HMinTouches = 2
	cfg.Resistance4HBreakoutBufferPct = 0.003
	cfg.Resistance4HMaxDistancePct = 0.04
	cfg.Resistance4HBreakoutBonus = 15

	got := bollPumpFourHourResistanceBreakout(bollPumpFixtureFourHourResistanceBreakout(), cfg)
	if !got.Triggered {
		t.Fatalf("Triggered = false, want true")
	}
	if got.Touches < 2 {
		t.Fatalf("touches = %d, want >= 2", got.Touches)
	}
	if got.Bonus != 15 {
		t.Fatalf("bonus = %.2f, want 15", got.Bonus)
	}
	if !strings.Contains(got.Reason, "4h resistance breakout") {
		t.Fatalf("reason = %q, want 4h resistance breakout", got.Reason)
	}
}

func TestBollPumpFourHourResistanceBreakoutRequiresCloseAboveBuffer(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.Resistance4HLookback = 40
	cfg.Resistance4HMinTouches = 2
	cfg.Resistance4HBreakoutBufferPct = 0.01
	cfg.Resistance4HMaxDistancePct = 0.005
	cfg.Resistance4HBreakoutBonus = 15
	bars := bollPumpFixtureFourHourResistanceBreakout()
	bars[len(bars)-1].Close = 106.8

	got := bollPumpFourHourResistanceBreakout(bars, cfg)
	if got.Triggered {
		t.Fatalf("Triggered = true, want false")
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
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout("15m")
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

func TestBollPumpWatchExtendsExpiryWhileTrendContinues(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.StageExpiryCandles = 5
	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "3m")
	state.Status = string(BollPumpStatusWatch)
	state.WatchScore = 100
	state.CurrentScore = 100
	state.WatchCandleStartMs = 0
	state.WatchStartedMs = 59_999
	state.LastCheckedCandleMs = 0
	state.ExpiresAtCandleMs = 120_000
	state.LastSignalLevel = string(BollPumpLevelWatch)

	bars := []BollPumpBar{
		{OpenTimeMs: 0, CloseTimeMs: 59_999, Open: 100, High: 101, Low: 99, Close: 100, Closed: true},
		{OpenTimeMs: 60_000, CloseTimeMs: 119_999, Open: 100, High: 102, Low: 100, Close: 101, Closed: true},
		{OpenTimeMs: 180_000, CloseTimeMs: 239_999, Open: 101, High: 103, Low: 100.5, Close: 102, Closed: true},
	}
	ind := []BollPumpIndicator{
		{Lower: 95, Middle: 100, Upper: 105, ValidBoll: true},
		{Lower: 96, Middle: 100, Upper: 106, ValidBoll: true},
		{Lower: 97, Middle: 101, Upper: 107, ValidBoll: true},
	}

	AdvanceBollPumpState(&state, bars, ind, 3_000_000, cfg)

	if state.Status != string(BollPumpStatusWatch) {
		t.Fatalf("status = %s, want WATCH while trend continues", state.Status)
	}
	if state.ExpiresAtCandleMs <= 180_000 {
		t.Fatalf("expires_at = %d, want refreshed beyond latest candle", state.ExpiresAtCandleMs)
	}
}

func TestBollPumpPullbackCandidateRefreshesExpiryWindow(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.StageExpiryCandles = 5
	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "3m")
	state.Status = string(BollPumpStatusPullback1Pending)
	state.WatchScore = 100
	state.CurrentScore = 100
	state.WatchCandleStartMs = 0
	state.WatchStartedMs = 59_999
	state.PendingPullbackCandleMs = 60_000
	state.PendingPullbackHigh = 103
	state.PendingPullbackLow = 99
	state.LastCheckedCandleMs = 60_000
	state.ExpiresAtCandleMs = 120_000
	state.LastSignalLevel = string(BollPumpLevelWatch)

	bars := []BollPumpBar{
		{OpenTimeMs: 60_000, CloseTimeMs: 119_999, Open: 102, High: 103, Low: 99, Close: 101, Closed: true},
		{OpenTimeMs: 180_000, CloseTimeMs: 239_999, Open: 101, High: 102, Low: 96, Close: 98, Closed: true},
	}
	ind := []BollPumpIndicator{
		{Lower: 97, Middle: 101, Upper: 105, ValidBoll: true},
		{Lower: 97, Middle: 101, Upper: 105, ValidBoll: true},
	}

	AdvanceBollPumpState(&state, bars, ind, 3_000_000, cfg)

	if state.Status != string(BollPumpStatusPullback1Pending) {
		t.Fatalf("status = %s, want PULLBACK_1_PENDING after fresh pullback candidate", state.Status)
	}
	if state.PendingPullbackCandleMs != 180_000 {
		t.Fatalf("pending candle = %d, want latest candle", state.PendingPullbackCandleMs)
	}
	if state.ExpiresAtCandleMs <= 180_000 {
		t.Fatalf("expires_at = %d, want refreshed beyond latest pullback", state.ExpiresAtCandleMs)
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

func TestBollPumpConfirmInvalidatesWhenCloseFallsBelowLowerBand(t *testing.T) {
	state := NewBollPumpRuntimeState("swap", "XYZUSDT", "15m")
	state.Status = string(BollPumpStatusConfirm1)
	state.WatchScore = 110
	state.CurrentScore = 120
	state.WatchCandleStartMs = 60_000
	state.WatchStartedMs = 119_999
	state.BounceCount = 1
	state.FirstPullbackLow = 99
	state.ExpiresAtCandleMs = 600_000
	state.LastCheckedCandleMs = 120_000
	state.LastSignalLevel = string(BollPumpLevelConfirm1)

	bars := []BollPumpBar{
		{OpenTimeMs: 120_000, CloseTimeMs: 179_999, Open: 105, High: 106, Low: 103, Close: 104, Closed: true},
		{OpenTimeMs: 180_000, CloseTimeMs: 239_999, Open: 104, High: 104.5, Low: 96, Close: 98, Closed: true},
	}
	ind := []BollPumpIndicator{
		{Lower: 100, Middle: 110, Upper: 120, Bandwidth: 0.18, ATR14: 2, ValidBoll: true, ValidATR: true},
		{Lower: 100, Middle: 110, Upper: 120, Bandwidth: 0.18, ATR14: 2, ValidBoll: true, ValidATR: true},
	}

	AdvanceBollPumpState(&state, bars, ind, 3_000_000, DefaultBollPumpConfig())

	if state.Status != string(BollPumpStatusInvalidated) {
		t.Fatalf("status = %s, want INVALIDATED after close below lower band", state.Status)
	}
}

func TestBollPumpPullbackCandidateRequiresLowerBandTouchAndRecovery(t *testing.T) {
	in := BollPumpIndicator{Lower: 100, Middle: 110, ValidBoll: true}

	if bollPumpIsPullbackCandidate(BollPumpBar{Low: 105.5, Close: 106}, in) {
		t.Fatalf("near lower-band hold should stay out of pullback candidates")
	}
	if !bollPumpIsPullbackCandidate(BollPumpBar{Low: 99.8, Close: 100.4}, in) {
		t.Fatalf("lower-band touch with close recovery should be a pullback candidate")
	}
	if bollPumpIsPullbackCandidate(BollPumpBar{Low: 99.8, Close: 99.7}, in) {
		t.Fatalf("lower-band break without close recovery should stay out of pullback candidates")
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

func bollPumpFixtureQuietBaseThenResistanceBreakout(tf string) []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenPump(tf)
	for _, i := range []int{75, 100, 115} {
		bars[i].High = 101.1
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

func bollPumpFixtureWickHeavyResistanceBreakout() []BollPumpBar {
	bars := bollPumpFixtureWickHeavyPump()
	for _, i := range []int{75, 100, 115} {
		bars[i].High = 101.1
	}
	return bars
}

func bollPumpFixtureWatchThenTwoConfirms() []BollPumpBar {
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout("15m")
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
	bars := bollPumpFixtureQuietBaseThenResistanceBreakout("15m")
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

func bollPumpFixtureFourHourResistanceBreakout() []BollPumpBar {
	bars := make([]BollPumpBar, 0, 70)
	closes := []float64{
		95, 96, 97, 98, 99, 104, 100, 98, 97, 99,
		101, 103, 100, 98, 96, 97, 99, 102, 105, 101,
		99, 98, 100, 102, 104, 101, 99, 100, 102, 103,
		101, 100, 102, 104, 103, 102, 104, 106, 107, 109.2,
	}
	for i, closePrice := range closes {
		high := closePrice + 0.6
		if i == 5 {
			high = 106.2
		}
		if i == 18 {
			high = 106.5
		}
		if i == 24 {
			high = 106.3
		}
		if i == len(closes)-1 {
			high = 110.0
		}
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(i) * 4 * 60 * 60 * 1000,
			CloseTimeMs: int64(i+1)*4*60*60*1000 - 1,
			Open:        closePrice - 0.3,
			High:        high,
			Low:         closePrice - 0.8,
			Close:       closePrice,
			Volume:      1000 + float64(i),
			QuoteVolume: (1000 + float64(i)) * closePrice,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureClearTrend(start float64, n int) []BollPumpBar {
	bars := make([]BollPumpBar, 0, n)
	price := start
	for i := 0; i < n; i++ {
		price += 0.45 + float64(i)*0.03
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(i) * 15 * 60 * 1000,
			CloseTimeMs: int64(i+1)*15*60*1000 - 1,
			Open:        price - 0.25,
			High:        price + 0.18,
			Low:         price - 0.34,
			Close:       price,
			Volume:      100 + float64(i),
			QuoteVolume: (100 + float64(i)) * price,
			Closed:      true,
		})
	}
	return bars
}

func bollPumpFixtureChoppyTrend(start float64, n int) []BollPumpBar {
	bars := make([]BollPumpBar, 0, n)
	moves := []float64{0.2, -0.25, 0.15, -0.2, 0.22, -0.18, 0.12, -0.1}
	price := start
	for i := 0; i < n; i++ {
		price += moves[i%len(moves)]
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(i) * 15 * 60 * 1000,
			CloseTimeMs: int64(i+1)*15*60*1000 - 1,
			Open:        price + 0.01,
			High:        price + 0.9,
			Low:         price - 0.8,
			Close:       price,
			Volume:      100 + float64(i),
			QuoteVolume: (100 + float64(i)) * price,
			Closed:      true,
		})
	}
	return bars
}
