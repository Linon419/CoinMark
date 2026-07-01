package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

const bollPumpSettingsName = "default"

func LoadBollPumpConfig(ctx context.Context, store *sqlite.Store, fallback BollPumpConfig) (BollPumpConfig, error) {
	cfg := NormalizeBollPumpConfig(fallback)
	if store == nil {
		return cfg, nil
	}
	var raw string
	err := store.GetContext(ctx, &raw, `SELECT config FROM boll_pump_settings WHERE name = ? LIMIT 1`, bollPumpSettingsName)
	if err == sql.ErrNoRows {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return NormalizeBollPumpConfig(fallback), err
	}
	return NormalizeBollPumpConfig(cfg), nil
}

func SaveBollPumpConfig(ctx context.Context, store *sqlite.Store, cfg BollPumpConfig) (BollPumpConfig, error) {
	if store == nil {
		return BollPumpConfig{}, fmt.Errorf("boll pump settings store is nil")
	}
	cfg = NormalizeBollPumpConfig(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return BollPumpConfig{}, err
	}
	err = store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_settings (name, config, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET config = excluded.config, updated_at = CURRENT_TIMESTAMP`,
			bollPumpSettingsName, string(raw))
		return err
	})
	if err != nil {
		return BollPumpConfig{}, err
	}
	return cfg, nil
}

func NormalizeBollPumpConfig(in BollPumpConfig) BollPumpConfig {
	def := DefaultBollPumpConfig()
	out := def

	out.Enabled = in.Enabled
	if market := normalizeBollPumpMarket(in.Market); market != "" {
		out.Market = market
	}
	if len(in.Timeframes) > 0 {
		out.Timeframes = normalizeBollPumpTimeframes(in.Timeframes)
	}
	if in.SymbolLimit > 0 {
		out.SymbolLimit = clampInt(in.SymbolLimit, 20, 1000)
	}
	if in.ScanTimeoutSec > 0 {
		out.ScanTimeoutSec = clampInt(in.ScanTimeoutSec, 5, 300)
	}
	if in.BollPeriod > 0 {
		out.BollPeriod = clampInt(in.BollPeriod, 5, 100)
	}
	if in.BollStdDev > 0 {
		out.BollStdDev = clampFloat(in.BollStdDev, 0.5, 5)
	}
	if in.ATRPeriod > 0 {
		out.ATRPeriod = clampInt(in.ATRPeriod, 2, 100)
	}

	out.StartupWindows = mergeIntTimeframeMap(def.StartupWindows, in.StartupWindows, 1, 120)
	out.GainThresholds = mergeFloatTimeframeMap(def.GainThresholds, in.GainThresholds, 0.001, 1)
	out.VolumeThresholds = mergeFloatTimeframeMap(def.VolumeThresholds, in.VolumeThresholds, 0.1, 50)

	if in.BackgroundLookback > 0 {
		out.BackgroundLookback = clampInt(in.BackgroundLookback, 20, 500)
	}
	if in.BackgroundRecentWindow > 0 {
		out.BackgroundRecentWindow = clampInt(in.BackgroundRecentWindow, 3, 100)
	}
	if in.BackgroundRecentMinPass > 0 {
		out.BackgroundRecentMinPass = clampInt(in.BackgroundRecentMinPass, 1, out.BackgroundRecentWindow)
	}
	if in.LowVolumeFactor > 0 {
		out.LowVolumeFactor = clampFloat(in.LowVolumeFactor, 0.05, 2)
	}
	if in.MiddleNearBandwidthFactor > 0 {
		out.MiddleNearBandwidthFactor = clampFloat(in.MiddleNearBandwidthFactor, 0.05, 2)
	}
	if in.ThinQuoteVolume24h > 0 {
		out.ThinQuoteVolume24h = clampFloat(in.ThinQuoteVolume24h, 100_000, 100_000_000)
	}
	if in.WatchTrendCheckCandles > 0 {
		out.WatchTrendCheckCandles = clampInt(in.WatchTrendCheckCandles, 2, 60)
	}
	if in.WatchTrendMaxDrawdownPct > 0 {
		out.WatchTrendMaxDrawdownPct = clampFloat(in.WatchTrendMaxDrawdownPct, 0.001, 0.2)
	}
	if in.WatchTrendMaxDrawdownATR > 0 {
		out.WatchTrendMaxDrawdownATR = clampFloat(in.WatchTrendMaxDrawdownATR, 0.1, 5)
	}
	out.TrendCleanBonus = clampFloat(in.TrendCleanBonus, 0, 30)
	out.TrendWickPenalty = normalizeBollPumpScorePenalty(in.TrendWickPenalty)
	out.TrendWeakPenalty = normalizeBollPumpScorePenalty(in.TrendWeakPenalty)
	if in.TrendWickBodyMaxRatio > 0 {
		out.TrendWickBodyMaxRatio = clampFloat(in.TrendWickBodyMaxRatio, 0.05, 0.8)
	}
	if in.TrendEfficiencyMin > 0 {
		out.TrendEfficiencyMin = clampFloat(in.TrendEfficiencyMin, 0.05, 1)
	}
	if tf := normalizeMinimumTrendTimeframe(in.MinimumTrendTimeframe); tf != "" {
		out.MinimumTrendTimeframe = tf
	}
	if in.MinimumTrendCheckCandles > 0 {
		out.MinimumTrendCheckCandles = clampInt(in.MinimumTrendCheckCandles, 3, 40)
	}
	if in.MinimumTrendGainPct > 0 {
		out.MinimumTrendGainPct = clampFloat(in.MinimumTrendGainPct, 0.001, 0.2)
	}
	if in.MinimumTrendEfficiencyMin > 0 {
		out.MinimumTrendEfficiencyMin = clampFloat(in.MinimumTrendEfficiencyMin, 0.05, 1)
	}
	if in.MinimumTrendRisingRatio > 0 {
		out.MinimumTrendRisingRatio = clampFloat(in.MinimumTrendRisingRatio, 0.1, 1)
	}
	if in.ResistanceLookback > 0 {
		out.ResistanceLookback = clampInt(in.ResistanceLookback, 20, 300)
	}
	if in.ResistanceSwingSpan > 0 {
		out.ResistanceSwingSpan = clampInt(in.ResistanceSwingSpan, 1, 5)
	}
	if in.ResistanceClusterATR > 0 {
		out.ResistanceClusterATR = clampFloat(in.ResistanceClusterATR, 0.1, 3)
	}
	if in.ResistanceClusterPct > 0 {
		out.ResistanceClusterPct = clampFloat(in.ResistanceClusterPct, 0.001, 0.05)
	}
	if in.ResistanceBreakoutBufferPct > 0 {
		out.ResistanceBreakoutBufferPct = clampFloat(in.ResistanceBreakoutBufferPct, 0.001, 0.05)
	}
	if in.ResistanceMaxDistancePct > 0 {
		out.ResistanceMaxDistancePct = clampFloat(in.ResistanceMaxDistancePct, 0.005, 0.2)
	}
	if in.ResistanceMinTouches > 0 {
		out.ResistanceMinTouches = clampInt(in.ResistanceMinTouches, 1, 8)
	}
	if in.ResistanceBreakoutScore > 0 {
		out.ResistanceBreakoutScore = clampFloat(in.ResistanceBreakoutScore, 0, 50)
	}
	if in.Resistance4HLookback > 0 {
		out.Resistance4HLookback = clampInt(in.Resistance4HLookback, 20, 200)
	}
	if in.Resistance4HSwingSpan > 0 {
		out.Resistance4HSwingSpan = clampInt(in.Resistance4HSwingSpan, 1, 5)
	}
	if in.Resistance4HClusterATR > 0 {
		out.Resistance4HClusterATR = clampFloat(in.Resistance4HClusterATR, 0.1, 3)
	}
	if in.Resistance4HClusterPct > 0 {
		out.Resistance4HClusterPct = clampFloat(in.Resistance4HClusterPct, 0.001, 0.05)
	}
	if in.Resistance4HBreakoutBufferPct > 0 {
		out.Resistance4HBreakoutBufferPct = clampFloat(in.Resistance4HBreakoutBufferPct, 0.001, 0.05)
	}
	if in.Resistance4HMaxDistancePct > 0 {
		out.Resistance4HMaxDistancePct = clampFloat(in.Resistance4HMaxDistancePct, 0.005, 0.2)
	}
	if in.Resistance4HMinTouches > 0 {
		out.Resistance4HMinTouches = clampInt(in.Resistance4HMinTouches, 1, 6)
	}
	if in.Resistance4HBreakoutBonus > 0 {
		out.Resistance4HBreakoutBonus = clampFloat(in.Resistance4HBreakoutBonus, 0, 50)
	}
	out.KeyK4HEnabled = in.KeyK4HEnabled
	if in.KeyK4HLookback > 0 {
		out.KeyK4HLookback = clampInt(in.KeyK4HLookback, 30, 500)
	}
	if in.KeyK4HThreshold > 0 {
		out.KeyK4HThreshold = clampFloat(in.KeyK4HThreshold, 0.1, 1)
	}
	if in.KeyK4HMinVolumeRatio > 0 {
		out.KeyK4HMinVolumeRatio = clampFloat(in.KeyK4HMinVolumeRatio, 0, 20)
	}
	if in.KeyK4HMinBodyPct > 0 {
		out.KeyK4HMinBodyPct = clampFloat(in.KeyK4HMinBodyPct, def.KeyK4HMinBodyPct, 0.2)
	}
	if in.KeyK4HMaxStickyScore > 0 {
		out.KeyK4HMaxStickyScore = clampFloat(in.KeyK4HMaxStickyScore, 0.1, 1)
	}
	if in.KeyK4HTelegramThreshold > 0 {
		out.KeyK4HTelegramThreshold = clampFloat(in.KeyK4HTelegramThreshold, 0, 200)
	}
	if in.WatchTelegramThreshold > 0 {
		out.WatchTelegramThreshold = clampFloat(in.WatchTelegramThreshold, 0, 100)
	}
	if in.Confirm1TelegramThreshold > 0 {
		out.Confirm1TelegramThreshold = clampFloat(in.Confirm1TelegramThreshold, 0, 100)
	}
	if in.Confirm2TelegramThreshold > 0 {
		out.Confirm2TelegramThreshold = clampFloat(in.Confirm2TelegramThreshold, 0, 100)
	}
	if in.ConfluenceWindowMs > 0 {
		out.ConfluenceWindowMs = int64(clampFloat(float64(in.ConfluenceWindowMs), 60_000, 3_600_000))
	}
	if in.StageExpiryCandles > 0 {
		out.StageExpiryCandles = clampInt(in.StageExpiryCandles, 5, 300)
	}
	if len(out.Timeframes) == 0 {
		out.Timeframes = def.Timeframes
	}
	return out
}

func normalizeBollPumpTimeframes(input []string) []string {
	allowed := map[string]bool{"1m": true, "3m": true, "5m": true, "15m": true, "30m": true, "1h": true}
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, tf := range input {
		tf = strings.TrimSpace(tf)
		if allowed[tf] && !seen[tf] {
			out = append(out, tf)
			seen[tf] = true
		}
	}
	return out
}

func normalizeMinimumTrendTimeframe(tf string) string {
	switch strings.TrimSpace(tf) {
	case "15m", "30m", "1h":
		return strings.TrimSpace(tf)
	default:
		return ""
	}
}

func mergeIntTimeframeMap(def map[string]int, input map[string]int, min, max int) map[string]int {
	out := make(map[string]int, len(def))
	for tf, v := range def {
		out[tf] = v
	}
	for tf, v := range input {
		if _, ok := out[tf]; ok && v > 0 {
			out[tf] = clampInt(v, min, max)
		}
	}
	return out
}

func mergeFloatTimeframeMap(def map[string]float64, input map[string]float64, min, max float64) map[string]float64 {
	out := make(map[string]float64, len(def))
	for tf, v := range def {
		out[tf] = v
	}
	for tf, v := range input {
		if _, ok := out[tf]; ok && v > 0 {
			out[tf] = clampFloat(v, min, max)
		}
	}
	return out
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func normalizeBollPumpScorePenalty(v float64) float64 {
	if v > 0 {
		v = -v
	}
	return clampFloat(v, -50, 0)
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
