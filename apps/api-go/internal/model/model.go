package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB handles SQLite TEXT ↔ Go []byte for JSON columns.
type JSONB json.RawMessage

func (j *JSONB) Scan(src interface{}) error {
	switch v := src.(type) {
	case string:
		*j = JSONB(v)
	case []byte:
		*j = append(JSONB{}, v...)
	case nil:
		*j = JSONB("null")
	default:
		return fmt.Errorf("JSONB.Scan: unsupported type %T", src)
	}
	return nil
}

func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return "null", nil
	}
	return string(j), nil
}

func (j JSONB) MarshalJSON() ([]byte, error) {
	if j == nil {
		return []byte("null"), nil
	}
	return []byte(j), nil
}

func (j *JSONB) UnmarshalJSON(data []byte) error {
	*j = append(JSONB{}, data...)
	return nil
}

// ---------------------------------------------------------------------------
// SQLite models (mirrors Python models.py)
// ---------------------------------------------------------------------------

type TradeBucket struct {
	ID                int64    `db:"id" json:"-"`
	Market            string   `db:"market" json:"market"`
	Symbol            string   `db:"symbol" json:"symbol"`
	Bucket            string   `db:"bucket" json:"bucket"`
	BucketStartMs     int64    `db:"bucket_start_ms" json:"bucket_start_ms"`
	TakerBuyNotional  float64  `db:"taker_buy_notional" json:"taker_buy_notional"`
	TakerSellNotional float64  `db:"taker_sell_notional" json:"taker_sell_notional"`
	QuoteNotional     float64  `db:"quote_notional" json:"quote_notional"`
	TradeCount        int      `db:"trade_count" json:"trade_count"`
	FirstTradeMs      *int64   `db:"first_trade_ms" json:"first_trade_ms"`
	LastTradeMs       *int64   `db:"last_trade_ms" json:"last_trade_ms"`
	OpenPrice         *float64 `db:"open_price" json:"open_price"`
	ClosePrice        *float64 `db:"close_price" json:"close_price"`
	HighPrice         *float64 `db:"high_price" json:"high_price"`
	LowPrice          *float64 `db:"low_price" json:"low_price"`
}

type OrderbookFeatureBucket struct {
	ID                    int64   `db:"id" json:"-"`
	Market                string  `db:"market" json:"market"`
	Symbol                string  `db:"symbol" json:"symbol"`
	Bucket                string  `db:"bucket" json:"bucket"`
	BucketStartMs         int64   `db:"bucket_start_ms" json:"bucket_start_ms"`
	SpreadBpsSum          float64 `db:"spread_bps_sum" json:"spread_bps_sum"`
	MicropriceShiftBpsSum float64 `db:"microprice_shift_bps_sum" json:"microprice_shift_bps_sum"`
	DepthImbalanceL20Sum  float64 `db:"depth_imbalance_l20_sum" json:"depth_imbalance_l20_sum"`
	WallPressureL20Sum    float64 `db:"wall_pressure_l20_sum" json:"wall_pressure_l20_sum"`
	SampleCount           int     `db:"sample_count" json:"sample_count"`
	TakerBuyNotional      float64 `db:"taker_buy_notional" json:"taker_buy_notional"`
	TakerSellNotional     float64 `db:"taker_sell_notional" json:"taker_sell_notional"`
	DepletionEvents       int     `db:"depletion_events" json:"depletion_events"`
	ReplenishmentEvents   int     `db:"replenishment_events" json:"replenishment_events"`
}

type FundingRateSnapshot struct {
	ID              int64   `db:"id" json:"-"`
	Symbol          string  `db:"symbol" json:"symbol"`
	LastFundingRate float64 `db:"last_funding_rate" json:"last_funding_rate"`
	MarkPrice       float64 `db:"mark_price" json:"mark_price"`
	EventTimeMs     int64   `db:"event_time_ms" json:"event_time_ms"`
}

type OpenInterestSnapshot struct {
	ID            int64   `db:"id" json:"-"`
	Symbol        string  `db:"symbol" json:"symbol"`
	OpenInterest  float64 `db:"open_interest" json:"open_interest"`
	MarkPrice     float64 `db:"mark_price" json:"mark_price"`
	OINotionalUSD float64 `db:"oi_notional_usd" json:"oi_notional_usd"`
	EventTimeMs   int64   `db:"event_time_ms" json:"event_time_ms"`
}

