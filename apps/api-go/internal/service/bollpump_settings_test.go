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
	cfg.MinimumTrendTimeframe = "30m"
	cfg.MinimumTrendCheckCandles = 12
	cfg.MinimumTrendGainPct = 0.02
	cfg.MinimumTrendEfficiencyMin = 0.55
	cfg.MinimumTrendRisingRatio = 0.7
	cfg.ResistanceLookback = 90
	cfg.ResistanceSwingSpan = 3
	cfg.ResistanceClusterATR = 0.6
	cfg.ResistanceClusterPct = 0.009
	cfg.ResistanceBreakoutBufferPct = 0.005
	cfg.ResistanceMaxDistancePct = 0.06
	cfg.ResistanceMinTouches = 4
	cfg.ResistanceBreakoutScore = 12
	cfg.Resistance4HLookback = 72
	cfg.Resistance4HSwingSpan = 3
	cfg.Resistance4HClusterATR = 0.7
	cfg.Resistance4HClusterPct = 0.01
	cfg.Resistance4HBreakoutBufferPct = 0.004
	cfg.Resistance4HMaxDistancePct = 0.05
	cfg.Resistance4HMinTouches = 3
	cfg.Resistance4HBreakoutBonus = 18
	cfg.KeyK4HEnabled = true
	cfg.KeyK4HLookback = 144
	cfg.KeyK4HThreshold = 0.75
	cfg.KeyK4HMinVolumeRatio = 1.1
	cfg.KeyK4HMinBodyPct = 0.002
	cfg.KeyK4HMaxStickyScore = 0.9
	cfg.KeyK4HTelegramThreshold = 82

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
	if got.MinimumTrendTimeframe != "30m" {
		t.Fatalf("minimum trend timeframe = %s, want 30m", got.MinimumTrendTimeframe)
	}
	if got.MinimumTrendCheckCandles != 12 {
		t.Fatalf("minimum trend candles = %d, want 12", got.MinimumTrendCheckCandles)
	}
	if got.MinimumTrendGainPct != 0.02 {
		t.Fatalf("minimum trend gain = %v, want 0.02", got.MinimumTrendGainPct)
	}
	if got.MinimumTrendEfficiencyMin != 0.55 {
		t.Fatalf("minimum trend efficiency = %v, want 0.55", got.MinimumTrendEfficiencyMin)
	}
	if got.MinimumTrendRisingRatio != 0.7 {
		t.Fatalf("minimum trend rising ratio = %v, want 0.7", got.MinimumTrendRisingRatio)
	}
	if got.ResistanceLookback != 90 {
		t.Fatalf("resistance lookback = %d, want 90", got.ResistanceLookback)
	}
	if got.ResistanceSwingSpan != 3 {
		t.Fatalf("resistance swing span = %d, want 3", got.ResistanceSwingSpan)
	}
	if got.ResistanceClusterATR != 0.6 {
		t.Fatalf("resistance cluster atr = %v, want 0.6", got.ResistanceClusterATR)
	}
	if got.ResistanceClusterPct != 0.009 {
		t.Fatalf("resistance cluster pct = %v, want 0.009", got.ResistanceClusterPct)
	}
	if got.ResistanceBreakoutBufferPct != 0.005 {
		t.Fatalf("resistance breakout buffer = %v, want 0.005", got.ResistanceBreakoutBufferPct)
	}
	if got.ResistanceMaxDistancePct != 0.06 {
		t.Fatalf("resistance max distance = %v, want 0.06", got.ResistanceMaxDistancePct)
	}
	if got.ResistanceMinTouches != 4 {
		t.Fatalf("resistance min touches = %d, want 4", got.ResistanceMinTouches)
	}
	if got.ResistanceBreakoutScore != 12 {
		t.Fatalf("resistance breakout score = %v, want 12", got.ResistanceBreakoutScore)
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
	if !got.KeyK4HEnabled {
		t.Fatalf("key K 4h enabled = false, want true")
	}
	if got.KeyK4HLookback != 144 {
		t.Fatalf("key K 4h lookback = %d, want 144", got.KeyK4HLookback)
	}
	if got.KeyK4HThreshold != 0.75 {
		t.Fatalf("key K 4h threshold = %v, want 0.75", got.KeyK4HThreshold)
	}
	if got.KeyK4HMinVolumeRatio != 1.1 {
		t.Fatalf("key K 4h min volume = %v, want 1.1", got.KeyK4HMinVolumeRatio)
	}
	if got.KeyK4HMinBodyPct != 0.002 {
		t.Fatalf("key K 4h min body = %v, want 0.002", got.KeyK4HMinBodyPct)
	}
	if got.KeyK4HMaxStickyScore != 0.9 {
		t.Fatalf("key K 4h max sticky = %v, want 0.9", got.KeyK4HMaxStickyScore)
	}
	if got.KeyK4HTelegramThreshold != 82 {
		t.Fatalf("key K 4h telegram threshold = %v, want 82", got.KeyK4HTelegramThreshold)
	}
}
