package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

type BollPumpSignalFilter struct {
	Market      string
	Symbol      string
	Timeframe   string
	SignalLevel string
	MinScore    float64
	SinceMs     int64
	Limit       int
}

type BollPumpStateFilter struct {
	Market           string
	Symbol           string
	Timeframe        string
	Status           string
	MinPriorityScore float64
	Limit            int
}

type BollPumpPerformance struct {
	Perf1hMaxGain      float64
	Perf1hMaxDrawdown  float64
	Perf1hCloseReturn  float64
	Perf4hMaxGain      float64
	Perf4hMaxDrawdown  float64
	Perf4hCloseReturn  float64
	Perf24hMaxGain     float64
	Perf24hMaxDrawdown float64
	Perf24hCloseReturn float64
	UpdatedMs          int64
}

type bollPumpSymbolKeyRow struct {
	Key    string `db:"key"`
	Symbol string `db:"symbol"`
}

func SaveBollPumpState(ctx context.Context, store *sqlite.Store, st model.BollPumpState) error {
	if store == nil {
		return fmt.Errorf("boll pump state store is nil")
	}
	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_states
(market, symbol, timeframe, status, watch_started_ms, watch_candle_start_ms, watch_score, current_score,
 confluence_score, priority_score, bounce_count, first_pullback_low, second_pullback_low,
 pending_pullback_candle_ms, pending_pullback_high, last_checked_candle_ms, last_signal_level,
 last_alert_ms, expires_at_candle_ms, details, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(market, symbol, timeframe) DO UPDATE SET
 status = excluded.status,
 watch_started_ms = excluded.watch_started_ms,
 watch_candle_start_ms = excluded.watch_candle_start_ms,
 watch_score = excluded.watch_score,
 current_score = excluded.current_score,
 confluence_score = excluded.confluence_score,
 priority_score = excluded.priority_score,
 bounce_count = excluded.bounce_count,
 first_pullback_low = excluded.first_pullback_low,
 second_pullback_low = excluded.second_pullback_low,
 pending_pullback_candle_ms = excluded.pending_pullback_candle_ms,
 pending_pullback_high = excluded.pending_pullback_high,
 last_checked_candle_ms = excluded.last_checked_candle_ms,
 last_signal_level = excluded.last_signal_level,
 last_alert_ms = excluded.last_alert_ms,
 expires_at_candle_ms = excluded.expires_at_candle_ms,
 details = excluded.details,
 updated_at = CURRENT_TIMESTAMP`,
			st.Market, st.Symbol, st.Timeframe, st.Status, st.WatchStartedMs, st.WatchCandleStartMs,
			st.WatchScore, st.CurrentScore, st.ConfluenceScore, st.PriorityScore, st.BounceCount,
			st.FirstPullbackLow, st.SecondPullbackLow, st.PendingPullbackCandleMs, st.PendingPullbackHigh,
			st.LastCheckedCandleMs, st.LastSignalLevel, st.LastAlertMs, st.ExpiresAtCandleMs,
			defaultJSON(st.Details),
		)
		return err
	})
}

func GetBollPumpState(ctx context.Context, store *sqlite.Store, market, symbol, timeframe string) (*model.BollPumpState, error) {
	if store == nil {
		return nil, fmt.Errorf("boll pump state store is nil")
	}
	var st model.BollPumpState
	err := store.GetContext(ctx, &st, `SELECT * FROM boll_pump_states WHERE market = ? AND symbol = ? AND timeframe = ? LIMIT 1`,
		normalizeMarket(market), strings.ToUpper(strings.TrimSpace(symbol)), strings.TrimSpace(timeframe))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func ListBollPumpStates(ctx context.Context, store *sqlite.Store, f BollPumpStateFilter) ([]model.BollPumpState, error) {
	if store == nil {
		return nil, fmt.Errorf("boll pump state store is nil")
	}
	where := []string{"1=1"}
	args := make([]interface{}, 0, 6)
	if market := normalizeMarket(f.Market); market != "" {
		where = append(where, "market = ?")
		args = append(args, market)
	}
	if symbol := strings.ToUpper(strings.TrimSpace(f.Symbol)); symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, symbol)
	}
	if tf := strings.TrimSpace(f.Timeframe); tf != "" {
		where = append(where, "timeframe = ?")
		args = append(args, tf)
	}
	if status := strings.TrimSpace(f.Status); status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if f.MinPriorityScore > 0 {
		where = append(where, "priority_score >= ?")
		args = append(args, f.MinPriorityScore)
	}
	limit := clampBollPumpLimit(f.Limit)
	where = append(where, "symbol LIKE ?")
	args = append(args, "%USDT", bollPumpOverfetchLimit(limit))
	q := `SELECT * FROM boll_pump_states WHERE ` + strings.Join(where, " AND ") + ` ORDER BY priority_score DESC, updated_at DESC LIMIT ?`
	var rows []model.BollPumpState
	if err := store.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	return filterBollPumpStatesForTradableUSDT(rows, limit), nil
}

func SaveBollPumpSignal(ctx context.Context, store *sqlite.Store, sig model.BollPumpSignal, insertAnomaly bool) (int64, error) {
	if store == nil {
		return 0, fmt.Errorf("boll pump signal store is nil")
	}
	if sig.Details == nil {
		sig.Details = model.JSONB(`{}`)
	}
	var id int64
	err := store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_signals
(market, symbol, timeframe, signal_level, price, volume_ratio, boll_bandwidth, bounce_count,
 score, confluence_score, priority_score, signal_time_ms, candle_start_ms, watch_candle_start_ms,
 pullback_candle_start_ms, quote_volume_24h, perf_1h_max_gain, perf_1h_max_drawdown,
 perf_1h_close_return, perf_4h_max_gain, perf_4h_max_drawdown, perf_4h_close_return,
 perf_24h_max_gain, perf_24h_max_drawdown, perf_24h_close_return, performance_updated_ms,
 reason, details)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sig.Market, sig.Symbol, sig.Timeframe, sig.SignalLevel, sig.Price, sig.VolumeRatio,
			sig.BollBandwidth, sig.BounceCount, sig.Score, sig.ConfluenceScore, sig.PriorityScore,
			sig.SignalTimeMs, sig.CandleStartMs, sig.WatchCandleStartMs, sig.PullbackCandleStartMs,
			sig.QuoteVolume24h, sig.Perf1hMaxGain, sig.Perf1hMaxDrawdown, sig.Perf1hCloseReturn,
			sig.Perf4hMaxGain, sig.Perf4hMaxDrawdown, sig.Perf4hCloseReturn, sig.Perf24hMaxGain,
			sig.Perf24hMaxDrawdown, sig.Perf24hCloseReturn, sig.PerformanceUpdatedMs, sig.Reason,
			defaultJSON(sig.Details),
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	if insertAnomaly {
		if _, err := insertAnomalyEvents(ctx, store, []map[string]interface{}{bollPumpAnomalyEvent(sig)}); err != nil {
			return id, err
		}
	}
	return id, nil
}

func GetBollPumpSignal(ctx context.Context, store *sqlite.Store, id int64) (*model.BollPumpSignal, error) {
	if store == nil {
		return nil, fmt.Errorf("boll pump signal store is nil")
	}
	var sig model.BollPumpSignal
	err := store.GetContext(ctx, &sig, `SELECT * FROM boll_pump_signals WHERE id = ? LIMIT 1`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sig, nil
}

func ListBollPumpSignals(ctx context.Context, store *sqlite.Store, f BollPumpSignalFilter) ([]model.BollPumpSignal, error) {
	if store == nil {
		return nil, fmt.Errorf("boll pump signal store is nil")
	}
	where := []string{"1=1"}
	args := make([]interface{}, 0, 8)
	if market := normalizeMarket(f.Market); market != "" {
		where = append(where, "market = ?")
		args = append(args, market)
	}
	if symbol := strings.ToUpper(strings.TrimSpace(f.Symbol)); symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, symbol)
	}
	if tf := strings.TrimSpace(f.Timeframe); tf != "" {
		where = append(where, "timeframe = ?")
		args = append(args, tf)
	}
	if lvl := strings.TrimSpace(f.SignalLevel); lvl != "" {
		where = append(where, "signal_level = ?")
		args = append(args, lvl)
	}
	if f.MinScore > 0 {
		where = append(where, "priority_score >= ?")
		args = append(args, f.MinScore)
	}
	if f.SinceMs > 0 {
		where = append(where, "signal_time_ms >= ?")
		args = append(args, f.SinceMs)
	}
	limit := clampBollPumpLimit(f.Limit)
	where = append(where, "symbol LIKE ?")
	args = append(args, "%USDT", bollPumpOverfetchLimit(limit))
	q := `SELECT * FROM boll_pump_signals WHERE ` + strings.Join(where, " AND ") + ` ORDER BY signal_time_ms DESC, priority_score DESC LIMIT ?`
	var rows []model.BollPumpSignal
	if err := store.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	return filterBollPumpSignalsForTradableUSDT(rows, limit), nil
}

func CleanupBollPumpSignals(ctx context.Context, store *sqlite.Store, retentionDays int) (int64, error) {
	if store == nil {
		return 0, fmt.Errorf("boll pump signal store is nil")
	}
	if retentionDays < 1 {
		retentionDays = 30
	}
	cutoff := time.Now().UnixMilli() - int64(retentionDays)*24*60*60*1000
	var affected int64
	err := store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM boll_pump_signals WHERE signal_time_ms < ?`, cutoff)
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

func ExpireStaleBollPumpStates(ctx context.Context, store *sqlite.Store, staleDays int) (int64, error) {
	if store == nil {
		return 0, fmt.Errorf("boll pump state store is nil")
	}
	if staleDays < 1 {
		staleDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	var affected int64
	err := store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE boll_pump_states SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE updated_at < ? AND status NOT IN (?, ?, ?)`,
			string(BollPumpStatusExpired), cutoff, string(BollPumpStatusCompleted), string(BollPumpStatusExpired), string(BollPumpStatusInvalidated))
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

func UpdateBollPumpPerformance(ctx context.Context, store *sqlite.Store, signalID int64, perf BollPumpPerformance) error {
	if store == nil {
		return fmt.Errorf("boll pump signal store is nil")
	}
	return store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boll_pump_signals SET
perf_1h_max_gain = ?, perf_1h_max_drawdown = ?, perf_1h_close_return = ?,
perf_4h_max_gain = ?, perf_4h_max_drawdown = ?, perf_4h_close_return = ?,
perf_24h_max_gain = ?, perf_24h_max_drawdown = ?, perf_24h_close_return = ?,
performance_updated_ms = ?
WHERE id = ?`,
			perf.Perf1hMaxGain, perf.Perf1hMaxDrawdown, perf.Perf1hCloseReturn,
			perf.Perf4hMaxGain, perf.Perf4hMaxDrawdown, perf.Perf4hCloseReturn,
			perf.Perf24hMaxGain, perf.Perf24hMaxDrawdown, perf.Perf24hCloseReturn,
			perf.UpdatedMs, signalID)
		return err
	})
}

