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
}
