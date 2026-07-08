package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReopenKeepsStoreUsable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	if _, err := store.ExecContext(ctx, `CREATE TABLE sample (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := store.ExecContext(ctx, `INSERT INTO sample (name) VALUES (?)`, "before"); err != nil {
		t.Fatalf("insert before reopen: %v", err)
	}

	if err := store.Reopen(ctx); err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	if _, err := store.ExecContext(ctx, `INSERT INTO sample (name) VALUES (?)`, "after"); err != nil {
		t.Fatalf("insert after reopen: %v", err)
	}

	var count int
	if err := store.GetContext(ctx, &count, `SELECT COUNT(*) FROM sample`); err != nil {
		t.Fatalf("count sample: %v", err)
	}
	if count != 2 {
		t.Fatalf("sample count = %d, want 2", count)
	}
}