func BollPumpStats(ctx context.Context, store *sqlite.Store, market string, sinceMs int64) (map[string]interface{}, error) {
	if store == nil {
		return nil, fmt.Errorf("boll pump signal store is nil")
	}
	market = normalizeMarket(market)
	if market == "" {
		market = "swap"
	}
	if sinceMs <= 0 {
		sinceMs = time.Now().UnixMilli() - 30*24*60*60*1000
	}
	var levels []bollPumpSymbolKeyRow
	if err := store.SelectContext(ctx, &levels, `SELECT signal_level AS key, symbol FROM boll_pump_signals WHERE market = ? AND signal_time_ms >= ? AND symbol LIKE ?`, market, sinceMs, "%USDT"); err != nil {
		return nil, err
	}
	var timeframes []bollPumpSymbolKeyRow
	if err := store.SelectContext(ctx, &timeframes, `SELECT timeframe AS key, symbol FROM boll_pump_signals WHERE market = ? AND signal_time_ms >= ? AND symbol LIKE ?`, market, sinceMs, "%USDT"); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"market":            market,
		"generatedAtMs":     time.Now().UnixMilli(),
		"countsByLevel":     countTradableSymbolRowsMap(levels),
		"countsByTimeframe": countTradableSymbolRowsMap(timeframes),
	}, nil
}

