package service

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"coinmark/api-go/internal/model"
)

func DefaultBollPumpConfig() BollPumpConfig {
	return BollPumpConfig{
		Enabled:        true,
		Market:         "swap",
		Timeframes:     []string{"1m", "3m", "5m", "15m", "30m", "1h"},
		SymbolLimit:    200,
		ScanTimeoutSec: 45,
		BollPeriod:     20,
		BollStdDev:     2,
		ATRPeriod:      14,
		StartupWindows: map[string]int{
			"1m": 12, "3m": 10, "5m": 8, "15m": 6, "30m": 5, "1h": 4,
		},
		GainThresholds: map[string]float64{
			"1m": 0.020, "3m": 0.025, "5m": 0.030, "15m": 0.040, "30m": 0.050, "1h": 0.060,
		},
		VolumeThresholds: map[string]float64{
			"1m": 5.0, "3m": 3.0, "5m": 2.5, "15m": 2.0, "30m": 1.8, "1h": 1.5,
		},
		BackgroundLookback:            80,
		BackgroundRecentWindow:        10,
		BackgroundRecentMinPass:       7,
		LowVolumeFactor:               0.8,
		MiddleNearBandwidthFactor:     0.35,
		ThinQuoteVolume24h:            2_000_000,
		WatchTrendCheckCandles:        6,
		WatchTrendMaxDrawdownPct:      0.01,
		WatchTrendMaxDrawdownATR:      0.75,
		TrendCleanBonus:               10,
		TrendWickPenalty:              -25,
		TrendWeakPenalty:              0,
		TrendWickBodyMaxRatio:         0.35,
		TrendEfficiencyMin:            0.30,
		MinimumTrendTimeframe:         "15m",
		MinimumTrendCheckCandles:      8,
		MinimumTrendGainPct:           0.01,
		MinimumTrendEfficiencyMin:     0.45,
		MinimumTrendRisingRatio:       0.60,
		ResistanceLookback:            60,
		ResistanceSwingSpan:           2,
		ResistanceClusterATR:          0.5,
		ResistanceClusterPct:          0.008,
		ResistanceBreakoutBufferPct:   0.003,
		ResistanceMaxDistancePct:      0.04,
		ResistanceMinTouches:          2,
		ResistanceBreakoutScore:       10,
		Resistance4HLookback:          60,
		Resistance4HSwingSpan:         2,
		Resistance4HClusterATR:        0.5,
		Resistance4HClusterPct:        0.008,
		Resistance4HBreakoutBufferPct: 0.003,
		Resistance4HMaxDistancePct:    0.04,
		Resistance4HMinTouches:        2,
		Resistance4HBreakoutBonus:     15,
		WatchTelegramThreshold:        70,
		Confirm1TelegramThreshold:     75,
		Confirm2TelegramThreshold:     80,
		ConfluenceWindowMs:            10 * 60 * 1000,
		StageExpiryCandles:            60,
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
	resistanceBreakout := bollPumpCurrentTimeframeResistanceBreakout(timeframe, bars, cfg)
	resistanceOK := resistanceBreakout.Triggered

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
	if !resistanceOK {
		reasons = append(reasons, "missing current timeframe resistance breakout")
	}
	if !gainOK || !volumeOK || !middleOK || !upperOK || !expandOK || !resistanceOK {
		return BollPumpWatchResult{Reasons: reasons}
	}

	backgroundScore, backgroundReasons := bollPumpBackgroundScore(bars, ind, startIdx, cfg)
	trendScore, trendReasons := bollPumpStartupTrendScore(bars, startIdx, latestIdx, cfg)
	score := 55.0 + 10 + 10 + 10 + cfg.ResistanceBreakoutScore + 5 + backgroundScore + trendScore
	if quoteVolume24h > 0 && quoteVolume24h < cfg.ThinQuoteVolume24h {
		score -= 15
		reasons = append(reasons, "thin 24h quote volume")
	}
	reasons = append(reasons, "volume-backed pump", fmt.Sprintf("cumulative gain %.2f%%", cumulativeGain*100))
	reasons = append(reasons, resistanceBreakout.Reason)
	reasons = append(reasons, backgroundReasons...)
	if trendScore != 0 {
		reasons = append(reasons, fmt.Sprintf("trend score %.0f", trendScore))
	}
	reasons = append(reasons, trendReasons...)
	score = bollPumpScoreFloor(score)
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

func bollPumpStartupTrendScore(bars []BollPumpBar, startIdx, endIdx int, cfg BollPumpConfig) (float64, []string) {
	if startIdx < 0 || endIdx <= startIdx || endIdx >= len(bars) {
		return 0, nil
	}
	total := endIdx - startIdx + 1
	path := 0.0
	rising := 0
	wickHeavy := 0
	for i := startIdx; i <= endIdx; i++ {
		b := bars[i]
		rng := b.High - b.Low
		if rng > 0 {
			bodyRatio := math.Abs(b.Close-b.Open) / rng
			if bodyRatio <= cfg.TrendWickBodyMaxRatio {
				wickHeavy++
			}
		}
		if i > startIdx {
			delta := b.Close - bars[i-1].Close
			path += math.Abs(delta)
			if delta > 0 {
				rising++
			}
		}
	}
	net := bars[endIdx].Close - bars[startIdx].Close
	efficiency := 0.0
	if path > 0 && net > 0 {
		efficiency = net / path
	}

	score := 0.0
	reasons := make([]string, 0, 3)
	if wickHeavy*2 >= total {
		score += cfg.TrendWickPenalty
		reasons = append(reasons, fmt.Sprintf("wick-heavy startup %d/%d", wickHeavy, total))
	}
	if efficiency >= 0.55 && rising*2 >= total-1 && wickHeavy*2 < total {
		score += cfg.TrendCleanBonus
		reasons = append(reasons, fmt.Sprintf("clean trend %.2f", efficiency))
	}
	return score, reasons
}

type bollPumpMinimumTrendGateResult struct {
	Pass   bool
	Reason string
}

func bollPumpMinimumTrendGate(bars []BollPumpBar, cfg BollPumpConfig) bollPumpMinimumTrendGateResult {
	cfg = NormalizeBollPumpConfig(cfg)
	closed := make([]BollPumpBar, 0, len(bars))
	for _, b := range bars {
		if b.Closed && b.Close > 0 {
			closed = append(closed, b)
		}
	}
	n := cfg.MinimumTrendCheckCandles
	if len(closed) < n {
		return bollPumpMinimumTrendGateResult{Reason: fmt.Sprintf("%s trend insufficient candles %d/%d", cfg.MinimumTrendTimeframe, len(closed), n)}
	}
	start := len(closed) - n
	window := closed[start:]
	first := window[0].Close
	last := window[len(window)-1].Close
	if first <= 0 {
		return bollPumpMinimumTrendGateResult{Reason: fmt.Sprintf("%s trend invalid price", cfg.MinimumTrendTimeframe)}
	}

	path := 0.0
	rising := 0
	wickHeavy := 0
	for i := range window {
		b := window[i]
		rng := b.High - b.Low
		if rng > 0 {
			bodyRatio := math.Abs(b.Close-b.Open) / rng
			if bodyRatio <= cfg.TrendWickBodyMaxRatio {
				wickHeavy++
			}
		}
		if i == 0 {
			continue
		}
		delta := b.Close - window[i-1].Close
		path += math.Abs(delta)
		if delta > 0 {
			rising++
		}
	}

	gain := last/first - 1
	efficiency := 0.0
	if path > 0 && last > first {
		efficiency = (last - first) / path
	}
	steps := len(window) - 1
	risingRatio := 0.0
	if steps > 0 {
		risingRatio = float64(rising) / float64(steps)
	}

	pass := gain >= cfg.MinimumTrendGainPct &&
		risingRatio >= cfg.MinimumTrendRisingRatio &&
		wickHeavy*2 < len(window)
	reason := fmt.Sprintf(
		"%s trend gain %.2f%% efficiency %.2f rising %.0f%% wick %d/%d",
		cfg.MinimumTrendTimeframe,
		gain*100,
		efficiency,
		risingRatio*100,
		wickHeavy,
		len(window),
	)
	return bollPumpMinimumTrendGateResult{Pass: pass, Reason: reason}
}

type bollPumpResistanceBreakoutResult struct {
	Triggered  bool
	Resistance float64
	Touches    int
	Distance   float64
	Bonus      float64
	Reason     string
}

type bollPumpResistanceCluster struct {
	sum     float64
	avg     float64
	maxHigh float64
	touches int
}

type bollPumpResistanceBreakoutParams struct {
	Label             string
	Lookback          int
	SwingSpan         int
	ClusterATR        float64
	ClusterPct        float64
	BreakoutBufferPct float64
	MaxDistancePct    float64
	MinTouches        int
	Bonus             float64
}

func bollPumpFourHourResistanceBreakout(bars []BollPumpBar, cfg BollPumpConfig) bollPumpResistanceBreakoutResult {
	cfg = NormalizeBollPumpConfig(cfg)
	if cfg.Resistance4HBreakoutBonus <= 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	return bollPumpResistanceBreakout(bars, cfg, bollPumpResistanceBreakoutParams{
		Label:             "4h",
		Lookback:          cfg.Resistance4HLookback,
		SwingSpan:         cfg.Resistance4HSwingSpan,
		ClusterATR:        cfg.Resistance4HClusterATR,
		ClusterPct:        cfg.Resistance4HClusterPct,
		BreakoutBufferPct: cfg.Resistance4HBreakoutBufferPct,
		MaxDistancePct:    cfg.Resistance4HMaxDistancePct,
		MinTouches:        cfg.Resistance4HMinTouches,
		Bonus:             cfg.Resistance4HBreakoutBonus,
	})
}

func bollPumpCurrentTimeframeResistanceBreakout(timeframe string, bars []BollPumpBar, cfg BollPumpConfig) bollPumpResistanceBreakoutResult {
	cfg = NormalizeBollPumpConfig(cfg)
	return bollPumpResistanceBreakout(bars, cfg, bollPumpResistanceBreakoutParams{
		Label:             timeframe,
		Lookback:          cfg.ResistanceLookback,
		SwingSpan:         cfg.ResistanceSwingSpan,
		ClusterATR:        cfg.ResistanceClusterATR,
		ClusterPct:        cfg.ResistanceClusterPct,
		BreakoutBufferPct: cfg.ResistanceBreakoutBufferPct,
		MaxDistancePct:    cfg.ResistanceMaxDistancePct,
		MinTouches:        cfg.ResistanceMinTouches,
		Bonus:             0,
	})
}

func bollPumpResistanceBreakout(bars []BollPumpBar, cfg BollPumpConfig, params bollPumpResistanceBreakoutParams) bollPumpResistanceBreakoutResult {
	closed := make([]BollPumpBar, 0, len(bars))
	for _, b := range bars {
		if b.Closed && b.High > 0 && b.Close > 0 {
			closed = append(closed, b)
		}
	}
	latestIdx := len(closed) - 1
	prevEnd := latestIdx - 1
	if prevEnd <= 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	price := closed[latestIdx].Close
	lookback := params.Lookback
	if lookback > prevEnd+1 {
		lookback = prevEnd + 1
	}
	start := prevEnd - lookback + 1
	if start < 0 {
		start = 0
	}
	span := params.SwingSpan
	if start+span > prevEnd-span {
		return bollPumpResistanceBreakoutResult{}
	}

	ind := ComputeBollPumpIndicators(closed, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)
	atr := 0.0
	if latestIdx < len(ind) && ind[latestIdx].ValidATR {
		atr = ind[latestIdx].ATR14
	}
	tolerance := math.Max(atr*params.ClusterATR, price*params.ClusterPct)
	if tolerance <= 0 {
		return bollPumpResistanceBreakoutResult{}
	}

	highs := make([]float64, 0, lookback)
	for i := start + span; i <= prevEnd-span; i++ {
		high := closed[i].High
		isSwing := true
		for j := i - span; j <= i+span; j++ {
			if j != i && closed[j].High >= high {
				isSwing = false
				break
			}
		}
		if isSwing {
			highs = append(highs, high)
		}
	}
	if len(highs) == 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	sort.Float64s(highs)

	clusters := make([]bollPumpResistanceCluster, 0, len(highs))
	for _, high := range highs {
		placed := false
		for i := range clusters {
			if math.Abs(high-clusters[i].avg) <= tolerance {
				clusters[i].sum += high
				clusters[i].touches++
				clusters[i].avg = clusters[i].sum / float64(clusters[i].touches)
				if high > clusters[i].maxHigh {
					clusters[i].maxHigh = high
				}
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, bollPumpResistanceCluster{sum: high, avg: high, maxHigh: high, touches: 1})
		}
	}

	var best bollPumpResistanceBreakoutResult
	for _, cluster := range clusters {
		if cluster.touches < params.MinTouches || cluster.maxHigh <= 0 {
			continue
		}
		breakoutLevel := cluster.maxHigh * (1 + params.BreakoutBufferPct)
		if price <= breakoutLevel {
			continue
		}
		distance := price/cluster.maxHigh - 1
		if params.MaxDistancePct > 0 && distance > params.MaxDistancePct {
			continue
		}
		if !best.Triggered || cluster.maxHigh > best.Resistance {
			best = bollPumpResistanceBreakoutResult{
				Triggered:  true,
				Resistance: cluster.maxHigh,
				Touches:    cluster.touches,
				Distance:   distance,
				Bonus:      params.Bonus,
			}
		}
	}
	if !best.Triggered {
		return bollPumpResistanceBreakoutResult{}
	}
	best.Reason = fmt.Sprintf("%s resistance breakout %.6g, touches %d, distance %.2f%%", strings.TrimSpace(params.Label), best.Resistance, best.Touches, best.Distance*100)
	return best
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

func bollPumpScoreFloor(v float64) float64 {
	if v < 0 {
		return 0
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

type BollPumpRuntimeState struct {
	Market                  string
	Symbol                  string
	Timeframe               string
	Status                  string
	WatchScore              float64
	CurrentScore            float64
	WatchCandleStartMs      int64
	WatchStartedMs          int64
	BounceCount             int
	FirstPullbackLow        float64
	SecondPullbackLow       float64
	PendingPullbackCandleMs int64
	PendingPullbackHigh     float64
	PendingPullbackLow      float64
	ExpiresAtCandleMs       int64
	LastCheckedCandleMs     int64
	LastSignalLevel         string
}

type BollPumpAdvanceResult struct {
	State   BollPumpRuntimeState
	Signals []model.BollPumpSignal
}

func NewBollPumpRuntimeState(market, symbol, timeframe string) BollPumpRuntimeState {
	return BollPumpRuntimeState{
		Market:    normalizeBollPumpMarket(market),
		Symbol:    strings.ToUpper(strings.TrimSpace(symbol)),
		Timeframe: timeframe,
		Status:    string(BollPumpStatusIdle),
	}
}

func AdvanceBollPumpState(state *BollPumpRuntimeState, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64, cfg BollPumpConfig) BollPumpAdvanceResult {
	if state == nil || len(bars) == 0 || len(bars) != len(ind) {
		return BollPumpAdvanceResult{}
	}
	latestIdx := len(bars) - 1
	latest := bars[latestIdx]
	if latest.OpenTimeMs <= state.LastCheckedCandleMs {
		return BollPumpAdvanceResult{State: *state}
	}
	if state.Status == "" {
		state.Status = string(BollPumpStatusIdle)
	}
	state.LastCheckedCandleMs = latest.OpenTimeMs
	signals := make([]model.BollPumpSignal, 0, 1)
	if bollPumpActiveLowerBandBreakdown(*state, latest, ind[latestIdx]) {
		bollPumpInvalidateRuntimeState(state)
		return BollPumpAdvanceResult{State: *state, Signals: signals}
	}

	switch state.Status {
	case string(BollPumpStatusIdle), string(BollPumpStatusExpired), string(BollPumpStatusCompleted), string(BollPumpStatusInvalidated):
		watch := EvaluateBollPumpWatch(state.Market, state.Symbol, state.Timeframe, bars, ind, quoteVolume24h, cfg)
		if watch.Triggered {
			state.Status = string(BollPumpStatusWatch)
			state.WatchScore = watch.Signal.Score
			state.CurrentScore = watch.Signal.Score
			state.WatchCandleStartMs = watch.Signal.CandleStartMs
			state.WatchStartedMs = watch.Signal.SignalTimeMs
			state.BounceCount = 0
			state.FirstPullbackLow = 0
			state.SecondPullbackLow = 0
			state.PendingPullbackCandleMs = 0
			state.PendingPullbackHigh = 0
			state.PendingPullbackLow = 0
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			state.LastSignalLevel = string(BollPumpLevelWatch)
			signals = append(signals, watch.Signal)
		}
	case string(BollPumpStatusWatch):
		if bollPumpHasThreeMiddleClosesAfterWatch(*state, bars, ind) && bollPumpIsPullbackCandidate(latest, ind[latestIdx]) {
			state.Status = string(BollPumpStatusPullback1Pending)
			state.PendingPullbackCandleMs = latest.OpenTimeMs
			state.PendingPullbackHigh = latest.High
			state.PendingPullbackLow = latest.Low
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			break
		}
		if bollPumpWatchTrendFailed(*state, bars, ind, cfg) {
			bollPumpInvalidateRuntimeState(state)
			break
		}
		if bollPumpStageExpired(*state, latest) {
			if bollPumpTrendStillAdvancing(latest, ind[latestIdx]) {
				bollPumpRefreshStageExpiry(state, latest, bars, cfg)
				break
			}
			state.Status = string(BollPumpStatusExpired)
		}
	case string(BollPumpStatusPullback1Pending):
		if bollPumpPendingInvalid(latest, ind[latestIdx]) {
			bollPumpInvalidateRuntimeState(state)
			break
		}
		if bollPumpIsPullbackCandidate(latest, ind[latestIdx]) {
			state.PendingPullbackCandleMs = latest.OpenTimeMs
			state.PendingPullbackHigh = latest.High
			state.PendingPullbackLow = latest.Low
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			break
		}
		if bollPumpStageExpired(*state, latest) {
			state.Status = string(BollPumpStatusExpired)
			break
		}
		if latest.OpenTimeMs > state.PendingPullbackCandleMs && latest.High > state.PendingPullbackHigh {
			if !bollPumpBounceRecovered(latest, ind[latestIdx]) {
				break
			}
			state.Status = string(BollPumpStatusConfirm1)
			state.BounceCount = 1
			state.FirstPullbackLow = state.PendingPullbackLow
			state.CurrentScore = bollPumpScoreFloor(state.WatchScore + 10)
			state.LastSignalLevel = string(BollPumpLevelConfirm1)
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			signals = append(signals, bollPumpConfirmSignal(*state, latest, ind[latestIdx], quoteVolume24h, BollPumpLevelConfirm1))
			state.PendingPullbackCandleMs = 0
			state.PendingPullbackHigh = 0
			state.PendingPullbackLow = 0
		}
	case string(BollPumpStatusConfirm1):
		if bollPumpIsPullbackCandidate(latest, ind[latestIdx]) {
			if bollPumpSecondLowInvalid(state.FirstPullbackLow, latest.Low, ind[latestIdx].ATR14) {
				bollPumpInvalidateRuntimeState(state)
				break
			}
			state.Status = string(BollPumpStatusPullback2Pending)
			state.PendingPullbackCandleMs = latest.OpenTimeMs
			state.PendingPullbackHigh = latest.High
			state.PendingPullbackLow = latest.Low
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			break
		}
		if bollPumpStageExpired(*state, latest) {
			if bollPumpTrendStillAdvancing(latest, ind[latestIdx]) {
				bollPumpRefreshStageExpiry(state, latest, bars, cfg)
				break
			}
			state.Status = string(BollPumpStatusExpired)
		}
	case string(BollPumpStatusPullback2Pending):
		if bollPumpPendingInvalid(latest, ind[latestIdx]) {
			bollPumpInvalidateRuntimeState(state)
			break
		}
		if bollPumpIsPullbackCandidate(latest, ind[latestIdx]) {
			if bollPumpSecondLowInvalid(state.FirstPullbackLow, latest.Low, ind[latestIdx].ATR14) {
				bollPumpInvalidateRuntimeState(state)
				break
			}
			state.PendingPullbackCandleMs = latest.OpenTimeMs
			state.PendingPullbackHigh = latest.High
			state.PendingPullbackLow = latest.Low
			bollPumpRefreshStageExpiry(state, latest, bars, cfg)
			break
		}
		if bollPumpStageExpired(*state, latest) {
			state.Status = string(BollPumpStatusExpired)
			break
		}
		if latest.OpenTimeMs > state.PendingPullbackCandleMs && latest.High > state.PendingPullbackHigh {
			if !bollPumpBounceRecovered(latest, ind[latestIdx]) {
				break
			}
			state.Status = string(BollPumpStatusCompleted)
			state.BounceCount = 2
			state.SecondPullbackLow = state.PendingPullbackLow
			state.CurrentScore = bollPumpScoreFloor(state.WatchScore + 20)
			state.LastSignalLevel = string(BollPumpLevelConfirm2)
			signals = append(signals, bollPumpConfirmSignal(*state, latest, ind[latestIdx], quoteVolume24h, BollPumpLevelConfirm2))
			state.PendingPullbackCandleMs = 0
			state.PendingPullbackHigh = 0
			state.PendingPullbackLow = 0
		}
	}
	return BollPumpAdvanceResult{State: *state, Signals: signals}
}

func bollPumpSecondLowInvalid(firstLow, secondLow, atr14 float64) bool {
	if firstLow <= 0 || secondLow <= 0 {
		return false
	}
	tolerance := math.Max(atr14, firstLow*0.015)
	return secondLow < firstLow-tolerance
}

func bollPumpStageExpired(state BollPumpRuntimeState, latest BollPumpBar) bool {
	return state.ExpiresAtCandleMs > 0 && latest.OpenTimeMs > state.ExpiresAtCandleMs
}

func bollPumpRefreshStageExpiry(state *BollPumpRuntimeState, latest BollPumpBar, bars []BollPumpBar, cfg BollPumpConfig) {
	if state == nil {
		return
	}
	state.ExpiresAtCandleMs = latest.OpenTimeMs + int64(cfg.StageExpiryCandles)*bollPumpIntervalMs(state.Timeframe, bars)
}

func bollPumpInvalidateRuntimeState(state *BollPumpRuntimeState) {
	if state == nil {
		return
	}
	state.Status = string(BollPumpStatusInvalidated)
	state.WatchScore = 0
	state.CurrentScore = 0
	state.BounceCount = 0
	state.FirstPullbackLow = 0
	state.SecondPullbackLow = 0
	state.PendingPullbackCandleMs = 0
	state.PendingPullbackHigh = 0
	state.PendingPullbackLow = 0
	state.LastSignalLevel = ""
}

func bollPumpActiveLowerBandBreakdown(state BollPumpRuntimeState, b BollPumpBar, in BollPumpIndicator) bool {
	if !bollPumpStatusIsActive(state.Status) {
		return false
	}
	return bollPumpPendingInvalid(b, in)
}

func bollPumpStatusIsActive(status string) bool {
	switch status {
	case string(BollPumpStatusWatch),
		string(BollPumpStatusPullback1Pending),
		string(BollPumpStatusConfirm1),
		string(BollPumpStatusPullback2Pending),
		string(BollPumpStatusConfirm2),
		string(BollPumpStatusCompleted):
		return true
	default:
		return false
	}
}

func bollPumpWatchTrendFailed(state BollPumpRuntimeState, bars []BollPumpBar, ind []BollPumpIndicator, cfg BollPumpConfig) bool {
	if state.WatchCandleStartMs <= 0 || len(bars) == 0 || len(bars) != len(ind) {
		return false
	}
	watchIdx := -1
	for i, b := range bars {
		if b.OpenTimeMs == state.WatchCandleStartMs {
			watchIdx = i
			break
		}
	}
	latestIdx := len(bars) - 1
	if watchIdx < 0 || latestIdx <= watchIdx {
		return false
	}
	postCount := latestIdx - watchIdx
	if postCount < cfg.WatchTrendCheckCandles {
		return false
	}
	watch := bars[watchIdx]
	latest := bars[latestIdx]
	if ind[latestIdx].ValidBoll && latest.Close >= ind[latestIdx].Middle {
		return false
	}
	drawdown := watch.Close - latest.Close
	if drawdown <= 0 || watch.Close <= 0 {
		return false
	}
	pctLimit := watch.Close * cfg.WatchTrendMaxDrawdownPct
	atrLimit := 0.0
	if ind[latestIdx].ValidATR {
		atrLimit = ind[latestIdx].ATR14 * cfg.WatchTrendMaxDrawdownATR
	}
	return drawdown >= math.Max(pctLimit, atrLimit)
}

func bollPumpHasThreeMiddleClosesAfterWatch(state BollPumpRuntimeState, bars []BollPumpBar, ind []BollPumpIndicator) bool {
	count := 0
	for i, b := range bars {
		if b.OpenTimeMs <= state.WatchCandleStartMs || b.OpenTimeMs >= state.LastCheckedCandleMs {
			continue
		}
		if i >= len(ind) || !ind[i].ValidBoll {
			continue
		}
		if b.Close >= ind[i].Middle {
			count++
		}
	}
	return count >= 3
}

func bollPumpIsPullbackCandidate(b BollPumpBar, in BollPumpIndicator) bool {
	return in.ValidBoll && b.Low <= in.Lower && b.Close > in.Lower
}

func bollPumpBounceRecovered(b BollPumpBar, in BollPumpIndicator) bool {
	return in.ValidBoll && b.Close >= in.Middle
}

func bollPumpTrendStillAdvancing(b BollPumpBar, in BollPumpIndicator) bool {
	return in.ValidBoll && b.Close >= in.Middle
}

func bollPumpPendingInvalid(b BollPumpBar, in BollPumpIndicator) bool {
	return in.ValidBoll && b.Close < in.Lower
}

func bollPumpConfirmSignal(state BollPumpRuntimeState, b BollPumpBar, in BollPumpIndicator, quoteVolume24h float64, level BollPumpSignalLevel) model.BollPumpSignal {
	score := state.CurrentScore
	bounceCount := state.BounceCount
	reason := "first lower-band confirm"
	if level == BollPumpLevelConfirm2 {
		reason = "second lower-band confirm"
	}
	return model.BollPumpSignal{
		Market:         state.Market,
		Symbol:         state.Symbol,
		Timeframe:      state.Timeframe,
		SignalLevel:    string(level),
		Price:          b.Close,
		VolumeRatio:    0,
		BollBandwidth:  in.Bandwidth,
		BounceCount:    bounceCount,
		Score:          score,
		PriorityScore:  score,
		SignalTimeMs:   b.CloseTimeMs,
		CandleStartMs:  b.OpenTimeMs,
		QuoteVolume24h: quoteVolume24h,
		Reason:         reason,
		Details:        model.JSONB(`{}`),
	}
}

func bollPumpIntervalMs(timeframe string, bars []BollPumpBar) int64 {
	if len(bars) >= 2 {
		d := bars[len(bars)-1].OpenTimeMs - bars[len(bars)-2].OpenTimeMs
		if d > 0 {
			return d
		}
	}
	if strings.HasSuffix(timeframe, "m") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(timeframe, "m"), 10, 64)
		if n > 0 {
			return n * 60 * 1000
		}
	}
	if strings.HasSuffix(timeframe, "h") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(timeframe, "h"), 10, 64)
		if n > 0 {
			return n * 60 * 60 * 1000
		}
	}
	return 60 * 1000
}
