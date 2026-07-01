package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"coinmark/api-go/internal/model"
)

const (
	keyKNearLineThresholdPct = 0.012
	keyKClearBounceThreshold = 0.68
)

type BollPumpKeyK4HResult struct {
	Triggered bool
	Signal    model.BollPumpSignal
	Reasons   []string
}

type bollPumpKeyKFeature struct {
	BodyPct                    float64 `json:"body_pct"`
	RangePct                   float64 `json:"range_pct"`
	LowerWickRatio             float64 `json:"lower_wick_ratio"`
	UpperWickRatio             float64 `json:"upper_wick_ratio"`
	ClosePositionInRange       float64 `json:"close_position_in_range"`
	VolumeRatio20              float64 `json:"volume_ratio_20"`
	BollWidthPct               float64 `json:"boll_width_pct"`
	EMA10                      float64 `json:"ema10"`
	LowToBBLowerPct            float64 `json:"low_to_bb_lower_pct"`
	LowToBBMidPct              float64 `json:"low_to_bb_mid_pct"`
	LowToEMA10Pct              float64 `json:"low_to_ema10_pct"`
	NearLineCount5             float64 `json:"near_line_count_5"`
	DirectionFlip5             float64 `json:"direction_flip_5"`
	NetReturn5                 float64 `json:"net_return_5"`
	LineTouchScore             float64 `json:"line_touch_score"`
	BodyRescueScore            float64 `json:"body_rescue_score"`
	BodyKeyKScore              float64 `json:"body_key_k_score"`
	RescueShapeScore           float64 `json:"rescue_shape_score"`
	VolumePushScore            float64 `json:"volume_push_score"`
	ClearBounceScore           float64 `json:"clear_bounce_score"`
	StickyScore                float64 `json:"sticky_score"`
	KeyKScore                  float64 `json:"key_k_score"`
	ProbeLevel                 string  `json:"probe_level"`
	SuggestedEvent             string  `json:"suggested_event"`
	EffectiveClearBounceReason string  `json:"effective_clear_bounce_reason"`
}

func EvaluateBollPumpKeyK4H(market, symbol string, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64, cfg BollPumpConfig) BollPumpKeyK4HResult {
	cfg = NormalizeBollPumpConfig(cfg)
	if !cfg.KeyK4HEnabled {
		return BollPumpKeyK4HResult{Reasons: []string{"4h key K disabled"}}
	}
	if len(bars) == 0 || len(bars) != len(ind) {
		return BollPumpKeyK4HResult{Reasons: []string{"missing bars or indicators"}}
	}
	if len(bars) < cfg.BollPeriod+20 {
		return BollPumpKeyK4HResult{Reasons: []string{"insufficient 4h history"}}
	}
	features := computeBollPumpKeyKFeatures(bars, ind)
	latestIdx := len(bars) - 1
	latest := bars[latestIdx]
	latestInd := ind[latestIdx]
	latestFeature := features[latestIdx]
	if !latest.Closed || !latestInd.ValidBoll || latest.Close <= 0 {
		return BollPumpKeyK4HResult{Reasons: []string{"latest 4h candle is not ready"}}
	}

	reasons := make([]string, 0, 8)
	if latestFeature.KeyKScore < cfg.KeyK4HThreshold {
		reasons = append(reasons, fmt.Sprintf("key_k_score %.2f below %.2f", latestFeature.KeyKScore, cfg.KeyK4HThreshold))
	}
	if latestFeature.VolumeRatio20 < cfg.KeyK4HMinVolumeRatio {
		reasons = append(reasons, fmt.Sprintf("volume ratio %.2fx below %.2fx", latestFeature.VolumeRatio20, cfg.KeyK4HMinVolumeRatio))
	}
	if latestFeature.BodyPct < cfg.KeyK4HMinBodyPct {
		reasons = append(reasons, fmt.Sprintf("body %.2f%% below %.2f%%", latestFeature.BodyPct*100, cfg.KeyK4HMinBodyPct*100))
	}
	if latestFeature.StickyScore > cfg.KeyK4HMaxStickyScore {
		reasons = append(reasons, fmt.Sprintf("sticky %.2f above %.2f", latestFeature.StickyScore, cfg.KeyK4HMaxStickyScore))
	}
	if len(reasons) > 0 {
		return BollPumpKeyK4HResult{Reasons: reasons}
	}

	details := bollPumpKeyKDetails(latestFeature)
	reasons = append(reasons,
		"4h key K confirmed",
		fmt.Sprintf("key_k_score %.2f", latestFeature.KeyKScore),
		fmt.Sprintf("clear_bounce %.2f", latestFeature.ClearBounceScore),
		fmt.Sprintf("body %.2f%% body_score %.2f", latestFeature.BodyPct*100, latestFeature.BodyKeyKScore),
		fmt.Sprintf("volume_score %.2f", latestFeature.VolumePushScore),
		fmt.Sprintf("sticky %.2f", latestFeature.StickyScore),
		fmt.Sprintf("probe %s", latestFeature.ProbeLevel),
	)
	score := bollPumpScoreFloor(latestFeature.KeyKScore * 100)
	return BollPumpKeyK4HResult{
		Triggered: true,
		Reasons:   reasons,
		Signal: model.BollPumpSignal{
			Market:         normalizeBollPumpMarket(market),
			Symbol:         strings.ToUpper(strings.TrimSpace(symbol)),
			Timeframe:      "4h",
			SignalLevel:    string(BollPumpLevelKeyK4H),
			Price:          latest.Close,
			VolumeRatio:    latestFeature.VolumeRatio20,
			BollBandwidth:  latestInd.Bandwidth,
			BounceCount:    0,
			Score:          score,
			PriorityScore:  score,
			SignalTimeMs:   latest.CloseTimeMs,
			CandleStartMs:  latest.OpenTimeMs,
			QuoteVolume24h: quoteVolume24h,
			Reason:         strings.Join(reasons, ", "),
			Details:        details,
		},
	}
}