func bollPumpAnomalyEvent(sig model.BollPumpSignal) map[string]interface{} {
	details := map[string]interface{}{
		"signalLevel":     sig.SignalLevel,
		"score":           sig.Score,
		"confluenceScore": sig.ConfluenceScore,
		"priorityScore":   sig.PriorityScore,
		"volumeRatio":     sig.VolumeRatio,
		"bollBandwidth":   sig.BollBandwidth,
		"bounceCount":     sig.BounceCount,
		"quoteVolume24h":  sig.QuoteVolume24h,
		"reason":          sig.Reason,
	}
	raw, _ := json.Marshal(details)
	return map[string]interface{}{
		"market":        sig.Market,
		"symbol":        sig.Symbol,
		"event_type":    "boll_pump",
		"tf_signal":     sig.Timeframe,
		"tf_level":      sig.SignalLevel,
		"event_time_ms": sig.SignalTimeMs,
		"title":         fmt.Sprintf("%s %s %s price=%.6g score=%.0f", sig.SignalLevel, sig.Symbol, sig.Timeframe, sig.Price, sig.PriorityScore),
		"details":       string(raw),
	}
}

func defaultJSON(v model.JSONB) model.JSONB {
	if len(v) == 0 {
		return model.JSONB(`{}`)
	}
	return v
}

