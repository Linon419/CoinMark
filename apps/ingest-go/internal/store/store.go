package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"coinmark/ingest-go/internal/ingest"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
	_ "modernc.org/sqlite"
)

type Store struct {
	sqlite *sql.DB
	ch     clickhouse.Conn
}

func New(ctx context.Context, databaseURL string, clickhouseURL string) (*Store, error) {
	sqliteDSN, err := sqliteDSNFromURL(databaseURL)
	if err != nil {
		return nil, err
	}

	sqliteDB, err := sql.Open("sqlite", sqliteDSN)
	if err != nil {
		return nil, err
	}
	sqliteDB.SetMaxOpenConns(1)
	sqliteDB.SetMaxIdleConns(1)
	sqliteDB.SetConnMaxLifetime(0)

	ctxPing, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqliteDB.PingContext(ctxPing); err != nil {
		_ = sqliteDB.Close()
		return nil, err
	}
	if err := initSQLiteSchema(ctx, sqliteDB); err != nil {
		_ = sqliteDB.Close()
		return nil, err
	}

	chConn, err := initClickHouse(ctx, strings.TrimSpace(clickhouseURL))
	if err != nil {
		_ = sqliteDB.Close()
		return nil, err
	}

	return &Store{sqlite: sqliteDB, ch: chConn}, nil
}

func (s *Store) Close() {
	if s.sqlite != nil {
		_ = s.sqlite.Close()
	}
	if s.ch != nil {
		_ = s.ch.Close()
	}
}

func sqliteDSNFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("DATABASE_URL is empty")
	}

	path := ""
	switch {
	case strings.HasPrefix(raw, "sqlite:///"):
		path = strings.TrimPrefix(raw, "sqlite:///")
	case strings.HasPrefix(raw, "file:"):
		return raw, nil
	default:
		return "", fmt.Errorf("only sqlite DATABASE_URL is supported, got: %s", raw)
	}

	if path == "" {
		return "", fmt.Errorf("invalid sqlite path in DATABASE_URL")
	}
	if !strings.HasPrefix(path, "/") && !(len(path) > 1 && path[1] == ':') {
		path = "/" + path
	}
	if strings.HasPrefix(path, "/") && len(path) > 2 && path[2] == ':' {
		path = path[1:]
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path), nil
}

func initSQLiteSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trade_buckets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			market TEXT NOT NULL,
			symbol TEXT NOT NULL,
			bucket TEXT NOT NULL,
			bucket_start_ms INTEGER NOT NULL,
			taker_buy_notional REAL NOT NULL DEFAULT 0,
			taker_sell_notional REAL NOT NULL DEFAULT 0,
			quote_notional REAL NOT NULL DEFAULT 0,
			trade_count INTEGER NOT NULL DEFAULT 0,
			first_trade_ms INTEGER,
			last_trade_ms INTEGER,
			open_price REAL,
			close_price REAL,
			high_price REAL,
			low_price REAL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (market, symbol, bucket, bucket_start_ms)
		);`,
		`CREATE INDEX IF NOT EXISTS ix_trade_bucket_query ON trade_buckets (market, bucket, bucket_start_ms);`,
		`CREATE INDEX IF NOT EXISTS ix_trade_bucket_symbol ON trade_buckets (market, symbol, bucket, bucket_start_ms);`,
		`CREATE TABLE IF NOT EXISTS orderbook_feature_buckets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			market TEXT NOT NULL,
			symbol TEXT NOT NULL,
			bucket TEXT NOT NULL,
			bucket_start_ms INTEGER NOT NULL,
			spread_bps_sum REAL NOT NULL DEFAULT 0,
			depth_imbalance_l5_sum REAL NOT NULL DEFAULT 0,
			microprice_shift_bps_sum REAL NOT NULL DEFAULT 0,
			wall_pressure_l5_sum REAL NOT NULL DEFAULT 0,
			sample_count INTEGER NOT NULL DEFAULT 0,
			taker_buy_notional REAL NOT NULL DEFAULT 0,
			taker_sell_notional REAL NOT NULL DEFAULT 0,
			depletion_events INTEGER NOT NULL DEFAULT 0,
			replenishment_events INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (market, symbol, bucket, bucket_start_ms)
		);`,
		`CREATE INDEX IF NOT EXISTS ix_orderbook_feature_symbol ON orderbook_feature_buckets (market, symbol, bucket, bucket_start_ms);`,
		`CREATE INDEX IF NOT EXISTS ix_orderbook_feature_query ON orderbook_feature_buckets (market, bucket, bucket_start_ms);`,
		`CREATE TABLE IF NOT EXISTS asset_market_caps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			asset TEXT NOT NULL UNIQUE,
			price_usd REAL NOT NULL,
			circulating_supply REAL NOT NULL,
			market_cap_usd REAL NOT NULL,
			source TEXT NOT NULL,
			event_time_ms INTEGER NOT NULL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS funding_rate_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL UNIQUE,
			last_funding_rate REAL NOT NULL,
			mark_price REAL NOT NULL,
			event_time_ms INTEGER NOT NULL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS open_interest_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL UNIQUE,
			open_interest REAL NOT NULL,
			mark_price REAL NOT NULL,
			oi_notional_usd REAL NOT NULL,
			event_time_ms INTEGER NOT NULL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func dec(v decimal.Decimal) float64 {
	f, _ := v.Float64()
	return f
}