type AssetMarketCap struct {
	ID                int64   `db:"id" json:"-"`
	Asset             string  `db:"asset" json:"asset"`
	PriceUSD          float64 `db:"price_usd" json:"price_usd"`
	CirculatingSupply float64 `db:"circulating_supply" json:"circulating_supply"`
	MarketCapUSD      float64 `db:"market_cap_usd" json:"market_cap_usd"`
	Source            string  `db:"source" json:"source"`
	EventTimeMs       int64   `db:"event_time_ms" json:"event_time_ms"`
}

type Favorite struct {
	ID       int64  `db:"id" json:"-"`
	ClientID string `db:"client_id" json:"client_id"`
	Market   string `db:"market" json:"market"`
	Symbol   string `db:"symbol" json:"symbol"`
}

type CoinInfo struct {
	ID          int64    `db:"id" json:"-"`
	Symbol      string   `db:"symbol" json:"symbol"`
	WhaleMinVal *float64 `db:"whale_min_val" json:"whale_min_val"`
	CreatedAt   *string  `db:"created_at" json:"-"`
	UpdatedAt   *string  `db:"updated_at" json:"-"`
}

type SRLevel struct {
	ID            int64   `db:"id" json:"-"`
	Market        string  `db:"market" json:"market"`
	Symbol        string  `db:"symbol" json:"symbol"`
	LevelPrice    float64 `db:"level_price" json:"level_price"`
	Timeframe     string  `db:"timeframe" json:"timeframe"`
	Touches       int     `db:"touches" json:"touches"`
	StrengthScore float64 `db:"strength_score" json:"strength_score"`
	LastTouchMs   int64   `db:"last_touch_ms" json:"last_touch_ms"`
	CreatedAt     *string `db:"created_at" json:"-"`
	UpdatedAt     *string `db:"updated_at" json:"-"`
}

type AnomalyEvent struct {
	ID          int64   `db:"id" json:"id"`
	Market      string  `db:"market" json:"market"`
	Symbol      string  `db:"symbol" json:"symbol"`
	EventType   string  `db:"event_type" json:"event_type"`
	TfSignal    string  `db:"tf_signal" json:"tf_signal"`
	TfLevel     *string `db:"tf_level" json:"tf_level"`
	EventTimeMs int64   `db:"event_time_ms" json:"event_time_ms"`
	Title       string  `db:"title" json:"title"`
	Details     JSONB   `db:"details" json:"details"`
	CreatedAt   *string `db:"created_at" json:"-"`
}

type AbsorptionSignalSnapshot struct {
	ID                int64    `db:"id" json:"-"`
	Market            string   `db:"market" json:"market"`
	Symbol            string   `db:"symbol" json:"symbol"`
	BucketStartMs     int64    `db:"bucket_start_ms" json:"bucket_start_ms"`
	Direction         string   `db:"direction" json:"direction"`
	SignalState       string   `db:"signal_state" json:"signal_state"`
	Score             float64  `db:"score" json:"score"`
	NetFlowStrength   *float64 `db:"net_flow_strength" json:"net_flow_strength"`
	ImpactPerNotional *float64 `db:"impact_per_notional" json:"impact_per_notional"`
	Window4hPassed    bool     `db:"window_4h_passed" json:"window_4h_passed"`
	Window1dPassed    bool     `db:"window_1d_passed" json:"window_1d_passed"`
	Window3dPassed    bool     `db:"window_3d_passed" json:"window_3d_passed"`
	Windows           JSONB    `db:"windows" json:"windows"`
	Reasons           JSONB    `db:"reasons" json:"reasons"`
	CreatedAt         *string  `db:"created_at" json:"-"`
	UpdatedAt         *string  `db:"updated_at" json:"-"`
}