func normalizeMarket(market string) string {
	m := strings.ToLower(strings.TrimSpace(market))
	if m == "spot" || m == "swap" {
		return m
	}
	return ""
}

func clampBollPumpLimit(limit int) int {
	if limit <= 0 {
		return 200
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func bollPumpOverfetchLimit(limit int) int {
	n := limit * 3
	if n < limit {
		n = limit
	}
	if n > 5000 {
		return 5000
	}
	return n
}

func filterBollPumpSignalsForTradableUSDT(rows []model.BollPumpSignal, limit int) []model.BollPumpSignal {
	out := make([]model.BollPumpSignal, 0, len(rows))
	for _, row := range rows {
		if !bollPumpTradableUSDTSymbol(row.Symbol) {
			continue
		}
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func filterBollPumpStatesForTradableUSDT(rows []model.BollPumpState, limit int) []model.BollPumpState {
	out := make([]model.BollPumpState, 0, len(rows))
	for _, row := range rows {
		if !bollPumpTradableUSDTSymbol(row.Symbol) {
			continue
		}
		out = append(out, normalizeBollPumpStateForList(row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PriorityScore > out[j].PriorityScore
	})
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func normalizeBollPumpStateForList(row model.BollPumpState) model.BollPumpState {
	if bollPumpStatusIsActive(row.Status) {
		return row
	}
	row.WatchScore = 0
	row.CurrentScore = 0
	row.ConfluenceScore = 0
	row.PriorityScore = 0
	row.LastSignalLevel = nil
	return row
}

func countTradableSymbolRowsMap(rows []bollPumpSymbolKeyRow) map[string]int64 {
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		if bollPumpTradableUSDTSymbol(r.Symbol) {
			out[r.Key]++
		}
	}
	return out
}
