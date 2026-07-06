package sqlite

import (
	"context"
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sqlx.DB
	writeCh chan writeOp
	done    chan struct{}
}

type writeOp struct {
	ctx    context.Context
	fn     func(ctx context.Context, tx *sqlx.Tx) error
	result chan error
}

func Open(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("sqlite: DSN is empty")
	}
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: synchronous: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA wal_autocheckpoint=1000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: wal_autocheckpoint: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_size_limit=134217728"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: journal_size_limit: %w", err)
	}

	s := &Store{
		DB:      db,
		writeCh: make(chan writeOp, 256),
		done:    make(chan struct{}),
	}
	go s.writeLoop()
	return s, nil
}

func (s *Store) writeLoop() {
	defer close(s.done)
	for op := range s.writeCh {
		tx, err := s.DB.Beginx()
		if err != nil {
			op.result <- fmt.Errorf("sqlite: begin tx: %w", err)
			continue
		}
		if err := op.fn(op.ctx, tx); err != nil {
			tx.Rollback()
			op.result <- err
			continue
		}
		op.result <- tx.Commit()
	}
}

func (s *Store) Write(ctx context.Context, fn func(ctx context.Context, tx *sqlx.Tx) error) error {
	op := writeOp{ctx: ctx, fn: fn, result: make(chan error, 1)}
	select {
	case s.writeCh <- op:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-op.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) Close() error {
	close(s.writeCh)
	<-s.done
	return s.DB.Close()
}

func (s *Store) QueryContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return s.DB.SelectContext(ctx, dest, query, args...)
}

func (s *Store) SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return s.DB.SelectContext(ctx, dest, query, args...)
}

func (s *Store) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return s.DB.GetContext(ctx, dest, query, args...)
}

func MustOpen(dsn string) *Store {
	s, err := Open(dsn)
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	return s
}
