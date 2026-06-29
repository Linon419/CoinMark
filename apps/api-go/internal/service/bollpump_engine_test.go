package service

import (
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
	if got.Signal.Score < 85 {
		t.Fatalf("score = %.2f, want >= 85", got.Signal.Score)
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

var _ model.BollPumpSignal
