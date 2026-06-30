package service

import "coinmark/api-go/internal/model"

type BollPumpSignalLevel string
type BollPumpStateStatus string

const (
	BollPumpLevelWatch    BollPumpSignalLevel = "WATCH"
	BollPumpLevelConfirm1 BollPumpSignalLevel = "CONFIRM_1"
	BollPumpLevelConfirm2 BollPumpSignalLevel = "CONFIRM_2"

	BollPumpStatusIdle             BollPumpStateStatus = "IDLE"
	BollPumpStatusWatch            BollPumpStateStatus = "WATCH"
	BollPumpStatusPullback1Pending BollPumpStateStatus = "PULLBACK_1_PENDING"
	BollPumpStatusConfirm1         BollPumpStateStatus = "CONFIRM_1"
	BollPumpStatusPullback2Pending BollPumpStateStatus = "PULLBACK_2_PENDING"
	BollPumpStatusConfirm2         BollPumpStateStatus = "CONFIRM_2"
	BollPumpStatusCompleted        BollPumpStateStatus = "COMPLETED"
	BollPumpStatusExpired          BollPumpStateStatus = "EXPIRED"
	BollPumpStatusInvalidated      BollPumpStateStatus = "INVALIDATED"
)

type BollPumpBar struct {
	OpenTimeMs  int64
	CloseTimeMs int64
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	QuoteVolume float64
	Closed      bool
}

type BollPumpIndicator struct {
	Middle    float64
	Upper     float64
	Lower     float64
	Bandwidth float64
	ATR14     float64
	ATRRatio  float64
	ValidBoll bool
	ValidATR  bool
}

type BollPumpConfig struct {
	Enabled                       bool               `json:"enabled"`
	Market                        string             `json:"market"`
	Timeframes                    []string           `json:"timeframes"`
	SymbolLimit                   int                `json:"symbol_limit"`
	ScanTimeoutSec                int                `json:"scan_timeout_sec"`
	BollPeriod                    int                `json:"boll_period"`
	BollStdDev                    float64            `json:"boll_std_dev"`
	ATRPeriod                     int                `json:"atr_period"`
	StartupWindows                map[string]int     `json:"startup_windows"`
	GainThresholds                map[string]float64 `json:"gain_thresholds"`
	VolumeThresholds              map[string]float64 `json:"volume_thresholds"`
	BackgroundLookback            int                `json:"background_lookback"`
	BackgroundRecentWindow        int                `json:"background_recent_window"`
	BackgroundRecentMinPass       int                `json:"background_recent_min_pass"`
	LowVolumeFactor               float64            `json:"low_volume_factor"`
	MiddleNearBandwidthFactor     float64            `json:"middle_near_bandwidth_factor"`
	ThinQuoteVolume24h            float64            `json:"thin_quote_volume_24h"`
	WatchTrendCheckCandles        int                `json:"watch_trend_check_candles"`
	WatchTrendMaxDrawdownPct      float64            `json:"watch_trend_max_drawdown_pct"`
	WatchTrendMaxDrawdownATR      float64            `json:"watch_trend_max_drawdown_atr"`
	TrendCleanBonus               float64            `json:"trend_clean_bonus"`
	TrendWickPenalty              float64            `json:"trend_wick_penalty"`
	TrendWeakPenalty              float64            `json:"trend_weak_penalty"`
	TrendWickBodyMaxRatio         float64            `json:"trend_wick_body_max_ratio"`
	TrendEfficiencyMin            float64            `json:"trend_efficiency_min"`
	MinimumTrendTimeframe         string             `json:"minimum_trend_timeframe"`
	MinimumTrendCheckCandles      int                `json:"minimum_trend_check_candles"`
	MinimumTrendGainPct           float64            `json:"minimum_trend_gain_pct"`
	MinimumTrendEfficiencyMin     float64            `json:"minimum_trend_efficiency_min"`
	MinimumTrendRisingRatio       float64            `json:"minimum_trend_rising_ratio"`
	ResistanceLookback            int                `json:"resistance_lookback"`
	ResistanceSwingSpan           int                `json:"resistance_swing_span"`
	ResistanceClusterATR          float64            `json:"resistance_cluster_atr"`
	ResistanceClusterPct          float64            `json:"resistance_cluster_pct"`
	ResistanceBreakoutBufferPct   float64            `json:"resistance_breakout_buffer_pct"`
	ResistanceMaxDistancePct      float64            `json:"resistance_max_distance_pct"`
	ResistanceMinTouches          int                `json:"resistance_min_touches"`
	ResistanceBreakoutScore       float64            `json:"resistance_breakout_score"`
	Resistance4HLookback          int                `json:"resistance_4h_lookback"`
	Resistance4HSwingSpan         int                `json:"resistance_4h_swing_span"`
	Resistance4HClusterATR        float64            `json:"resistance_4h_cluster_atr"`
	Resistance4HClusterPct        float64            `json:"resistance_4h_cluster_pct"`
	Resistance4HBreakoutBufferPct float64            `json:"resistance_4h_breakout_buffer_pct"`
	Resistance4HMaxDistancePct    float64            `json:"resistance_4h_max_distance_pct"`
	Resistance4HMinTouches        int                `json:"resistance_4h_min_touches"`
	Resistance4HBreakoutBonus     float64            `json:"resistance_4h_breakout_bonus"`
	WatchTelegramThreshold        float64            `json:"watch_telegram_threshold"`
	Confirm1TelegramThreshold     float64            `json:"confirm1_telegram_threshold"`
	Confirm2TelegramThreshold     float64            `json:"confirm2_telegram_threshold"`
	ConfluenceWindowMs            int64              `json:"confluence_window_ms"`
	StageExpiryCandles            int                `json:"stage_expiry_candles"`
}

type BollPumpWatchResult struct {
	Triggered       bool
	Signal          model.BollPumpSignal
	BackgroundScore float64
	Reasons         []string
}
