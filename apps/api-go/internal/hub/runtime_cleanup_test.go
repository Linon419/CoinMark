package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestCleanupDeletesExpiredSQLiteHistoryBuckets(t *testing.T) {
	ctx := context.Background()
	store := openMigratedStore(t)
	defer store.Close()

	nowMs := time.Now().UnixMilli()
	oldTrade := nowMs - 8*24*60*60*1000
	newTrade := nowMs - 6*24*60*60*1000
	oldOrderbook := nowMs - 4*24*60*60*1000
	newOrderbook := nowMs - 2*24*60*60*1000

	insertTradeBucket(t, store, oldTrade)
	insertTradeBucket(t, store, newTrade)
	insertOrderbookBucket(t, store, oldOrderbook)
	insertOrderbookBucket(t, store, newOrderbook)

	rt := &Runtime{
		cfg: &config.Config{
			TradeBucketRetentionDays:     7,
			OrderbookBucketRetentionDays: 3,
			SQLiteVacuumIntervalSec:      0,
		},
		store: store,
	}

	rt.doCleanup(ctx)

	assertCount(t, store, "trade_buckets", 1)
	assertCount(t, store, "orderbook_feature_buckets", 1)
	assertBucketExists(t, store, "trade_buckets", newTrade)
	assertBucketExists(t, store, "orderbook_feature_buckets", newOrderbook)
}

func TestMaintainSQLiteWALTriggersSelfHealAfterPinnedThreshold(t *testing.T) {
	ctx := context.Background()
	recoveries := 0
	checkpoints := 0
	rt := &Runtime{
		cfg: &config.Config{
			SQLiteWALSelfHealEnabled:  true,
			SQLiteWALPinnedMaxChecks:  2,
			SQLiteWALSelfHealMinBytes: 1,
		},
		sqliteCheckpointFn: func(context.Context, string) (sqliteCheckpointResult, bool) {
			checkpoints++
			return sqliteCheckpointResult{Log: 40000, Checkpointed: 1, WALBytes: 2}, true
		},
		sqliteWALRecoverFn: func(context.Context, sqliteCheckpointResult) bool {
			recoveries++
			return true
		},
	}

	rt.maintainSQLiteWAL(ctx)
	if recoveries != 0 {
		t.Fatalf("recoveries after first pinned check = %d, want 0", recoveries)
	}

	rt.maintainSQLiteWAL(ctx)
	if recoveries != 1 {
		t.Fatalf("recoveries after second pinned check = %d, want 1", recoveries)
	}
	if checkpoints != 2 {
		t.Fatalf("checkpoint calls = %d, want 2", checkpoints)
	}
	if rt.sqliteWALPinnedChecks != 0 {
		t.Fatalf("pinned checks after successful recovery = %d, want 0", rt.sqliteWALPinnedChecks)
	}
}

func openMigratedStore(t *testing.T) *sqlite.Store {
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

func insertTradeBucket(t *testing.T, store *sqlite.Store, bucketStartMs int64) {
	t.Helper()
	err := store.Write(context.Background(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO trade_buckets
			(market, symbol, bucket, bucket_start_ms, taker_buy_notional, taker_sell_notional, quote_notional, trade_count)
			VALUES ('swap', 'BTCUSDT', '1m', ?, 1, 1, 2, 1)`, bucketStartMs)
		return err
	})
	if err != nil {
		t.Fatalf("insert trade bucket: %v", err)
	}
}

func insertOrderbookBucket(t *testing.T, store *sqlite.Store, bucketStartMs int64) {
	t.Helper()
	err := store.Write(context.Background(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO orderbook_feature_buckets
			(market, symbol, bucket, bucket_start_ms, sample_count)
			VALUES ('swap', 'BTCUSDT', '1m', ?, 1)`, bucketStartMs)
		return err
	})
	if err != nil {
		t.Fatalf("insert orderbook bucket: %v", err)
	}
}

func assertCount(t *testing.T, store *sqlite.Store, table string, want int) {
	t.Helper()
	var got int
	if err := store.GetContext(context.Background(), &got, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertBucketExists(t *testing.T, store *sqlite.Store, table string, bucketStartMs int64) {
	t.Helper()
	var got int
	if err := store.GetContext(context.Background(), &got, "SELECT COUNT(*) FROM "+table+" WHERE bucket_start_ms = ?", bucketStartMs); err != nil {
		t.Fatalf("count %s bucket: %v", table, err)
	}
	if got != 1 {
		t.Fatalf("%s bucket %d count = %d, want 1", table, bucketStartMs, got)
	}
}
