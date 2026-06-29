package service

import (
	"fmt"
	"math"
	"strings"

	"coinmark/api-go/internal/model"
)

func DefaultBollPumpConfig() BollPumpConfig {
	return BollPumpConfig{
		Enabled:    true,
		Market:     "swap",
		Timeframes: []string{"1m", "3m", "5m", "15m", "30m", "1h"},
		BollPeriod: 20,
		BollStdDev: 2,
		ATRPeriod:  14,
		StartupWindows: map[string]int{
			"1m": 12, "3m": 10, "5m": 8, "15m": 6, "30m": 5, "1h": 4,
		},
		GainThresholds: map[string]float64{
			"1m": 0.020, "3m": 0.025, "5m": 0.030, "15m": 0.040, "30m": 0.050, "1h": 0.060,
		},
		VolumeThresholds: map[string]float64{
			"1m": 5.0, "3m": 3.0, "5m": 2.5, "15m": 2.0, "30m": 1.8, "1h": 1.5,
		},
		BackgroundLookback:        80,
		BackgroundRecentWindow:    10,
		BackgroundRecentMinPass:   7,
		LowVolumeFactor:           0.8,
		MiddleNearBandwidthFactor: 0.35,
		ThinQuoteVolume24h:        2_000_000,
		WatchTelegramThreshold:    70,
		Confirm1TelegramThreshold: 75,
		Confirm2TelegramThreshold: 80,
		ConfluenceWindowMs:        10 * 60 * 1000,
		StageExpiryCandles:        60,
	}
}

func EvaluateBollPumpWatch(market, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64, cfg BollPumpConfig) BollPumpWatchResult {
	if len(bars) == 0 || len(bars) != len(ind) {
		return BollPumpWatchResult{Reasons: []string{"missing bars or indicators"}}
	}
	window := bollPumpStartupWindow(timeframe, cfg)
	if window <= 0 || len(bars) < window+cfg.BollPeriod {
		return BollPumpWatchResult{Reasons: []string{"insufficient startup history"}}
	}
	latestIdx := len(bars) - 1
	startIdx := latestIdx - window + 1
	latest := bars[latestIdx]
	latestInd := ind[latestIdx]
	if !latest.Closed || !latestInd.ValidBoll {
		return BollPumpWatchResult{Reasons: []string{"latest candle is not ready"}}
	}
	if bollPumpLargeBearish(latest) || !bollPumpCloseStrong(latest) {
		return BollPumpWatchResult{Reasons: []string{"large bearish or weak close"}}
	}

	startClose := bars[startIdx].Close
	cumulativeGain := 0.0
	if startClose > 0 {
		cumulativeGain = latest.Close/startClose - 1
	}
	gainOK := cumulativeGain >= bollPumpGainThreshold(timeframe, cfg)
	volumeRatio, volumeOK := bollPumpWindowVolumeRatio(bars, startIdx, latestIdx, bollPumpVolumeThreshold(timeframe, cfg))
	middleOK := latest.Close >= latestInd.Middle
	upperOK := bollPumpUpperProximity(latest, latestInd)
	expandOK := latestIdx > 0 && ind[latestIdx-1].ValidBoll && latestInd.Bandwidth > ind[latestIdx-1].Bandwidth && latestInd.Middle >= ind[latestIdx-1].Middle && latest.Close >= latestInd.Middle

	reasons := make([]string, 0, 8)
	if !gainOK {
		reasons = append(reasons, "cumulative gain below threshold")
	}
	if !volumeOK {
		reasons = append(reasons, "volume ratio below threshold")
	}
	if !middleOK {
		reasons = append(reasons, "close below middle")
	}
	if !upperOK {
		reasons = append(reasons, "not near upper band")
	}
	if !expandOK {
		reasons = append(reasons, "boll not expanding upward")
	}
	if !gainOK || !volumeOK || !middleOK || !upperOK || !expandOK {
		return BollPumpWatchResult{Reasons: reasons}
	}

	backgroundScore, backgroundReasons := bollPumpBackgroundScore(bars, ind, startIdx, cfg)
	score := 55.0 + 10 + 10 + 10 + 5 + backgroundScore
	if quoteVolume24h > 0 && quoteVolume24h < cfg.ThinQuoteVolume24h {
		score -= 15
		reasons = append(reasons, "thin 24h quote volume")
	}
	reasons = append(reasons, "volume-backed pump", fmt.Sprintf("cumulative gain %.2f%%", cumulativeGain*100))
	reasons = append(reasons, backgroundReasons...)
	score = bollPumpScoreCap(score)
	return BollPumpWatchResult{
		Triggered:       true,
		BackgroundScore: backgroundScore,
		Reasons:         reasons,
		Signal: model.BollPumpSignal{
			Market:         normalizeBollPumpMarket(market),
			Symbol:         strings.ToUpper(strings.TrimSpace(symbol)),
			Timeframe:      timeframe,
			SignalLevel:    string(BollPumpLevelWatch),
			Price:          latest.Close,
			VolumeRatio:    volumeRatio,
			BollBandwidth:  latestInd.Bandwidth,
			BounceCount:    0,
			Score:          score,
			PriorityScore:  score,
			SignalTimeMs:   latest.CloseTimeMs,
			CandleStartMs:  latest.OpenTimeMs,
			QuoteVolume24h: quoteVolume24h,
			Reason:         strings.Join(reasons, ", "),
			Details:        model.JSONB(`{}`),
		},
	}
}

