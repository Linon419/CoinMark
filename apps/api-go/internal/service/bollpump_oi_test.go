package service

import (
	"encoding/json"
	"strings"
	"testing"

	"coinmark/api-go/internal/model"
)

func TestBollPumpOIGrowthScoreFromHist(t *testing.T) {
	cfg := DefaultBollPumpConfig()
	cfg.OIGrowthPeriod = "15m"
	cfg.OIGrowthPeriods = 4
	cfg.OIGrowthMinPct = 0.05
	cfg.OIGrowthFullPct = 0.30
	cfg.OIGrowthMaxBonus = 12

	bucketMs := int64(15 * 60 * 1000)
	nowMs := int64(100) * bucketMs
	hist := []map[string]interface{}{
		{"timestamp": float64(95 * bucketMs), "sumOpenInterestValue": "100"},
		{"timestamp": float64(96 * bucketMs), "sumOpenInterestValue": "102"},
		{"timestamp": float64(97 * bucketMs), "sumOpenInterestValue": "110"},
		{"timestamp": float64(98 * bucketMs), "sumOpenInterestValue": "120"},
		{"timestamp": float64(99 * bucketMs), "sumOpenInterestValue": "130"},
		{"timestamp": float64(100 * bucketMs), "sumOpenInterestValue": "500"},
	}

	got := bollPumpOIGrowthScoreFromHist(hist, cfg, nowMs)
	if !got.Available {
		t.Fatalf("available = false, want true")
	}
	if got.GrowthPct < 0.299 || got.GrowthPct > 0.301 {
		t.Fatalf("growth = %.6f, want 0.30", got.GrowthPct)
	}
	if got.Bonus != 12 {
		t.Fatalf("bonus = %.2f, want 12", got.Bonus)
	}
}

func TestBollPumpApplyOIGrowthScoreAddsReasonAndDetails(t *testing.T) {
	sig := model.BollPumpSignal{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "15m",
		SignalLevel:   string(BollPumpLevelWatch),
		Score:         80,
		PriorityScore: 80,
		Reason:        "volume-backed pump",
		Details:       model.JSONB(`{"existing":true}`),
	}
	score := bollPumpOIGrowthScore{
		Available: true,
		GrowthPct: 0.2,
		Bonus:     8,
		Period:    "15m",
		Periods:   4,
		PrevValue: 100,
		CurrValue: 120,
		Reason:    "OI growth 20.00% over 4x15m bonus 8",
	}

	got := bollPumpApplyOIGrowthScore(sig, score)
	if got.Score != 88 || got.PriorityScore != 88 {
		t.Fatalf("score = %.2f priority = %.2f, want 88/88", got.Score, got.PriorityScore)
	}
	if !strings.Contains(got.Reason, "OI growth 20.00%") {
		t.Fatalf("reason = %q, want OI growth reason", got.Reason)
	}
	var details map[string]interface{}
	if err := json.Unmarshal(got.Details, &details); err != nil {
		t.Fatalf("details json: %v", err)
	}
	if details["existing"] != true {
		t.Fatalf("existing detail lost: %#v", details)
	}
	if details["oi_growth_bonus"].(float64) != 8 {
		t.Fatalf("oi_growth_bonus = %#v, want 8", details["oi_growth_bonus"])
	}
}