func decPtr(v *decimal.Decimal) any {
	if v == nil {
		return nil
	}
	f, _ := v.Float64()
	return f
}

func intPtr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func chunk[T any](in []T, size int) [][]T {
	if len(in) == 0 {
		return nil
	}
	if size <= 0 {
		return [][]T{in}
	}
	out := make([][]T, 0, (len(in)+size-1)/size)
	for i := 0; i < len(in); i += size {
		j := i + size
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

func (s *Store) UpsertTradeBuckets(ctx context.Context, rows []ingest.TradeDrainItem, batchSize int) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
INSERT INTO trade_buckets (
	market, symbol, bucket, bucket_start_ms,
	taker_buy_notional, taker_sell_notional, quote_notional, trade_count,
	first_trade_ms, last_trade_ms, open_price, close_price, high_price, low_price
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(market, symbol, bucket, bucket_start_ms)
DO UPDATE SET
	taker_buy_notional = trade_buckets.taker_buy_notional + excluded.taker_buy_notional,
	taker_sell_notional = trade_buckets.taker_sell_notional + excluded.taker_sell_notional,
	quote_notional = trade_buckets.quote_notional + excluded.quote_notional,
	trade_count = trade_buckets.trade_count + excluded.trade_count,
	first_trade_ms = CASE
		WHEN trade_buckets.first_trade_ms IS NULL THEN excluded.first_trade_ms
		WHEN excluded.first_trade_ms IS NULL THEN trade_buckets.first_trade_ms
		WHEN excluded.first_trade_ms < trade_buckets.first_trade_ms THEN excluded.first_trade_ms
		ELSE trade_buckets.first_trade_ms
	END,
	last_trade_ms = CASE
		WHEN trade_buckets.last_trade_ms IS NULL THEN excluded.last_trade_ms
		WHEN excluded.last_trade_ms IS NULL THEN trade_buckets.last_trade_ms
		WHEN excluded.last_trade_ms > trade_buckets.last_trade_ms THEN excluded.last_trade_ms
		ELSE trade_buckets.last_trade_ms
	END,
	open_price = CASE
		WHEN trade_buckets.first_trade_ms IS NULL THEN excluded.open_price
		WHEN excluded.first_trade_ms IS NOT NULL AND excluded.first_trade_ms < trade_buckets.first_trade_ms THEN excluded.open_price
		ELSE trade_buckets.open_price
	END,
	close_price = CASE
		WHEN trade_buckets.last_trade_ms IS NULL THEN excluded.close_price
		WHEN excluded.last_trade_ms IS NOT NULL AND excluded.last_trade_ms > trade_buckets.last_trade_ms THEN excluded.close_price
		ELSE trade_buckets.close_price
	END,
	high_price = CASE
		WHEN trade_buckets.high_price IS NULL THEN excluded.high_price
		WHEN excluded.high_price IS NULL THEN trade_buckets.high_price
		WHEN excluded.high_price > trade_buckets.high_price THEN excluded.high_price
		ELSE trade_buckets.high_price
	END,
	low_price = CASE
		WHEN trade_buckets.low_price IS NULL THEN excluded.low_price
		WHEN excluded.low_price IS NULL THEN trade_buckets.low_price
		WHEN excluded.low_price < trade_buckets.low_price THEN excluded.low_price
		ELSE trade_buckets.low_price
	END,
	updated_at = CURRENT_TIMESTAMP
`

	written := 0
	for _, part := range chunk(rows, batchSize) {
		tx, err := s.sqlite.BeginTx(ctx, nil)
		if err != nil {
			return written, err
		}
		for _, r := range part {
			if _, err := tx.ExecContext(
				ctx,
				stmt,
				r.Key.Market,
				r.Key.Symbol,
				r.Key.Bucket,
				r.Key.BucketStartMS,
				dec(r.Delta.TakerBuyNotional),
				dec(r.Delta.TakerSellNotional),
				dec(r.Delta.QuoteNotional),
				r.Delta.TradeCount,
				intPtr(r.Delta.FirstTradeMS),
				intPtr(r.Delta.LastTradeMS),
				decPtr(r.Delta.OpenPrice),
				decPtr(r.Delta.ClosePrice),
				decPtr(r.Delta.HighPrice),
				decPtr(r.Delta.LowPrice),
			); err != nil {
				_ = tx.Rollback()
				return written, err
			}
		}
		if err := tx.Commit(); err != nil {
			return written, err
		}
		written += len(part)

		if s.ch != nil {
			states, err := s.fetchTradeBucketStates(ctx, part)
			if err != nil {
				log.Printf("mirror trade fetch failed: %v", err)
			} else if err := s.insertTradeBucketStatesCH(ctx, states); err != nil {
				log.Printf("mirror trade insert failed: %v", err)
			}
		}
	}
	return written, nil
}

func (s *Store) UpsertOrderbookBuckets(ctx context.Context, rows []ingest.OrderbookDrainItem, batchSize int) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
INSERT INTO orderbook_feature_buckets (
	market, symbol, bucket, bucket_start_ms,
	spread_bps_sum, depth_imbalance_l5_sum, microprice_shift_bps_sum, wall_pressure_l5_sum, sample_count,
	taker_buy_notional, taker_sell_notional, depletion_events, replenishment_events
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(market, symbol, bucket, bucket_start_ms)
DO UPDATE SET
	spread_bps_sum = orderbook_feature_buckets.spread_bps_sum + excluded.spread_bps_sum,
	depth_imbalance_l5_sum = orderbook_feature_buckets.depth_imbalance_l5_sum + excluded.depth_imbalance_l5_sum,
	microprice_shift_bps_sum = orderbook_feature_buckets.microprice_shift_bps_sum + excluded.microprice_shift_bps_sum,
	wall_pressure_l5_sum = orderbook_feature_buckets.wall_pressure_l5_sum + excluded.wall_pressure_l5_sum,
	sample_count = orderbook_feature_buckets.sample_count + excluded.sample_count,
	taker_buy_notional = orderbook_feature_buckets.taker_buy_notional + excluded.taker_buy_notional,
	taker_sell_notional = orderbook_feature_buckets.taker_sell_notional + excluded.taker_sell_notional,
	depletion_events = orderbook_feature_buckets.depletion_events + excluded.depletion_events,
	replenishment_events = orderbook_feature_buckets.replenishment_events + excluded.replenishment_events,
	updated_at = CURRENT_TIMESTAMP
`

	written := 0
	for _, part := range chunk(rows, batchSize) {
		tx, err := s.sqlite.BeginTx(ctx, nil)
		if err != nil {
			return written, err
		}
		for _, r := range part {
			if _, err := tx.ExecContext(
				ctx,
				stmt,
				r.Key.Market,
				r.Key.Symbol,
				r.Key.Bucket,
				r.Key.BucketStartMS,
				dec(r.Delta.SpreadBPSSum),
				dec(r.Delta.DepthImbalanceL5Sum),
				dec(r.Delta.MicropriceShiftBPSSum),
				dec(r.Delta.WallPressureL5Sum),
				r.Delta.SampleCount,
				dec(r.Delta.TakerBuyNotional),
				dec(r.Delta.TakerSellNotional),
				r.Delta.DepletionEvents,
				r.Delta.ReplenishmentEvents,
			); err != nil {
				_ = tx.Rollback()
				return written, err
			}
		}
		if err := tx.Commit(); err != nil {
			return written, err
		}
		written += len(part)

		if s.ch != nil {
			states, err := s.fetchOrderbookBucketStates(ctx, part)
			if err != nil {
				log.Printf("mirror orderbook fetch failed: %v", err)
			} else if err := s.insertOrderbookBucketStatesCH(ctx, states); err != nil {
				log.Printf("mirror orderbook insert failed: %v", err)
			}
		}
	}
	return written, nil
}

type MarketCapRow struct {
	Asset             string
	PriceUSD          decimal.Decimal
	CirculatingSupply decimal.Decimal
	MarketCapUSD      decimal.Decimal
	Source            string
	EventTimeMS       int64
}

func (s *Store) UpsertMarketCaps(ctx context.Context, rows []MarketCapRow) error {
	if len(rows) == 0 {
		return nil
	}
	const stmt = `
INSERT INTO asset_market_caps (asset, price_usd, circulating_supply, market_cap_usd, source, event_time_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(asset)
DO UPDATE SET
	price_usd = excluded.price_usd,
	circulating_supply = excluded.circulating_supply,
	market_cap_usd = excluded.market_cap_usd,
	source = excluded.source,
	event_time_ms = excluded.event_time_ms,
	updated_at = CURRENT_TIMESTAMP
`
	tx, err := s.sqlite.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, stmt, row.Asset, dec(row.PriceUSD), dec(row.CirculatingSupply), dec(row.MarketCapUSD), row.Source, row.EventTimeMS); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if s.ch != nil {
		states, err := s.fetchMarketCapStates(ctx, rows)
		if err != nil {
			log.Printf("mirror marketcap fetch failed: %v", err)
		} else if err := s.insertMarketCapStatesCH(ctx, states); err != nil {
			log.Printf("mirror marketcap insert failed: %v", err)
		}
	}
	return nil
}

type FundingSnapshotRow struct {
	Symbol          string
	LastFundingRate decimal.Decimal
	MarkPrice       decimal.Decimal
	EventTimeMS     int64
}

func (s *Store) UpsertFundingSnapshots(ctx context.Context, rows []FundingSnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	const stmt = `
INSERT INTO funding_rate_snapshots (symbol, last_funding_rate, mark_price, event_time_ms)
VALUES (?, ?, ?, ?)
ON CONFLICT(symbol)
DO UPDATE SET
	last_funding_rate = excluded.last_funding_rate,
	mark_price = excluded.mark_price,
	event_time_ms = excluded.event_time_ms,
	updated_at = CURRENT_TIMESTAMP
`
	tx, err := s.sqlite.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, stmt, row.Symbol, dec(row.LastFundingRate), dec(row.MarkPrice), row.EventTimeMS); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if s.ch != nil {
		states, err := s.fetchFundingStates(ctx, rows)
		if err != nil {
			log.Printf("mirror funding fetch failed: %v", err)
		} else if err := s.insertFundingStatesCH(ctx, states); err != nil {
			log.Printf("mirror funding insert failed: %v", err)
		}
	}
	return nil
}