type BollPumpState struct {
	ID                      int64    `db:"id" json:"id"`
	Market                  string   `db:"market" json:"market"`
	Symbol                  string   `db:"symbol" json:"symbol"`
	Timeframe               string   `db:"timeframe" json:"timeframe"`
	Status                  string   `db:"status" json:"status"`
	WatchStartedMs          *int64   `db:"watch_started_ms" json:"watch_started_ms"`
	WatchCandleStartMs      *int64   `db:"watch_candle_start_ms" json:"watch_candle_start_ms"`
	WatchScore              float64  `db:"watch_score" json:"watch_score"`
	CurrentScore            float64  `db:"current_score" json:"current_score"`
	ConfluenceScore         float64  `db:"confluence_score" json:"confluence_score"`
	PriorityScore           float64  `db:"priority_score" json:"priority_score"`
	BounceCount             int      `db:"bounce_count" json:"bounce_count"`
	FirstPullbackLow        *float64 `db:"first_pullback_low" json:"first_pullback_low"`
	SecondPullbackLow       *float64 `db:"second_pullback_low" json:"second_pullback_low"`
	PendingPullbackCandleMs *int64   `db:"pending_pullback_candle_ms" json:"pending_pullback_candle_ms"`
	PendingPullbackHigh     *float64 `db:"pending_pullback_high" json:"pending_pullback_high"`
	LastCheckedCandleMs     *int64   `db:"last_checked_candle_ms" json:"last_checked_candle_ms"`
	LastSignalLevel         *string  `db:"last_signal_level" json:"last_signal_level"`
	LastAlertMs             *int64   `db:"last_alert_ms" json:"last_alert_ms"`
	ExpiresAtCandleMs       *int64   `db:"expires_at_candle_ms" json:"expires_at_candle_ms"`
	Details                 JSONB    `db:"details" json:"details"`
	CreatedAt               *string  `db:"created_at" json:"-"`
	UpdatedAt               *string  `db:"updated_at" json:"-"`
}

type BollPumpSignal struct {
	ID                    int64    `db:"id" json:"id"`
	Market                string   `db:"market" json:"market"`
	Symbol                string   `db:"symbol" json:"symbol"`
	Timeframe             string   `db:"timeframe" json:"timeframe"`
	SignalLevel           string   `db:"signal_level" json:"signal_level"`
	Price                 float64  `db:"price" json:"price"`
	VolumeRatio           float64  `db:"volume_ratio" json:"volume_ratio"`
	BollBandwidth         float64  `db:"boll_bandwidth" json:"boll_bandwidth"`
	BounceCount           int      `db:"bounce_count" json:"bounce_count"`
	Score                 float64  `db:"score" json:"score"`
	ConfluenceScore       float64  `db:"confluence_score" json:"confluence_score"`
	PriorityScore         float64  `db:"priority_score" json:"priority_score"`
	SignalTimeMs          int64    `db:"signal_time_ms" json:"signal_time_ms"`
	CandleStartMs         int64    `db:"candle_start_ms" json:"candle_start_ms"`
	WatchCandleStartMs    *int64   `db:"watch_candle_start_ms" json:"watch_candle_start_ms"`
	PullbackCandleStartMs *int64   `db:"pullback_candle_start_ms" json:"pullback_candle_start_ms"`
	QuoteVolume24h        float64  `db:"quote_volume_24h" json:"quote_volume_24h"`
	Perf1hMaxGain         *float64 `db:"perf_1h_max_gain" json:"perf_1h_max_gain"`
	Perf1hMaxDrawdown     *float64 `db:"perf_1h_max_drawdown" json:"perf_1h_max_drawdown"`
	Perf1hCloseReturn     *float64 `db:"perf_1h_close_return" json:"perf_1h_close_return"`
	Perf4hMaxGain         *float64 `db:"perf_4h_max_gain" json:"perf_4h_max_gain"`
	Perf4hMaxDrawdown     *float64 `db:"perf_4h_max_drawdown" json:"perf_4h_max_drawdown"`
	Perf4hCloseReturn     *float64 `db:"perf_4h_close_return" json:"perf_4h_close_return"`
	Perf24hMaxGain        *float64 `db:"perf_24h_max_gain" json:"perf_24h_max_gain"`
	Perf24hMaxDrawdown    *float64 `db:"perf_24h_max_drawdown" json:"perf_24h_max_drawdown"`
	Perf24hCloseReturn    *float64 `db:"perf_24h_close_return" json:"perf_24h_close_return"`
	PerformanceUpdatedMs  *int64   `db:"performance_updated_ms" json:"performance_updated_ms"`
	Reason                string   `db:"reason" json:"reason"`
	Details               JSONB    `db:"details" json:"details"`
	CreatedAt             *string  `db:"created_at" json:"-"`
}

