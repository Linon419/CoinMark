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
	Enabled                   bool               `json:"enabled"`
	Market                    string             `json:"market"`
	Timeframes                []string           `json:"timeframes"`
	SymbolLimit               int                `json:"symbol_limit"`
	ScanTimeoutSec            int                `json:"scan_timeout_sec"`
	BollPeriod                int                `json:"boll_period"`
	BollStdDev                float64            `json:"boll_std_dev"`
	ATRPeriod                 int                `json:"atr_period"`
	StartupWindows            map[string]int     `json:"startup_windows"`
	GainThresholds            map[string]float64 `json:"gain_thresholds"`
	VolumeThresholds          map[string]float64 `json:"volume_thresholds"`
	BackgroundLookback        int                `json:"background_lookback"`
	BackgroundRecentWindow    int                `json:"background_recent_window"`
	BackgroundRecentMinPass   int                `json:"background_recent_min_pass"`
	LowVolumeFactor           float64            `json:"low_volume_factor"`
	MiddleNearBandwidthFactor float64            `json:"middle_near_bandwidth_factor"`
	ThinQuoteVolume24h        float64            `json:"thin_quote_volume_24h"`
	WatchTelegramThreshold    float64            `json:"watch_telegram_threshold"`
	Confirm1TelegramThreshold float64            `json:"confirm1_telegram_threshold"`
	Confirm2TelegramThreshold float64            `json:"confirm2_telegram_threshold"`
	ConfluenceWindowMs        int64              `json:"confluence_window_ms"`
	StageExpiryCandles        int                `json:"stage_expiry_candles"`
}

type BollPumpWatchResult struct {
	Triggered       bool
	Signal          model.BollPumpSignal
	BackgroundScore float64
	Reasons         []string
}