func computeBollPumpKeyKFeatures(bars []BollPumpBar, ind []BollPumpIndicator) []bollPumpKeyKFeature {
	out := make([]bollPumpKeyKFeature, len(bars))
	ema := bollPumpEMA10(bars)
	nearAny := make([]bool, len(bars))
	flips := make([]float64, len(bars))
	for i, b := range bars {
		price := b.Close
		candleRange := b.High - b.Low
		if price <= 0 {
			continue
		}
		f := &out[i]
		if candleRange > 0 {
			f.LowerWickRatio = (math.Min(b.Open, b.Close) - b.Low) / candleRange
			f.UpperWickRatio = (b.High - math.Max(b.Open, b.Close)) / candleRange
			f.ClosePositionInRange = (b.Close - b.Low) / candleRange
		}
		f.BodyPct = (b.Close - b.Open) / price
		f.RangePct = candleRange / price
		f.VolumeRatio20 = bollPumpRollingVolumeRatio(bars, i, 20)
		f.EMA10 = ema[i]
		if ind[i].ValidBoll {
			f.BollWidthPct = (ind[i].Upper - ind[i].Lower) / price
			f.LowToBBLowerPct = (b.Low - ind[i].Lower) / price
			f.LowToBBMidPct = (b.Low - ind[i].Middle) / price
		}
		if ema[i] > 0 {
			f.LowToEMA10Pct = (b.Low - ema[i]) / price
		}
		nearLower := math.Abs(f.LowToBBLowerPct) <= 0.004 || f.LowToBBLowerPct < 0
		nearMid := math.Abs(f.LowToBBMidPct) <= 0.004
		nearEMA := math.Abs(f.LowToEMA10Pct) <= 0.004
		nearAny[i] = nearLower || nearMid || nearEMA
		if i >= 5 && bars[i-5].Close > 0 {
			f.NetReturn5 = b.Close/bars[i-5].Close - 1
		}
		if i > 0 {
			retNow := b.Close/bars[i-1].Close - 1
			retPrev := 0.0
			if i > 1 {
				retPrev = bars[i-1].Close/bars[i-2].Close - 1
			}
			if (retNow > 0) != (retPrev > 0) {
				flips[i] = 1
			}
		}
	}
	for i := range bars {
		out[i].NearLineCount5 = bollPumpRollingBoolCount(nearAny, i, 5)
		out[i].DirectionFlip5 = bollPumpRollingFloatSum(flips, i, 5)
		scoreBollPumpKeyKFeature(&out[i])
	}
	return out
}

