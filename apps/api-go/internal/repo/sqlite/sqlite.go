package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB       *sqlx.DB
	dsn      string
	dbMu     sync.RWMutex
	close    sync.Once
	closed   bool
	closeErr error
	writeCh  chan writeOp
	done     chan struct{}
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
	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}

	s := &Store{
		DB:      db,
		dsn:     dsn,
		writeCh: make(chan writeOp, 256),
		done:    make(chan struct{}),
	}
	go s.writeLoop()
	return s, nil
}

func openDB(dsn string) (*sqlx.DB, error) {
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
	return db, nil
}

func (s *Store) writeLoop() {
	defer close(s.done)
	for op := range s.writeCh {
		db, err := s.currentDB()
		if err != nil {
			op.result <- err
			continue
		}
		tx, err := db.Beginx()
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
	if err := s.checkOpen(); err != nil {
		return err
	}
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
	s.close.Do(func() {
		close(s.writeCh)
		<-s.done

		s.dbMu.Lock()
		s.closed = true
		db := s.DB
		s.DB = nil
		s.dbMu.Unlock()

		if db != nil {
			s.closeErr = db.Close()
		}
	})
	return s.closeErr
}

func (s *Store) Reopen(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("sqlite: store is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newDB, err := openDB(s.dsn)
	if err != nil {
		return err
	}

	s.dbMu.Lock()
	if s.closed {
		s.dbMu.Unlock()
		newDB.Close()
		return fmt.Errorf("sqlite: store is closed")
	}
	oldDB := s.DB
	s.DB = newDB
	s.dbMu.Unlock()

	if oldDB != nil {
		go func() {
			_ = oldDB.Close()
		}()
	}
	return nil
}

func (s *Store) CloseIdleConnections() {
	db, err := s.currentDB()
	if err != nil {
		return
	}
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(1)
}

func (s *Store) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	db, err := s.currentDB()
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, query, args...)
}

func (s *Store) QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	db, err := s.currentDB()
	if err != nil {
		return nil, err
	}
	return db.QueryxContext(ctx, query, args...)
}

func (s *Store) QueryContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return s.SelectContext(ctx, dest, query, args...)
}

func (s *Store) SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	db, err := s.currentDB()
	if err != nil {
		return err
	}
	return db.SelectContext(ctx, dest, query, args...)
}

func (s *Store) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	db, err := s.currentDB()
	if err != nil {
		return err
	}
	return db.GetContext(ctx, dest, query, args...)
}

func (s *Store) currentDB() (*sqlx.DB, error) {
	if s == nil {
		return nil, fmt.Errorf("sqlite: store is nil")
	}
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.closed || s.DB == nil {
		return nil, fmt.Errorf("sqlite: store is closed")
	}
	return s.DB, nil
}

func (s *Store) checkOpen() error {
	_, err := s.currentDB()
	return err
}

func MustOpen(dsn string) *Store {
	s, err := Open(dsn)
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	return s
}
