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
}
