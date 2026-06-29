package service

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