type OrderbookHeatmapSnapshot struct {
	ID            int64   `db:"id" json:"-"`
	Market        string  `db:"market" json:"market"`
	Symbol        string  `db:"symbol" json:"symbol"`
	BucketStartMs int64   `db:"bucket_start_ms" json:"bucket_start_ms"`
	Side          string  `db:"side" json:"side"`
	PriceBin      float64 `db:"price_bin" json:"price_bin"`
	PriceStep     float64 `db:"price_step" json:"price_step"`
	Intensity     float64 `db:"intensity" json:"intensity"`
	LevelCount    int     `db:"level_count" json:"level_count"`
	CreatedAt     *string `db:"created_at" json:"-"`
	UpdatedAt     *string `db:"updated_at" json:"-"`
}

// ---------------------------------------------------------------------------
// ClickHouse row types (lightweight, for query results)
// ---------------------------------------------------------------------------

type CHTradeRow struct {
	Market            string   `json:"market"`
	Symbol            string   `json:"symbol"`
	Bucket            string   `json:"bucket"`
	BucketStartMs     int64    `json:"bucket_start_ms"`
	TakerBuyNotional  float64  `json:"taker_buy_notional"`
	TakerSellNotional float64  `json:"taker_sell_notional"`
	QuoteNotional     float64  `json:"quote_notional"`
	TradeCount        int64    `json:"trade_count"`
	FirstTradeMs      *int64   `json:"first_trade_ms"`
	LastTradeMs       *int64   `json:"last_trade_ms"`
	OpenPrice         *float64 `json:"open_price"`
	ClosePrice        *float64 `json:"close_price"`
	HighPrice         *float64 `json:"high_price"`
	LowPrice          *float64 `json:"low_price"`
}

type CHOBFeatureRow struct {
	Market                string  `json:"market"`
	Symbol                string  `json:"symbol"`
	Bucket                string  `json:"bucket"`
	BucketStartMs         int64   `json:"bucket_start_ms"`
	SpreadBpsSum          float64 `json:"spread_bps_sum"`
	MicropriceShiftBpsSum float64 `json:"microprice_shift_bps_sum"`
	DepthImbalanceL20Sum  float64 `json:"depth_imbalance_l20_sum"`
	WallPressureL20Sum    float64 `json:"wall_pressure_l20_sum"`
	SampleCount           int     `json:"sample_count"`
	TakerBuyNotional      float64 `json:"taker_buy_notional"`
	TakerSellNotional     float64 `json:"taker_sell_notional"`
	DepletionEvents       int     `json:"depletion_events"`
	ReplenishmentEvents   int     `json:"replenishment_events"`
}

type CHFundingRow struct {
	Symbol          string  `json:"symbol"`
	LastFundingRate float64 `json:"last_funding_rate"`
	MarkPrice       float64 `json:"mark_price"`
	EventTimeMs     int64   `json:"event_time_ms"`
}

type CHOIRow struct {
	Symbol        string  `json:"symbol"`
	OpenInterest  float64 `json:"open_interest"`
	MarkPrice     float64 `json:"mark_price"`
	OINotionalUSD float64 `json:"oi_notional_usd"`
	EventTimeMs   int64   `json:"event_time_ms"`
}

type CHMarketCapRow struct {
	Asset             string  `json:"asset"`
	PriceUSD          float64 `json:"price_usd"`
	CirculatingSupply float64 `json:"circulating_supply"`
	MarketCapUSD      float64 `json:"market_cap_usd"`
	Source            string  `json:"source"`
	EventTimeMs       int64   `json:"event_time_ms"`
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

var BucketMs = map[string]int64{
	"1m":  60_000,
	"15m": 900_000,
	"1h":  3_600_000,
	"4h":  14_400_000,
	"1d":  86_400_000,
}
