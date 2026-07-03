package service

import (
	"context"
	"path/filepath"
	"testing"

	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestTGNotifyPrefsRoundTripIncludesBollPumpAndAbsorption(t *testing.T) {
	ctx := context.Background()
	store := openTGNotifyPrefsStore(t)
	defer store.Close()

	prefs, err := LoadTGNotifyPrefs(ctx, store, 12345)
	if err != nil {
		t.Fatalf("load default prefs: %v", err)
	}
	if !prefs.MarketAnomalyEnabled {
		t.Fatalf("default market anomaly enabled = false, want true")
	}
	if prefs.WhaleWallEnabled {
		t.Fatalf("default whale wall enabled = true, want false")
	}
	if prefs.AbsorptionEnabled {
		t.Fatalf("default absorption enabled = true, want false")
	}
	if !prefs.BollPumpEnabled {
		t.Fatalf("default boll pump enabled = false, want true")
	}

	prefs.MarketAnomalyEnabled = false
	prefs.WhaleWallEnabled = true
	prefs.AbsorptionEnabled = true
	prefs.BollPumpEnabled = false
	prefs.MuteAll = true
	if err := SaveTGNotifyPrefs(ctx, store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	got, err := LoadTGNotifyPrefs(ctx, store, 12345)
	if err != nil {
		t.Fatalf("reload prefs: %v", err)
	}
	if got.MarketAnomalyEnabled {
		t.Fatalf("market anomaly enabled = true, want false")
	}
	if !got.WhaleWallEnabled {
		t.Fatalf("whale wall enabled = false, want true")
	}
	if !got.AbsorptionEnabled {
		t.Fatalf("absorption enabled = false, want true")
	}
	if got.BollPumpEnabled {
		t.Fatalf("boll pump enabled = true, want false")
	}
	if !got.MuteAll {
		t.Fatalf("mute all = false, want true")
	}
}

func TestTGNotifyEventCategorySeparatesAbsorption(t *testing.T) {
	cases := map[string]string{
		"whale_wall_far":            "whale_wall",
		"whale_wall_filled":         "whale_wall",
		"signal_lab_persistent_buy": "absorption",
		"absorption_signal":         "absorption",
		"absorption_signal_long":    "absorption",
		"absorption_signal_short":   "absorption",
		"boll_pump":                 "boll_pump",
		"breakout_up":               "market_anomaly",
		"volume_spike":              "market_anomaly",
		"price_rise_large_5m":       "market_anomaly",
		"volume_rise_large_15m":     "market_anomaly",
		"new_high_1d":               "market_anomaly",
		"signal_lab_climax_long":    "market_anomaly",
	}

	for eventType, want := range cases {
		if got := TGNotifyEventCategory(eventType); got != want {
			t.Fatalf("TGNotifyEventCategory(%q) = %q, want %q", eventType, got, want)
		}
	}
}

func openTGNotifyPrefsStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migration.Migrate(context.Background(), store); err != nil {
		store.Close()
		t.Fatalf("migrate sqlite: %v", err)
	}
	return store
}
