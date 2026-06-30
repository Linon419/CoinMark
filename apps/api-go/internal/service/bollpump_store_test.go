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

func TestBollPumpListsFilterNonUSDTPairs(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	usdt := model.BollPumpSignal{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "15m",
		SignalLevel:   "WATCH",
		Price:         1,
		Score:         75,
		PriorityScore: 75,
		SignalTimeMs:  time.Now().UnixMilli(),
		CandleStartMs: 60000,
		Reason:        "volume-backed pump",
		Details:       model.JSONB(`{}`),
	}
	usdc := usdt
	usdc.Symbol = "ETHUSDC"
	usdc.CandleStartMs = 120000
	if _, err := SaveBollPumpSignal(ctx, store, usdt, false); err != nil {
		t.Fatalf("save usdt signal: %v", err)
	}
	if _, err := SaveBollPumpSignal(ctx, store, usdc, false); err != nil {
		t.Fatalf("save usdc signal: %v", err)
	}

	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "15m",
		Status:        string(BollPumpStatusWatch),
		CurrentScore:  75,
		PriorityScore: 75,
	}); err != nil {
		t.Fatalf("save usdt state: %v", err)
	}
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:        "swap",
		Symbol:        "ETHUSDC",
		Timeframe:     "3m",
		Status:        string(BollPumpStatusCompleted),
		CurrentScore:  120,
		PriorityScore: 120,
	}); err != nil {
		t.Fatalf("save usdc state: %v", err)
	}

	signals, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Limit: 10})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(signals) != 1 || signals[0].Symbol != "XYZUSDT" {
		t.Fatalf("signals = %#v, want only XYZUSDT", signals)
	}

	states, err := ListBollPumpStates(ctx, store, BollPumpStateFilter{Market: "swap", Limit: 10})
	if err != nil {
		t.Fatalf("list states: %v", err)
	}
	if len(states) != 1 || states[0].Symbol != "XYZUSDT" {
		t.Fatalf("states = %#v, want only XYZUSDT", states)
	}
}

func TestBollPumpStatsFilterNonUSDTPairs(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()

	now := time.Now().UnixMilli()
	for _, symbol := range []string{"XYZUSDT", "ETHUSDC"} {
		if _, err := SaveBollPumpSignal(ctx, store, model.BollPumpSignal{
			Market:        "swap",
			Symbol:        symbol,
			Timeframe:     "15m",
			SignalLevel:   "WATCH",
			Price:         1,
			Score:         75,
			PriorityScore: 75,
			SignalTimeMs:  now,
			CandleStartMs: now,
			Reason:        "volume-backed pump",
			Details:       model.JSONB(`{}`),
		}, false); err != nil {
			t.Fatalf("save signal %s: %v", symbol, err)
		}
	}

	stats, err := BollPumpStats(ctx, store, "swap", now-1)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	levels := stats["countsByLevel"].(map[string]int64)
	timeframes := stats["countsByTimeframe"].(map[string]int64)
	if levels["WATCH"] != 1 {
		t.Fatalf("WATCH count = %d, want 1", levels["WATCH"])
	}
	if timeframes["15m"] != 1 {
		t.Fatalf("15m count = %d, want 1", timeframes["15m"])
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