func scoreBollPumpKeyKFeature(f *bollPumpKeyKFeature) {
	minDistance := math.Min(math.Abs(f.LowToBBLowerPct), math.Min(math.Abs(f.LowToBBMidPct), math.Abs(f.LowToEMA10Pct)))
	nearLineTouch := keyKClip01(1.0 - minDistance/keyKNearLineThresholdPct)
	downwardCollision := 0.0
	if f.LowToBBLowerPct <= 0 || f.LowToBBMidPct <= 0 || f.LowToEMA10Pct <= 0 {
		downwardCollision = 1.0
	}
	lineTouch := math.Max(nearLineTouch, downwardCollision)
	bodyRescueScore := keyKClip01(f.BodyPct / 0.015)
	bodyKeyKScore := keyKClip01(f.BodyPct / 0.012)
	rescueShape := keyKClip01(0.55*f.ClosePositionInRange + 0.30*f.LowerWickRatio + 0.15*bodyRescueScore)
	volumePush := keyKClip01((f.VolumeRatio20 - 0.8) / 1.4)
	f.LineTouchScore = lineTouch
	f.BodyRescueScore = bodyRescueScore
	f.BodyKeyKScore = bodyKeyKScore
	f.RescueShapeScore = rescueShape
	f.VolumePushScore = volumePush
	f.ClearBounceScore = keyKClip01(0.45*lineTouch + 0.40*rescueShape + 0.15*volumePush)
	f.StickyScore = keyKClip01(
		0.45*keyKClip01(f.NearLineCount5/4.0) +
			0.35*keyKClip01(f.DirectionFlip5/4.0) +
			0.20*keyKClip01(1.0-math.Abs(f.NetReturn5)/0.018),
	)
	f.KeyKScore = keyKClip01(
		0.50*f.ClearBounceScore +
			0.25*bodyKeyKScore +
			0.25*volumePush -
			0.20*f.StickyScore,
	)
	f.ProbeLevel = bollPumpKeyKProbeLevel(f)
	f.EffectiveClearBounceReason = bollPumpKeyKEffectiveBounceReason(f)
	f.SuggestedEvent = bollPumpKeyKSuggestedEvent(f)
}

func bollPumpKeyKProbeLevel(f *bollPumpKeyKFeature) string {
	level := "level_2"
	distance := math.Abs(f.LowToBBLowerPct)
	if d := math.Abs(f.LowToBBMidPct); d < distance {
		level, distance = "level_3", d
	}
	if d := math.Abs(f.LowToEMA10Pct); d < distance {
		level, distance = "level_4", d
	}
	if distance > keyKNearLineThresholdPct {
		return "pending"
	}
	return level
}

func bollPumpKeyKEffectiveBounceReason(f *bollPumpKeyKFeature) string {
	if f.ProbeLevel == "pending" {
		return "probe_pending"
	}
	if !bollPumpKeyKHasDownwardCollision(f) {
		return "no_downward_collision"
	}
	if f.ClearBounceScore < keyKClearBounceThreshold {
		return "clear_bounce_below_threshold"
	}
	if f.StickyScore >= 0.70 {
		return "sticky_too_high"
	}
	return "effective"
}

func bollPumpKeyKHasDownwardCollision(f *bollPumpKeyKFeature) bool {
	switch f.ProbeLevel {
	case "level_2":
		return f.LowToBBLowerPct <= 0
	case "level_3":
		return f.LowToBBMidPct <= 0
	case "level_4":
		return f.LowToEMA10Pct <= 0
	default:
		return false
	}
}

func bollPumpKeyKSuggestedEvent(f *bollPumpKeyKFeature) string {
	if f.StickyScore >= 0.7 && f.KeyKScore < 0.65 {
		return "sticky"
	}
	if f.KeyKScore >= 0.72 {
		return "key_k"
	}
	if f.ClearBounceScore >= 0.68 {
		return "clear_bounce"
	}
	if f.ProbeLevel == "pending" {
		return "pending"
	}
	return "neutral"
}

func bollPumpEMA10(bars []BollPumpBar) []float64 {
	out := make([]float64, len(bars))
	if len(bars) == 0 {
		return out
	}
	alpha := 2.0 / 11.0
	out[0] = bars[0].Close
	for i := 1; i < len(bars); i++ {
		out[i] = bars[i].Close*alpha + out[i-1]*(1-alpha)
	}
	return out
}

func bollPumpRollingVolumeRatio(bars []BollPumpBar, idx, window int) float64 {
	if idx < 0 || window <= 0 || idx+1 < window {
		return 0
	}
	start := idx + 1 - window
	sum := 0.0
	for i := start; i <= idx; i++ {
		sum += bars[i].Volume
	}
	avg := sum / float64(window)
	if avg <= 0 {
		return 0
	}
	return bars[idx].Volume / avg
}

func bollPumpRollingBoolCount(values []bool, idx, window int) float64 {
	if idx < 0 || window <= 0 {
		return 0
	}
	start := idx + 1 - window
	if start < 0 {
		start = 0
	}
	count := 0.0
	for i := start; i <= idx; i++ {
		if values[i] {
			count++
		}
	}
	return count
}

func bollPumpRollingFloatSum(values []float64, idx, window int) float64 {
	if idx < 0 || window <= 0 {
		return 0
	}
	start := idx + 1 - window
	if start < 0 {
		start = 0
	}
	sum := 0.0
	for i := start; i <= idx; i++ {
		sum += values[i]
	}
	return sum
}

func bollPumpKeyKDetails(feature bollPumpKeyKFeature) model.JSONB {
	raw, err := json.Marshal(feature)
	if err != nil {
		return model.JSONB(`{}`)
	}
	return model.JSONB(raw)
}

func keyKClip01(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
