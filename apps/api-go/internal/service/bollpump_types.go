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
	Enabled                   bool
	Market                    string
	Timeframes                []string
	SymbolLimit               int
	ScanTimeoutSec            int
	BollPeriod                int
	BollStdDev                float64
	ATRPeriod                 int
	StartupWindows            map[string]int
	GainThresholds            map[string]float64
	VolumeThresholds          map[string]float64
	BackgroundLookback        int
	BackgroundRecentWindow    int
	BackgroundRecentMinPass   int
	LowVolumeFactor           float64
	MiddleNearBandwidthFactor float64
	ThinQuoteVolume24h        float64
	WatchTelegramThreshold    float64
	Confirm1TelegramThreshold float64
	Confirm2TelegramThreshold float64
	ConfluenceWindowMs        int64
	StageExpiryCandles        int
}

type BollPumpWatchResult struct {
	Triggered       bool
	Signal          model.BollPumpSignal
	BackgroundScore float64
	Reasons         []string
}