type OISnapshotRow struct {
	Symbol        string
	OpenInterest  decimal.Decimal
	MarkPrice     decimal.Decimal
	OINotionalUSD decimal.Decimal
	EventTimeMS   int64
}

func (s *Store) UpsertOISnapshots(ctx context.Context, rows []OISnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	const stmt = `
INSERT INTO open_interest_snapshots (symbol, open_interest, mark_price, oi_notional_usd, event_time_ms)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(symbol)
DO UPDATE SET
	open_interest = excluded.open_interest,
	mark_price = excluded.mark_price,
	oi_notional_usd = excluded.oi_notional_usd,
	event_time_ms = excluded.event_time_ms,
	updated_at = CURRENT_TIMESTAMP
`
	tx, err := s.sqlite.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, stmt, row.Symbol, dec(row.OpenInterest), dec(row.MarkPrice), dec(row.OINotionalUSD), row.EventTimeMS); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if s.ch != nil {
		states, err := s.fetchOIStates(ctx, rows)
		if err != nil {
			log.Printf("mirror open interest fetch failed: %v", err)
		} else if err := s.insertOIStatesCH(ctx, states); err != nil {
			log.Printf("mirror open interest insert failed: %v", err)
		}
	}
	return nil
}

func (s *Store) CleanupAbsorptionSnapshots(ctx context.Context, retentionHours int) (int64, error) {
	res, err := s.sqlite.ExecContext(
		ctx,
		`DELETE FROM absorption_signal_snapshots
		 WHERE bucket_start_ms < (CAST(strftime('%s', 'now') AS INTEGER) * 1000) - (? * 3600 * 1000)`,
		retentionHours,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

func initClickHouse(ctx context.Context, raw string) (clickhouse.Conn, error) {
	if raw == "" {
		return nil, nil
	}
	conn, err := newClickHouseConn(raw)
	if err != nil {
		return nil, fmt.Errorf("connect clickhouse failed: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping clickhouse failed: %w", err)
	}
	if err := initClickHouseSchema(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("init clickhouse schema failed: %w", err)
	}
	return conn, nil
}

func newClickHouseConn(raw string) (clickhouse.Conn, error) {
	addr := strings.TrimSpace(raw)
	database := strings.TrimSpace(os.Getenv("CLICKHOUSE_DB"))
	if database == "" {
		database = "default"
	}
	username := strings.TrimSpace(os.Getenv("CLICKHOUSE_USER"))
	if username == "" {
		username = "default"
	}
	password := os.Getenv("CLICKHOUSE_PASSWORD")

	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		host := u.Host
		if strings.Contains(host, ":") {
			hostPort := strings.Split(host, ":")
			if len(hostPort) == 2 && hostPort[1] == "8123" {
				host = hostPort[0] + ":9000"
			}
		} else {
			host += ":9000"
		}
		addr = host
		if dbFromURL := strings.TrimPrefix(u.Path, "/"); dbFromURL != "" {
			database = dbFromURL
		}
		if u.User != nil {
			if uName := u.User.Username(); uName != "" {
				username = uName
			}
			if uPwd, ok := u.User.Password(); ok {
				password = uPwd
			}
		}
	} else {
		addr = strings.TrimPrefix(addr, "http://")
		addr = strings.TrimPrefix(addr, "https://")
		if strings.Contains(addr, "/") {
			addr = strings.Split(addr, "/")[0]
		}
		if strings.Contains(addr, ":") {
			hostPort := strings.Split(addr, ":")
			if len(hostPort) == 2 && hostPort[1] == "8123" {
				addr = hostPort[0] + ":9000"
			}
		} else {
			addr += ":9000"
		}
	}
	return clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: database, Username: username, Password: password},
		DialTimeout: 5 * time.Second,
	})
}

