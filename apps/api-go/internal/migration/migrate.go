package migration

import (
	"context"
	"log"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

func Migrate(ctx context.Context, store *sqlite.Store) error {
	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, ddl := range schemaDDL {
			if _, err := tx.Exec(ddl); err != nil {
				log.Printf("migration exec error: %v, sql: %.120s", err, ddl)
				return err
			}
		}
		if err := ensureColumn(tx, "tg_notify_prefs", "absorption_enabled", "BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		return nil
	})
}

func ensureColumn(tx *sqlx.Tx, table, column, definition string) error {
	rows, err := tx.Queryx("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		row := make(map[string]interface{})
		if err := rows.MapScan(row); err != nil {
			return err
		}
		if name, ok := row["name"].(string); ok && name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

var schemaDDL = []string{
	// trade_buckets
	`CREATE TABLE IF NOT EXISTS trade_buckets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		bucket VARCHAR(8) NOT NULL,
		bucket_start_ms BIGINT NOT NULL,
		taker_buy_notional NUMERIC(38,18) NOT NULL DEFAULT 0,
		taker_sell_notional NUMERIC(38,18) NOT NULL DEFAULT 0,
		quote_notional NUMERIC(38,18) NOT NULL DEFAULT 0,
		trade_count INTEGER NOT NULL DEFAULT 0,
		first_trade_ms BIGINT,
		last_trade_ms BIGINT,
		open_price NUMERIC(38,18),
		close_price NUMERIC(38,18),
		high_price NUMERIC(38,18),
		low_price NUMERIC(38,18),
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, bucket, bucket_start_ms)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_trade_bucket_query ON trade_buckets (market, bucket, bucket_start_ms)`,
	`CREATE INDEX IF NOT EXISTS ix_trade_bucket_symbol ON trade_buckets (market, symbol, bucket, bucket_start_ms)`,

	// orderbook_feature_buckets
	`CREATE TABLE IF NOT EXISTS orderbook_feature_buckets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		bucket VARCHAR(8) NOT NULL,
		bucket_start_ms BIGINT NOT NULL,
		spread_bps_sum NUMERIC(38,18) NOT NULL DEFAULT 0,
		microprice_shift_bps_sum NUMERIC(38,18) NOT NULL DEFAULT 0,
		depth_imbalance_l20_sum NUMERIC(38,18) NOT NULL DEFAULT 0,
		wall_pressure_l20_sum NUMERIC(38,18) NOT NULL DEFAULT 0,
		sample_count INTEGER NOT NULL DEFAULT 0,
		taker_buy_notional NUMERIC(38,18) NOT NULL DEFAULT 0,
		taker_sell_notional NUMERIC(38,18) NOT NULL DEFAULT 0,
		depletion_events INTEGER NOT NULL DEFAULT 0,
		replenishment_events INTEGER NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, bucket, bucket_start_ms)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_orderbook_feature_symbol ON orderbook_feature_buckets (market, symbol, bucket, bucket_start_ms)`,
	`CREATE INDEX IF NOT EXISTS ix_orderbook_feature_query ON orderbook_feature_buckets (market, bucket, bucket_start_ms)`,

	// funding_rate_snapshots
	`CREATE TABLE IF NOT EXISTS funding_rate_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		symbol VARCHAR(32) NOT NULL UNIQUE,
		last_funding_rate NUMERIC(18,10) NOT NULL,
		mark_price NUMERIC(38,18) NOT NULL,
		event_time_ms BIGINT NOT NULL,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,

	// open_interest_snapshots
	`CREATE TABLE IF NOT EXISTS open_interest_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		symbol VARCHAR(32) NOT NULL UNIQUE,
		open_interest NUMERIC(38,18) NOT NULL,
		mark_price NUMERIC(38,18) NOT NULL,
		oi_notional_usd NUMERIC(38,18) NOT NULL,
		event_time_ms BIGINT NOT NULL,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,

	// asset_market_caps
	`CREATE TABLE IF NOT EXISTS asset_market_caps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		asset VARCHAR(32) NOT NULL UNIQUE,
		price_usd NUMERIC(38,18) NOT NULL,
		circulating_supply NUMERIC(38,18) NOT NULL,
		market_cap_usd NUMERIC(38,18) NOT NULL,
		source VARCHAR(64) NOT NULL,
		event_time_ms BIGINT NOT NULL,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS ix_asset_market_caps_cap ON asset_market_caps (market_cap_usd)`,

	// favorites
	`CREATE TABLE IF NOT EXISTS favorites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_id VARCHAR(64) NOT NULL,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (client_id, market, symbol)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_favorites_client ON favorites (client_id)`,

	// coin_info
	`CREATE TABLE IF NOT EXISTS coin_info (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		symbol VARCHAR(32) NOT NULL UNIQUE,
		whale_min_val NUMERIC(38,18),
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,

	// sr_levels
	`CREATE TABLE IF NOT EXISTS sr_levels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		level_price NUMERIC(38,18) NOT NULL,
		timeframe VARCHAR(8) NOT NULL,
		touches INTEGER NOT NULL,
		strength_score NUMERIC(18,6) NOT NULL,
		last_touch_ms BIGINT NOT NULL,
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, timeframe, level_price)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_sr_levels_symbol ON sr_levels (market, symbol, timeframe)`,

	// anomaly_events
	`CREATE TABLE IF NOT EXISTS anomaly_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		event_type VARCHAR(32) NOT NULL,
		tf_signal VARCHAR(8) NOT NULL,
		tf_level VARCHAR(8),
		event_time_ms BIGINT NOT NULL,
		title VARCHAR(256) NOT NULL,
		details JSON NOT NULL,
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS ix_anomaly_events_time ON anomaly_events (event_time_ms DESC)`,
	`CREATE INDEX IF NOT EXISTS ix_anomaly_events_symbol ON anomaly_events (market, symbol, event_time_ms DESC)`,

	// absorption_signal_snapshots
	`CREATE TABLE IF NOT EXISTS absorption_signal_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		bucket_start_ms BIGINT NOT NULL,
		direction VARCHAR(16) NOT NULL,
		signal_state VARCHAR(16) NOT NULL,
		score NUMERIC(10,4) NOT NULL DEFAULT 0,
		net_flow_strength NUMERIC(18,8),
		impact_per_notional NUMERIC(18,12),
		window_4h_passed BOOLEAN NOT NULL DEFAULT 0,
		window_1d_passed BOOLEAN NOT NULL DEFAULT 0,
		window_3d_passed BOOLEAN NOT NULL DEFAULT 0,
		windows JSON NOT NULL DEFAULT '{}',
		reasons JSON NOT NULL DEFAULT '[]',
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, bucket_start_ms, direction)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_absorption_signal_market_time ON absorption_signal_snapshots (market, bucket_start_ms)`,
	`CREATE INDEX IF NOT EXISTS ix_absorption_signal_market_symbol ON absorption_signal_snapshots (market, symbol, bucket_start_ms)`,

	// orderbook_heatmap_1m
	`CREATE TABLE IF NOT EXISTS orderbook_heatmap_1m (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		bucket_start_ms BIGINT NOT NULL,
		side VARCHAR(8) NOT NULL DEFAULT 'unknown',
		price_bin NUMERIC(38,18) NOT NULL,
		price_step NUMERIC(38,18) NOT NULL,
		intensity NUMERIC(38,18) NOT NULL DEFAULT 0,
		level_count INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, bucket_start_ms, side, price_bin)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_time ON orderbook_heatmap_1m (market, bucket_start_ms)`,
	`CREATE INDEX IF NOT EXISTS ix_orderbook_heatmap_market_symbol ON orderbook_heatmap_1m (market, symbol, bucket_start_ms)`,

	// boll_pump_states
	`CREATE TABLE IF NOT EXISTS boll_pump_states (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		timeframe VARCHAR(8) NOT NULL,
		status VARCHAR(32) NOT NULL,
		watch_started_ms BIGINT,
		watch_candle_start_ms BIGINT,
		watch_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		current_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		confluence_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		priority_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		bounce_count INTEGER NOT NULL DEFAULT 0,
		first_pullback_low NUMERIC(38,18),
		second_pullback_low NUMERIC(38,18),
		pending_pullback_candle_ms BIGINT,
		pending_pullback_high NUMERIC(38,18),
		last_checked_candle_ms BIGINT,
		last_signal_level VARCHAR(16),
		last_alert_ms BIGINT,
		expires_at_candle_ms BIGINT,
		details JSON NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (market, symbol, timeframe)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_boll_pump_states_status ON boll_pump_states (market, status, updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS ix_boll_pump_states_symbol ON boll_pump_states (market, symbol, timeframe)`,

	// boll_pump_signals
	`CREATE TABLE IF NOT EXISTS boll_pump_signals (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		market VARCHAR(8) NOT NULL,
		symbol VARCHAR(32) NOT NULL,
		timeframe VARCHAR(8) NOT NULL,
		signal_level VARCHAR(16) NOT NULL,
		price NUMERIC(38,18) NOT NULL,
		volume_ratio NUMERIC(18,8) NOT NULL DEFAULT 0,
		boll_bandwidth NUMERIC(18,8) NOT NULL DEFAULT 0,
		bounce_count INTEGER NOT NULL DEFAULT 0,
		score NUMERIC(10,4) NOT NULL DEFAULT 0,
		confluence_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		priority_score NUMERIC(10,4) NOT NULL DEFAULT 0,
		signal_time_ms BIGINT NOT NULL,
		candle_start_ms BIGINT NOT NULL,
		watch_candle_start_ms BIGINT,
		pullback_candle_start_ms BIGINT,
		quote_volume_24h NUMERIC(38,18) NOT NULL DEFAULT 0,
		perf_1h_max_gain NUMERIC(18,8),
		perf_1h_max_drawdown NUMERIC(18,8),
		perf_1h_close_return NUMERIC(18,8),
		perf_4h_max_gain NUMERIC(18,8),
		perf_4h_max_drawdown NUMERIC(18,8),
		perf_4h_close_return NUMERIC(18,8),
		perf_24h_max_gain NUMERIC(18,8),
		perf_24h_max_drawdown NUMERIC(18,8),
		perf_24h_close_return NUMERIC(18,8),
		performance_updated_ms BIGINT,
		reason VARCHAR(512) NOT NULL,
		details JSON NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_time ON boll_pump_signals (market, signal_time_ms DESC)`,
	`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_symbol ON boll_pump_signals (market, symbol, signal_time_ms DESC)`,
	`CREATE INDEX IF NOT EXISTS ix_boll_pump_signals_level ON boll_pump_signals (market, signal_level, signal_time_ms DESC)`,

	// tg_notify_prefs (group-level notify switches)
	`CREATE TABLE IF NOT EXISTS tg_notify_prefs (
		chat_id BIGINT PRIMARY KEY,
		market_anomaly_enabled BOOLEAN NOT NULL DEFAULT 1,
		whale_wall_enabled BOOLEAN NOT NULL DEFAULT 0,
		absorption_enabled BOOLEAN NOT NULL DEFAULT 0,
		signal_lab_enabled BOOLEAN NOT NULL DEFAULT 0,
		mute_all BOOLEAN NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
	)`,
}
