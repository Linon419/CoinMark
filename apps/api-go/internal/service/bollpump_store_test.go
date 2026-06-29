package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestSaveAndListBollPumpSignal(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	sig := model.BollPumpSignal{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "15m",
		SignalLevel:   "WATCH",
		Price:         0.1234,
		VolumeRatio:   2.7,
		BollBandwidth: 0.034,
		Score:         75,
		PriorityScore: 75,
		SignalTimeMs:  time.Now().UnixMilli(),
		CandleStartMs: 60000,
		Reason:        "volume-backed pump",
		Details:       model.JSONB(`{"score":75}`),
	}
	if _, err := SaveBollPumpSignal(ctx, store, sig, false); err != nil {
		t.Fatalf("save signal: %v", err)
	}

	rows, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Limit: 10})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(rows) != 1 || rows[0].Symbol != "XYZUSDT" || rows[0].SignalLevel != "WATCH" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func openBollPumpTestStore(t *testing.T) *sqlite.Store {
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