func initClickHouseSchema(ctx context.Context, conn clickhouse.Conn) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trade_buckets (
			market String,
			symbol String,
			bucket String,
			bucket_start_ms Int64,
			taker_buy_notional Float64,
			taker_sell_notional Float64,
			quote_notional Float64,
			trade_count Int64,
			first_trade_ms Nullable(Int64),
			last_trade_ms Nullable(Int64),
			open_price Nullable(Float64),
			close_price Nullable(Float64),
			high_price Nullable(Float64),
			low_price Nullable(Float64),
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (market, symbol, bucket, bucket_start_ms);`,
		`CREATE TABLE IF NOT EXISTS orderbook_feature_buckets (
			market String,
			symbol String,
			bucket String,
			bucket_start_ms Int64,
			spread_bps_sum Float64,
			depth_imbalance_l5_sum Float64,
			microprice_shift_bps_sum Float64,
			wall_pressure_l5_sum Float64,
			sample_count Int64,
			taker_buy_notional Float64,
			taker_sell_notional Float64,
			depletion_events Int64,
			replenishment_events Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (market, symbol, bucket, bucket_start_ms);`,
		`CREATE TABLE IF NOT EXISTS asset_market_caps (
			asset String,
			price_usd Float64,
			circulating_supply Float64,
			market_cap_usd Float64,
			source String,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (asset);`,
		`CREATE TABLE IF NOT EXISTS funding_rate_snapshots (
			symbol String,
			last_funding_rate Float64,
			mark_price Float64,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (symbol);`,
		`CREATE TABLE IF NOT EXISTS open_interest_snapshots (
			symbol String,
			open_interest Float64,
			mark_price Float64,
			oi_notional_usd Float64,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (symbol);`,
	}
	for _, stmt := range stmts {
		if err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

type tradeBucketState struct {
	Market            string
	Symbol            string
	Bucket            string
	BucketStartMS     int64
	TakerBuyNotional  float64
	TakerSellNotional float64
	QuoteNotional     float64
	TradeCount        int64
	FirstTradeMS      sql.NullInt64
	LastTradeMS       sql.NullInt64
	OpenPrice         sql.NullFloat64
	ClosePrice        sql.NullFloat64
	HighPrice         sql.NullFloat64
	LowPrice          sql.NullFloat64
}

type tradeBucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

func (s *Store) fetchTradeBucketStates(ctx context.Context, items []ingest.TradeDrainItem) ([]tradeBucketState, error) {
	keys := make([]tradeBucketKey, 0, len(items))
	seen := map[string]struct{}{}
	for _, row := range items {
		key := fmt.Sprintf("%s|%s|%s|%d", row.Key.Market, row.Key.Symbol, row.Key.Bucket, row.Key.BucketStartMS)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, tradeBucketKey{
			Market:        row.Key.Market,
			Symbol:        row.Key.Symbol,
			Bucket:        row.Key.Bucket,
			BucketStartMS: row.Key.BucketStartMS,
		})
	}
	if len(keys) == 0 {
		return nil, nil
	}

	result := make([]tradeBucketState, 0, len(keys))
	for _, part := range chunk(keys, 200) {
		where := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*4)
		for _, key := range part {
			where = append(where, "(market=? AND symbol=? AND bucket=? AND bucket_start_ms=?)")
			args = append(args, key.Market, key.Symbol, key.Bucket, key.BucketStartMS)
		}
		query := `SELECT
			market, symbol, bucket, bucket_start_ms,
			taker_buy_notional, taker_sell_notional, quote_notional, trade_count,
			first_trade_ms, last_trade_ms, open_price, close_price, high_price, low_price
		FROM trade_buckets WHERE ` + strings.Join(where, " OR ")
		rows, err := s.sqlite.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row tradeBucketState
			if err := rows.Scan(
				&row.Market,
				&row.Symbol,
				&row.Bucket,
				&row.BucketStartMS,
				&row.TakerBuyNotional,
				&row.TakerSellNotional,
				&row.QuoteNotional,
				&row.TradeCount,
				&row.FirstTradeMS,
				&row.LastTradeMS,
				&row.OpenPrice,
				&row.ClosePrice,
				&row.HighPrice,
				&row.LowPrice,
			); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return result, nil
}

func (s *Store) insertTradeBucketStatesCH(ctx context.Context, rows []tradeBucketState) error {
	if s.ch == nil || len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO trade_buckets (
		market, symbol, bucket, bucket_start_ms,
		taker_buy_notional, taker_sell_notional, quote_notional, trade_count,
		first_trade_ms, last_trade_ms, open_price, close_price, high_price, low_price, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, row := range rows {
		if err := batch.Append(
			row.Market, row.Symbol, row.Bucket, row.BucketStartMS,
			row.TakerBuyNotional, row.TakerSellNotional, row.QuoteNotional, row.TradeCount,
			nullInt64(row.FirstTradeMS), nullInt64(row.LastTradeMS), nullFloat64(row.OpenPrice), nullFloat64(row.ClosePrice), nullFloat64(row.HighPrice), nullFloat64(row.LowPrice),
			version,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

type orderbookBucketState struct {
	Market                string
	Symbol                string
	Bucket                string
	BucketStartMS         int64
	SpreadBPSSum          float64
	DepthImbalanceL5Sum   float64
	MicropriceShiftBPSSum float64
	WallPressureL5Sum     float64
	SampleCount           int64
	TakerBuyNotional      float64
	TakerSellNotional     float64
	DepletionEvents       int64
	ReplenishmentEvents   int64
}

type orderbookBucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

func (s *Store) fetchOrderbookBucketStates(ctx context.Context, items []ingest.OrderbookDrainItem) ([]orderbookBucketState, error) {
	keys := make([]orderbookBucketKey, 0, len(items))
	seen := map[string]struct{}{}
	for _, row := range items {
		key := fmt.Sprintf("%s|%s|%s|%d", row.Key.Market, row.Key.Symbol, row.Key.Bucket, row.Key.BucketStartMS)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, orderbookBucketKey{
			Market:        row.Key.Market,
			Symbol:        row.Key.Symbol,
			Bucket:        row.Key.Bucket,
			BucketStartMS: row.Key.BucketStartMS,
		})
	}
	if len(keys) == 0 {
		return nil, nil
	}

	result := make([]orderbookBucketState, 0, len(keys))
	for _, part := range chunk(keys, 200) {
		where := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*4)
		for _, key := range part {
			where = append(where, "(market=? AND symbol=? AND bucket=? AND bucket_start_ms=?)")
			args = append(args, key.Market, key.Symbol, key.Bucket, key.BucketStartMS)
		}
		query := `SELECT
			market, symbol, bucket, bucket_start_ms,
			spread_bps_sum, depth_imbalance_l5_sum, microprice_shift_bps_sum, wall_pressure_l5_sum, sample_count,
			taker_buy_notional, taker_sell_notional, depletion_events, replenishment_events
		FROM orderbook_feature_buckets WHERE ` + strings.Join(where, " OR ")
		rows, err := s.sqlite.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row orderbookBucketState
			if err := rows.Scan(
				&row.Market,
				&row.Symbol,
				&row.Bucket,
				&row.BucketStartMS,
				&row.SpreadBPSSum,
				&row.DepthImbalanceL5Sum,
				&row.MicropriceShiftBPSSum,
				&row.WallPressureL5Sum,
				&row.SampleCount,
				&row.TakerBuyNotional,
				&row.TakerSellNotional,
				&row.DepletionEvents,
				&row.ReplenishmentEvents,
			); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return result, nil
}

func (s *Store) insertOrderbookBucketStatesCH(ctx context.Context, rows []orderbookBucketState) error {
	if s.ch == nil || len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO orderbook_feature_buckets (
		market, symbol, bucket, bucket_start_ms,
		spread_bps_sum, depth_imbalance_l5_sum, microprice_shift_bps_sum, wall_pressure_l5_sum, sample_count,
		taker_buy_notional, taker_sell_notional, depletion_events, replenishment_events, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, row := range rows {
		if err := batch.Append(
			row.Market, row.Symbol, row.Bucket, row.BucketStartMS,
			row.SpreadBPSSum, row.DepthImbalanceL5Sum, row.MicropriceShiftBPSSum, row.WallPressureL5Sum, row.SampleCount,
			row.TakerBuyNotional, row.TakerSellNotional, row.DepletionEvents, row.ReplenishmentEvents,
			version,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

type marketCapState struct {
	Asset             string
	PriceUSD          float64
	CirculatingSupply float64
	MarketCapUSD      float64
	Source            string
	EventTimeMS       int64
}

func (s *Store) fetchMarketCapStates(ctx context.Context, items []MarketCapRow) ([]marketCapState, error) {
	assets := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, row := range items {
		if _, ok := seen[row.Asset]; ok {
			continue
		}
		seen[row.Asset] = struct{}{}
		assets = append(assets, row.Asset)
	}
	if len(assets) == 0 {
		return nil, nil
	}
	query := `SELECT asset, price_usd, circulating_supply, market_cap_usd, source, event_time_ms
	FROM asset_market_caps WHERE asset IN (` + placeholders(len(assets)) + `)`
	args := make([]any, 0, len(assets))
	for _, asset := range assets {
		args = append(args, asset)
	}
	rows, err := s.sqlite.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]marketCapState, 0, len(assets))
	for rows.Next() {
		var row marketCapState
		if err := rows.Scan(&row.Asset, &row.PriceUSD, &row.CirculatingSupply, &row.MarketCapUSD, &row.Source, &row.EventTimeMS); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) insertMarketCapStatesCH(ctx context.Context, rows []marketCapState) error {
	if s.ch == nil || len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO asset_market_caps (
		asset, price_usd, circulating_supply, market_cap_usd, source, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, row := range rows {
		if err := batch.Append(row.Asset, row.PriceUSD, row.CirculatingSupply, row.MarketCapUSD, row.Source, row.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

type fundingState struct {
	Symbol          string
	LastFundingRate float64
	MarkPrice       float64
	EventTimeMS     int64
}

func (s *Store) fetchFundingStates(ctx context.Context, items []FundingSnapshotRow) ([]fundingState, error) {
	symbols := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, row := range items {
		if _, ok := seen[row.Symbol]; ok {
			continue
		}
		seen[row.Symbol] = struct{}{}
		symbols = append(symbols, row.Symbol)
	}
	if len(symbols) == 0 {
		return nil, nil
	}
	query := `SELECT symbol, last_funding_rate, mark_price, event_time_ms
	FROM funding_rate_snapshots WHERE symbol IN (` + placeholders(len(symbols)) + `)`
	args := make([]any, 0, len(symbols))
	for _, symbol := range symbols {
		args = append(args, symbol)
	}
	rows, err := s.sqlite.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]fundingState, 0, len(symbols))
	for rows.Next() {
		var row fundingState
		if err := rows.Scan(&row.Symbol, &row.LastFundingRate, &row.MarkPrice, &row.EventTimeMS); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) insertFundingStatesCH(ctx context.Context, rows []fundingState) error {
	if s.ch == nil || len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO funding_rate_snapshots (
		symbol, last_funding_rate, mark_price, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, row := range rows {
		if err := batch.Append(row.Symbol, row.LastFundingRate, row.MarkPrice, row.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

type oiState struct {
	Symbol       string
	OpenInterest float64
	MarkPrice    float64
	OINotional   float64
	EventTimeMS  int64
}

func (s *Store) fetchOIStates(ctx context.Context, items []OISnapshotRow) ([]oiState, error) {
	symbols := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, row := range items {
		if _, ok := seen[row.Symbol]; ok {
			continue
		}
		seen[row.Symbol] = struct{}{}
		symbols = append(symbols, row.Symbol)
	}
	if len(symbols) == 0 {
		return nil, nil
	}
	query := `SELECT symbol, open_interest, mark_price, oi_notional_usd, event_time_ms
	FROM open_interest_snapshots WHERE symbol IN (` + placeholders(len(symbols)) + `)`
	args := make([]any, 0, len(symbols))
	for _, symbol := range symbols {
		args = append(args, symbol)
	}
	rows, err := s.sqlite.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]oiState, 0, len(symbols))
	for rows.Next() {
		var row oiState
		if err := rows.Scan(&row.Symbol, &row.OpenInterest, &row.MarkPrice, &row.OINotional, &row.EventTimeMS); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) insertOIStatesCH(ctx context.Context, rows []oiState) error {
	if s.ch == nil || len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO open_interest_snapshots (
		symbol, open_interest, mark_price, oi_notional_usd, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, row := range rows {
		if err := batch.Append(row.Symbol, row.OpenInterest, row.MarkPrice, row.OINotional, row.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func nullInt64(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullFloat64(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}
