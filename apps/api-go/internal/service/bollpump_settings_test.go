package service

import (
	"context"
	"reflect"
	"testing"
)

func TestBollPumpSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	cfg := DefaultBollPumpConfig()
	cfg.Enabled = false
	cfg.SymbolLimit = 321
	cfg.Timeframes = []string{"15m", "bad", "1m", "15m"}
	cfg.GainThresholds["15m"] = 0.09
	cfg.VolumeThresholds["1m"] = 8
	cfg.TrendCleanBonus = 7
	cfg.TrendWickPenalty = -35
	cfg.TrendWeakPenalty = -12
	cfg.TrendWickBodyMaxRatio = 0.22
	cfg.TrendEfficiencyMin = 0.41
	cfg.Resistance4HLookback = 72
	cfg.Resistance4HSwingSpan = 3
	cfg.Resistance4HClusterATR = 0.7
	cfg.Resistance4HClusterPct = 0.01
	cfg.Resistance4HBreakoutBufferPct = 0.004
	cfg.Resistance4HMaxDistancePct = 0.05
	cfg.Resistance4HMinTouches = 3
	cfg.Resistance4HBreakoutBonus = 18

	saved, err := SaveBollPumpConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if saved.Enabled {
		t.Fatalf("enabled = true, want false")
	}
	if !reflect.DeepEqual(saved.Timeframes, []string{"15m", "1m"}) {
		t.Fatalf("timeframes = %#v", saved.Timeframes)
	}

	got, err := LoadBollPumpConfig(ctx, store, DefaultBollPumpConfig())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if got.SymbolLimit != 321 {
		t.Fatalf("symbol limit = %d, want 321", got.SymbolLimit)
	}
	if got.GainThresholds["15m"] != 0.09 {
		t.Fatalf("15m gain = %v, want 0.09", got.GainThresholds["15m"])
	}
	if got.VolumeThresholds["1m"] != 8 {
		t.Fatalf("1m volume = %v, want 8", got.VolumeThresholds["1m"])
	}
	if got.TrendCleanBonus != 7 {
		t.Fatalf("trend clean bonus = %v, want 7", got.TrendCleanBonus)
	}
	if got.TrendWickPenalty != -35 {
		t.Fatalf("trend wick penalty = %v, want -35", got.TrendWickPenalty)
	}
	if got.TrendWeakPenalty != -12 {
		t.Fatalf("trend weak penalty = %v, want -12", got.TrendWeakPenalty)
	}
	if got.TrendWickBodyMaxRatio != 0.22 {
		t.Fatalf("trend wick body max ratio = %v, want 0.22", got.TrendWickBodyMaxRatio)
	}
	if got.TrendEfficiencyMin != 0.41 {
		t.Fatalf("trend efficiency min = %v, want 0.41", got.TrendEfficiencyMin)
	}
	if got.Resistance4HLookback != 72 {
		t.Fatalf("4h lookback = %d, want 72", got.Resistance4HLookback)
	}
	if got.Resistance4HSwingSpan != 3 {
		t.Fatalf("4h swing span = %d, want 3", got.Resistance4HSwingSpan)
	}
	if got.Resistance4HClusterATR != 0.7 {
		t.Fatalf("4h cluster atr = %v, want 0.7", got.Resistance4HClusterATR)
	}
	if got.Resistance4HClusterPct != 0.01 {
		t.Fatalf("4h cluster pct = %v, want 0.01", got.Resistance4HClusterPct)
	}
	if got.Resistance4HBreakoutBufferPct != 0.004 {
		t.Fatalf("4h breakout buffer = %v, want 0.004", got.Resistance4HBreakoutBufferPct)
	}
	if got.Resistance4HMaxDistancePct != 0.05 {
		t.Fatalf("4h max distance = %v, want 0.05", got.Resistance4HMaxDistancePct)
	}
	if got.Resistance4HMinTouches != 3 {
		t.Fatalf("4h min touches = %d, want 3", got.Resistance4HMinTouches)
	}
	if got.Resistance4HBreakoutBonus != 18 {
		t.Fatalf("4h breakout bonus = %v, want 18", got.Resistance4HBreakoutBonus)
	}
}
