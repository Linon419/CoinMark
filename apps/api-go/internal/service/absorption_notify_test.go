package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSyncAbsorptionSnapshotEventsCreatesTelegramEventsForConfirmedSignals(t *testing.T) {
	ctx := context.Background()
	store := openTGNotifyPrefsStore(t)
	defer store.Close()

	windows, _ := json.Marshal(map[string]map[string]interface{}{
		"4h": {"passed": true},
		"1d": {"passed": true},
		"3d": {"passed": true},
	})
	reasons, _ := json.Marshal([]string{"L1_DELTA4H_ABNORMAL", "L2_PERSISTENCE_RATIO_GT_60"})
	netFlow := 2_500_000.0
	impact := 0.0000012

	inserted, err := syncAbsorptionSnapshotEvents(ctx, store, []absValue{
		{
			Market: "swap", Symbol: "BTCUSDT", Direction: "LONG_BIAS", SignalState: "STRONG",
			Score: 95, BucketStartMs: 1_700_000_000_000, NetFlowStrength: &netFlow, ImpactPerNot: &impact,
			W4h: true, W1d: true, W3d: true, Windows: windows, Reasons: reasons,
		},
		{
			Market: "swap", Symbol: "ETHUSDT", Direction: "LONG_BIAS", SignalState: "WATCH",
			Score: 58, BucketStartMs: 1_700_000_060_000, NetFlowStrength: &netFlow,
			W4h: true, Windows: windows, Reasons: reasons,
		},
	}, 720)
	if err != nil {
		t.Fatalf("sync absorption events: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}

	var rows []struct {
		Symbol    string `db:"symbol"`
		EventType string `db:"event_type"`
		Title     string `db:"title"`
		Details   string `db:"details"`
	}
	if err := store.SelectContext(ctx, &rows, `SELECT symbol, event_type, title, details FROM anomaly_events ORDER BY id`); err != nil {
		t.Fatalf("select anomaly events: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Symbol != "BTCUSDT" || rows[0].EventType != "absorption_signal_long" {
		t.Fatalf("event = %s/%s, want BTCUSDT/absorption_signal_long", rows[0].Symbol, rows[0].EventType)
	}
	if rows[0].Title != "BTCUSDT 吸筹扫描看多 STRONG (95)" {
		t.Fatalf("title = %q", rows[0].Title)
	}

	var details map[string]interface{}
	if err := json.Unmarshal([]byte(rows[0].Details), &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if details["direction"] != "LONG_BIAS" || details["signalState"] != "STRONG" {
		t.Fatalf("details = %#v", details)
	}
	if details["window4hPassed"] != true || details["window1dPassed"] != true || details["window3dPassed"] != true {
		t.Fatalf("window flags = %#v", details)
	}
}

func TestSyncAbsorptionSnapshotEventsUsesCooldown(t *testing.T) {
	ctx := context.Background()
	store := openTGNotifyPrefsStore(t)
	defer store.Close()

	netFlow := 1_000_000.0
	values := []absValue{
		{
			Market: "swap", Symbol: "BTCUSDT", Direction: "LONG_BIAS", SignalState: "CONFIRM",
			Score: 72, BucketStartMs: 1_700_000_000_000, NetFlowStrength: &netFlow,
		},
	}
	if inserted, err := syncAbsorptionSnapshotEvents(ctx, store, values, 720); err != nil || inserted != 1 {
		t.Fatalf("first sync inserted=%d err=%v, want 1 nil", inserted, err)
	}
	values[0].BucketStartMs += 60_000
	if inserted, err := syncAbsorptionSnapshotEvents(ctx, store, values, 720); err != nil || inserted != 0 {
		t.Fatalf("second sync inserted=%d err=%v, want 0 nil", inserted, err)
	}
}