func bollPumpStartupWindow(timeframe string, cfg BollPumpConfig) int {
	if v, ok := cfg.StartupWindows[timeframe]; ok {
		return v
	}
	return 6
}

func bollPumpGainThreshold(timeframe string, cfg BollPumpConfig) float64 {
	if v, ok := cfg.GainThresholds[timeframe]; ok {
		return v
	}
	return 0.04
}

func bollPumpVolumeThreshold(timeframe string, cfg BollPumpConfig) float64 {
	if v, ok := cfg.VolumeThresholds[timeframe]; ok {
		return v
	}
	return 2
}

func bollPumpScoreCap(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func bollPumpCloseStrong(b BollPumpBar) bool {
	if b.Close >= b.Open {
		return true
	}
	rng := b.High - b.Low
	if rng <= 0 {
		return false
	}
	return (b.Close-b.Low)/rng >= 0.60
}

func bollPumpLargeBearish(b BollPumpBar) bool {
	rng := b.High - b.Low
	if rng <= 0 {
		return false
	}
	return b.Close < b.Open && math.Abs(b.Close-b.Open) >= 0.6*rng
}

func bollPumpUpperProximity(b BollPumpBar, in BollPumpIndicator) bool {
	if !in.ValidBoll {
		return false
	}
	width := in.Upper - in.Lower
	return b.Close >= in.Upper-0.15*width || b.High >= in.Upper
}

func bollPumpBackgroundScore(bars []BollPumpBar, ind []BollPumpIndicator, startupStart int, cfg BollPumpConfig) (float64, []string) {
	end := startupStart
	start := end - cfg.BackgroundLookback
	if start < 0 || end <= start || len(ind) != len(bars) {
		return 0, nil
	}
	score := 0.0
	reasons := make([]string, 0, 4)
	bwValues := make([]float64, 0, end-start)
	atrValues := make([]float64, 0, end-start)
	for i := start; i < end; i++ {
		if ind[i].ValidBoll {
			bwValues = append(bwValues, ind[i].Bandwidth)
		}
		if ind[i].ValidATR {
			atrValues = append(atrValues, ind[i].ATRRatio)
		}
	}
	prevIdx := end - 1
	if ind[prevIdx].ValidBoll && ind[prevIdx].Bandwidth <= bollPumpPercentile(bwValues, 0.30) {
		score += 5
		reasons = append(reasons, "quiet bandwidth")
	}
	recentStart := end - cfg.BackgroundRecentWindow
	if recentStart < start {
		recentStart = start
	}
	if bollPumpRecentLowVolume(bars, recentStart, end, cfg) {
		score += 5
		reasons = append(reasons, "quiet volume")
	}
	if bollPumpRecentMiddleNear(bars, ind, recentStart, end, cfg) {
		score += 5
		reasons = append(reasons, "middle consolidation")
	}
	if ind[prevIdx].ValidATR && ind[prevIdx].ATRRatio <= bollPumpPercentile(atrValues, 0.40) {
		score += 5
		reasons = append(reasons, "quiet ATR")
	}
	return score, reasons
}

func bollPumpWindowVolumeRatio(bars []BollPumpBar, startIdx, endIdx int, threshold float64) (float64, bool) {
	best := 0.0
	ok := false
	for i := startIdx; i <= endIdx; i++ {
		avg := bollPumpAverageVolumeBefore(bars, i, 20)
		if avg <= 0 {
			continue
		}
		ratio := bars[i].Volume / avg
		if ratio > best {
			best = ratio
		}
		if ratio >= threshold {
			ok = true
		}
	}
	return best, ok
}

func bollPumpAverageVolumeBefore(bars []BollPumpBar, idx int, window int) float64 {
	start := idx - window
	if start < 0 {
		return 0
	}
	sum := 0.0
	for i := start; i < idx; i++ {
		sum += bars[i].Volume
	}
	return sum / float64(window)
}

func bollPumpRecentLowVolume(bars []BollPumpBar, start, end int, cfg BollPumpConfig) bool {
	pass := 0
	total := 0
	for i := start; i < end; i++ {
		avg := bollPumpAverageVolumeBefore(bars, i, 20)
		if avg <= 0 {
			continue
		}
		total++
		if bars[i].Volume < avg*cfg.LowVolumeFactor {
			pass++
		}
	}
	return total > 0 && pass >= cfg.BackgroundRecentMinPass
}

func bollPumpRecentMiddleNear(bars []BollPumpBar, ind []BollPumpIndicator, start, end int, cfg BollPumpConfig) bool {
	pass := 0
	total := 0
	for i := start; i < end; i++ {
		if !ind[i].ValidBoll || ind[i].Middle == 0 {
			continue
		}
		total++
		dist := math.Abs(bars[i].Close-ind[i].Middle) / ind[i].Middle
		if dist <= cfg.MiddleNearBandwidthFactor*ind[i].Bandwidth {
			pass++
		}
	}
	return total > 0 && pass >= cfg.BackgroundRecentMinPass
}

func normalizeBollPumpMarket(market string) string {
	m := normalizeMarket(market)
	if m == "" {
		return "swap"
	}
	return m
}
