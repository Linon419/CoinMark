package migration

import (
	"context"
	"path/filepath"
	"testing"

	"coinmark/api-go/internal/repo/sqlite"
)

func TestMigrateAddsNotifyCategoryColumnsToExistingTGNotifyPrefs(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	_, err = store.DB.Exec(`CREATE TABLE tg_notify_prefs (
		chat_id BIGINT PRIMARY KEY,
		market_anomaly_enabled BOOLEAN NOT NULL DEFAULT 1,
		whale_wall_enabled BOOLEAN NOT NULL DEFAULT 0,
		signal_lab_enabled BOOLEAN NOT NULL DEFAULT 0,
		mute_all BOOLEAN NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create old prefs table: %v", err)
	}

	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate old schema twice: %v", err)
	}

	for _, column := range []string{"absorption_enabled", "boll_pump_enabled"} {
		var count int
		if err := store.GetContext(ctx, &count, `SELECT COUNT(*) FROM pragma_table_info('tg_notify_prefs') WHERE name = ?`, column); err != nil {
			t.Fatalf("query table info %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("%s column count = %d, want 1", column, count)
		}
	}
}

func TestMigrateCreatesBollPumpTables(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	if err := Migrate(ctx, store); err != nil {
		t.Fatalf("migrate sqlite twice: %v", err)
	}

	for _, table := range []string{"boll_pump_states", "boll_pump_signals", "boll_pump_settings"} {
		var count int
		if err := store.GetContext(ctx, &count, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
}
