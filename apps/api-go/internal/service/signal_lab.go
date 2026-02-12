package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

const (
	sigPersistentBuy = "signal_lab_persistent_buy"
	sigSingleLarge   = "signal_lab_single_large"
	sigClimaxShort   = "signal_lab_climax_short"
	sigClimaxLong    = "signal_lab_climax_long"
)

// ---------------------------------------------------------------------------
// SignalLabParams
// ---------------------------------------------------------------------------

type SignalLabParams struct {
	Bucket                    string  `json:"bucket"`
	ZThreshold                float64 `json:"zThreshold"`
	LookbackMinutes           int     `json:"lookbackMinutes"`
	DetectionWindowMinutes    int     `json:"detectionWindowMinutes"`
	MinLargeCount             int     `json:"minLargeCount"`
	BuyRatioThreshold         float64 `json:"buyRatioThreshold"`
	MinPersistentSpanMinutes  int     `json:"minPersistentSpanMinutes"`
	MinAvgIntervalMinutes     int     `json:"minAvgIntervalMinutes"`
	MinDistinctTimeBuckets    int     `json:"minDistinctTimeBuckets"`
	ForecastHorizonMinutes    int     `json:"forecastHorizonMinutes"`
	CooldownMinutes           int     `json:"cooldownMinutes"`
	SingleLargeZThreshold     float64 `json:"singleLargeZThreshold"`
	SingleLargeMinNotional    float64 `json:"singleLargeMinNotional"`
	SingleLargeCooldownMin    int     `json:"singleLargeCooldownMinutes"`
	SlopeWindowMinutes        int     `json:"slopeWindowMinutes"`
	SlopeR2Threshold          float64 `json:"slopeR2Threshold"`
	SymbolLimit               int     `json:"symbolLimit"`
}

func DefaultSignalLabParams() SignalLabParams {
	return SignalLabParams{
		Bucket: "1h", ZThreshold: 2.8, LookbackMinutes: 4320, DetectionWindowMinutes: 1440,
		MinLargeCount: 3, BuyRatioThreshold: 0.8, MinPersistentSpanMinutes: 180,
		MinAvgIntervalMinutes: 60, MinDistinctTimeBuckets: 3, ForecastHorizonMinutes: 240,
		CooldownMinutes: 720, SingleLargeZThreshold: 3.5, SingleLargeMinNotional: 10000,
		SingleLargeCooldownMin: 240, SlopeWindowMinutes: 720, SlopeR2Threshold: 0.7, SymbolLimit: 200,
	}
}

// ---------------------------------------------------------------------------
// BucketPoint
// ---------------------------------------------------------------------------

type bucketPoint struct {
	Ts    int64
	Close float64
	Buy   float64
	Sell  float64
}

func (b bucketPoint) net() float64 { return b.Buy - b.Sell }

func slBucketMinutes(bucket string) int {
	b := strings.TrimSpace(strings.ToLower(bucket))
	if strings.HasSuffix(b, "h") {
		n := atoi(b[:len(b)-1])
		return n * 60
	}
	if strings.HasSuffix(b, "d") {
		n := atoi(b[:len(b)-1])
		return n * 1440
	}
	n := atoi(strings.TrimSuffix(b, "m"))
	if n <= 0 {
		n = 1
	}
	return n
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---------------------------------------------------------------------------
// Scoring
// ---------------------------------------------------------------------------

func slSignalState(score float64) string {
	if score >= 85 {
		return "STRONG"
	}
	if score >= 70 {
		return "CONFIRM"
	}
	if score >= 55 {
		return "WATCH"
	}
	return "NONE"
}

func slSignalStateRank(state string) int {
	s := strings.ToUpper(state)
	switch s {
	case "STRONG", "HIGH":
		return 3
	case "CONFIRM":
		return 2
	case "WATCH":
		return 1
	}
	return 0
}

func mcapImpactAdj(flowUSDT, mcapUSD float64) float64 {
	if mcapUSD <= 0 || flowUSDT <= 0 {
		return 0
	}
	ratio := flowUSDT / mcapUSD
	if ratio < 1e-9 {
		return -10
	}
	logR := math.Log10(ratio)
	return clamp((logR+5.0)*6.25, -10, 15)
}

func scoreSignal(zScore, buyRatio float64, largeBuyCount int) float64 {
	zPart := clamp((zScore-2.0)*15.0, 0, 30)
	ratioPart := clamp((buyRatio-0.5)*100.0, 0, 30)
	countPart := clamp(float64(largeBuyCount)*4.0, 0, 20)
	return math.Round(clamp(35+zPart+ratioPart+countPart, 0, 100)*100) / 100
}

func scoreSingleLarge(zScore float64, slopeConfirms bool) float64 {
	zPart := clamp((zScore-2.0)*20.0, 0, 50)
	slopePart := 0.0
	if slopeConfirms {
		slopePart = 10
	}
	return math.Round(clamp(40+zPart+slopePart, 0, 100)*100) / 100
}

// ---------------------------------------------------------------------------
// linreg on cumsum
// ---------------------------------------------------------------------------

func cumsum(vals []float64) []float64 {
	out := make([]float64, len(vals))
	s := 0.0
	for i, v := range vals {
		s += v
		out[i] = s
	}
	return out
}

func linreg(ys []float64) (float64, float64) {
	n := len(ys)
	if n < 3 {
		return 0, 0
	}
	nf := float64(n)
	sumX := nf * (nf - 1) / 2
	sumX2 := nf * (nf - 1) * (2*nf - 1) / 6
	sumY, sumXY := 0.0, 0.0
	for i, y := range ys {
		sumY += y
		sumXY += float64(i) * y
	}
	denom := nf*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-15 {
		return 0, 0
	}
	k := (nf*sumXY - sumX*sumY) / denom
	b := (sumY - k*sumX) / nf
	yMean := sumY / nf
	ssTot, ssRes := 0.0, 0.0
	for i, y := range ys {
		ssTot += (y - yMean) * (y - yMean)
		pred := k*float64(i) + b
		ssRes += (y - pred) * (y - pred)
	}
	if ssTot < 1e-15 {
		return 0, 0
	}
	r2 := 1 - ssRes/ssTot
	return k, math.Max(0, r2)
}

// ---------------------------------------------------------------------------
// scanSymbolSignals
// ---------------------------------------------------------------------------

func scanSymbolSignals(symbol, market string, rows []bucketPoint, params SignalLabParams, mcapUSD float64) []map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}

	lookback := max(60, params.LookbackMinutes)
	detectWindowMs := int64(max(15, params.DetectionWindowMinutes)) * 60 * 1000
	minPersistSpanMs := int64(max(1, params.MinPersistentSpanMinutes)) * 60 * 1000
	minAvgIntMs := int64(max(1, params.MinAvgIntervalMinutes)) * 60 * 1000
	bktMin := slBucketMinutes(params.Bucket)
	distinctBucketMs := int64(max(bktMin*4, 10)) * 60 * 1000
	cooldownMs := int64(max(1, params.CooldownMinutes)) * 60 * 1000
	singleCooldownMs := int64(max(1, params.SingleLargeCooldownMin)) * 60 * 1000
	singleZThr := params.SingleLargeZThreshold
	singleMinNot := params.SingleLargeMinNotional
	slopeWindow := max(10, params.SlopeWindowMinutes)
	slopeR2Thr := params.SlopeR2Threshold

	type largeEvent struct {
		ts   int64
		side string
		absN float64
	}

	hist := make([]float64, 0, lookback)
	histSum, histSumSq := 0.0, 0.0
	var largeEvents []largeEvent
	var netFlowBuf []float64
	var signals []map[string]interface{}
	lastPersistentTs := int64(0)
	lastSingleTs := int64(0)

	pushHist := func(x float64) {
		hist = append(hist, x)
		histSum += x
		histSumSq += x * x
		if len(hist) > lookback {
			old := hist[0]
			hist = hist[1:]
			histSum -= old
			histSumSq -= old * old
		}
	}

	for _, row := range rows {
		x := math.Abs(row.net())

		netFlowBuf = append(netFlowBuf, row.net())
		if len(netFlowBuf) > slopeWindow {
			netFlowBuf = netFlowBuf[1:]
		}

		n := len(hist)
		zScore := 0.0
		if n >= 20 {
			mean := histSum / float64(n)
			variance := math.Max(0, histSumSq/float64(n)-mean*mean)
			std := math.Sqrt(variance)
			if std > 1e-9 {
				zScore = (x - mean) / std
			}
		}

		minTs := row.Ts - detectWindowMs
		trimIdx := 0
		for trimIdx < len(largeEvents) && largeEvents[trimIdx].ts < minTs {
			trimIdx++
		}
		if trimIdx > 0 {
			largeEvents = largeEvents[trimIdx:]
		}

		rowSlopeK, rowSlopeR2 := 0.0, 0.0

		if zScore >= params.ZThreshold {
			side := "buy"
			if row.net() <= 0 {
				side = "sell"
			}
			largeEvents = append(largeEvents, largeEvent{row.Ts, side, x})

			if len(netFlowBuf) >= 10 {
				rowSlopeK, rowSlopeR2 = linreg(cumsum(netFlowBuf))
			}

			if zScore >= singleZThr && x >= singleMinNot && row.Ts-lastSingleTs >= singleCooldownMs {
				slopeConfSL := rowSlopeR2 >= slopeR2Thr &&
					((side == "buy" && rowSlopeK > 0) || (side == "sell" && rowSlopeK < 0))
				mcapAdjSL := mcapImpactAdj(math.Abs(row.net()), mcapUSD)
				slScore := scoreSingleLarge(zScore, slopeConfSL)
				slScore = math.Round(clamp(slScore+mcapAdjSL, 0, 100)*100) / 100
				flowMcapPct := 0.0
				if mcapUSD > 0 {
					flowMcapPct = math.Abs(row.net()) / mcapUSD * 100
				}
				signals = append(signals, map[string]interface{}{
					"market": market, "symbol": symbol, "ts": row.Ts,
					"close": row.Close, "netFlow": row.net(),
					"zScore": math.Round(zScore*10000) / 10000,
					"direction": side,
					"slopeK": math.Round(rowSlopeK*1e6) / 1e6,
					"slopeR2": math.Round(rowSlopeR2*10000) / 10000,
					"slopeConfirms": slopeConfSL,
					"flowMcapPct": math.Round(flowMcapPct*1e6) / 1e6,
					"mcapAdj": math.Round(mcapAdjSL*100) / 100,
					"score": slScore, "signalState": slSignalState(slScore),
					"eventType": sigSingleLarge,
				})
				lastSingleTs = row.Ts
			}
		}

		buyAmt, sellAmt := 0.0, 0.0
		largeBuyCount := 0
		var buyEventTs []int64
		for _, ev := range largeEvents {
			if ev.side == "buy" {
				buyAmt += ev.absN
				largeBuyCount++
				buyEventTs = append(buyEventTs, ev.ts)
			} else {
				sellAmt += ev.absN
			}
		}
		totalAmt := buyAmt + sellAmt
		buyRatio := 0.0
		if totalAmt > 0 {
			buyRatio = buyAmt / totalAmt
		}

		persistentSpanMs := int64(0)
		avgIntervalMs := 0.0
		if len(buyEventTs) >= 2 {
			persistentSpanMs = buyEventTs[len(buyEventTs)-1] - buyEventTs[0]
			sum := int64(0)
			for i := 1; i < len(buyEventTs); i++ {
				sum += buyEventTs[i] - buyEventTs[i-1]
			}
			avgIntervalMs = float64(sum) / float64(len(buyEventTs)-1)
		}
		distinctBuckets := countDistinct(buyEventTs, distinctBucketMs)

		isTrigger := row.net() > 0 &&
			zScore >= params.ZThreshold &&
			largeBuyCount >= params.MinLargeCount &&
			buyRatio >= params.BuyRatioThreshold &&
			persistentSpanMs >= minPersistSpanMs &&
			int64(avgIntervalMs) >= minAvgIntMs &&
			distinctBuckets >= params.MinDistinctTimeBuckets

		if !isTrigger {
			pushHist(x)
			continue
		}
		if row.Ts-lastPersistentTs < cooldownMs {
			pushHist(x)
			continue
		}

		slopeConfPB := rowSlopeR2 >= slopeR2Thr && rowSlopeK > 0
		mcapAdjPB := mcapImpactAdj(buyAmt, mcapUSD)
		score := scoreSignal(zScore, buyRatio, largeBuyCount)
		bonus := 0.0
		if slopeConfPB {
			bonus = 10
		}
		score = math.Round(clamp(score+bonus+mcapAdjPB, 0, 100)*100) / 100
		flowMcapPctPB := 0.0
		if mcapUSD > 0 {
			flowMcapPctPB = buyAmt / mcapUSD * 100
		}
		signals = append(signals, map[string]interface{}{
			"market": market, "symbol": symbol, "ts": row.Ts,
			"close": row.Close, "netFlow": row.net(),
			"zScore": math.Round(zScore*10000) / 10000,
			"largeBuyCount": largeBuyCount, "buyRatio": math.Round(buyRatio*10000) / 10000,
			"persistentSpanMinutes": math.Round(float64(persistentSpanMs)/60000*100) / 100,
			"avgIntervalMinutes":    math.Round(avgIntervalMs/60000*100) / 100,
			"distinctTimeBuckets":   distinctBuckets,
			"slopeK": math.Round(rowSlopeK*1e6) / 1e6,
			"slopeR2": math.Round(rowSlopeR2*10000) / 10000,
			"slopeConfirms": slopeConfPB,
			"flowMcapPct": math.Round(flowMcapPctPB*1e6) / 1e6,
			"mcapAdj": math.Round(mcapAdjPB*100) / 100,
			"score": score, "signalState": slSignalState(score),
			"eventType": sigPersistentBuy,
		})
		lastPersistentTs = row.Ts
		pushHist(x)
	}
	return signals
}

func countDistinct(ts []int64, bucketMs int64) int {
	seen := make(map[int64]struct{})
	for _, t := range ts {
		seen[t/bucketMs] = struct{}{}
	}
	return len(seen)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// loadMarketCaps, topSymbols, loadRows
// ---------------------------------------------------------------------------

func loadMarketCaps(ctx context.Context, ch *chrepo.Client, symbols []string) map[string]float64 {
	assetToSym := make(map[string]string)
	for _, sym := range symbols {
		upper := strings.ToUpper(sym)
		for _, suffix := range []string{"USDT", "USDC", "BUSD"} {
			if strings.HasSuffix(upper, suffix) {
				assetToSym[upper[:len(upper)-len(suffix)]] = sym
				break
			}
		}
	}
	if len(assetToSym) == 0 {
		return nil
	}
	assets := make([]string, 0, len(assetToSym))
	for a := range assetToSym {
		assets = append(assets, a)
	}
	rows, err := ch.QueryMarketCaps(ctx, assets)
	if err != nil {
		return nil
	}
	out := make(map[string]float64)
	for _, r := range rows {
		if sym, ok := assetToSym[r.Asset]; ok && r.MarketCapUSD > 0 {
			out[sym] = r.MarketCapUSD
		}
	}
	return out
}

func topSymbols(ctx context.Context, ch *chrepo.Client, market string, symbolLimit int) []string {
	sinceMs := time.Now().UnixMilli() - 24*60*60*1000
	lim := symbolLimit
	if lim < 20 {
		lim = 20
	}
	rows, err := ch.QueryTradeAggVolume(ctx, market, "1m", sinceMs, lim)
	if err != nil {
		return nil
	}
	symbols := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Symbol != "" {
			symbols = append(symbols, r.Symbol)
		}
	}
	return binance.FilterExcludedSymbols(symbols)
}

func loadRows(ctx context.Context, ch *chrepo.Client, market string, symbols []string, startMs, endMs int64, bucket string) map[string][]bucketPoint {
	if len(symbols) == 0 {
		return nil
	}
	rows, err := ch.QueryTradeBuckets(ctx, market, "", symbols, "1m", startMs, endMs, "asc", 0)
	if err != nil {
		return nil
	}

	bktMs := int64(slBucketMinutes(bucket)) * 60 * 1000

	raw := make(map[string][]model.CHTradeRow)
	for _, r := range rows {
		raw[r.Symbol] = append(raw[r.Symbol], r)
	}

	grouped := make(map[string][]bucketPoint)
	for sym, symRows := range raw {
		if bktMs <= 60000 {
			pts := make([]bucketPoint, 0, len(symRows))
			for _, r := range symRows {
				cp := 0.0
				if r.ClosePrice != nil {
					cp = *r.ClosePrice
				}
				pts = append(pts, bucketPoint{Ts: r.BucketStartMs, Close: cp, Buy: r.TakerBuyNotional, Sell: r.TakerSellNotional})
			}
			grouped[sym] = pts
		} else {
			type agg struct{ buy, sell, close float64 }
			m := make(map[int64]*agg)
			for _, r := range symRows {
				key := (r.BucketStartMs / bktMs) * bktMs
				a, ok := m[key]
				if !ok {
					a = &agg{}
					m[key] = a
				}
				a.buy += r.TakerBuyNotional
				a.sell += r.TakerSellNotional
				if r.ClosePrice != nil {
					a.close = *r.ClosePrice
				}
			}
			var pts []bucketPoint
			for k, a := range m {
				pts = append(pts, bucketPoint{Ts: k, Close: a.close, Buy: a.buy, Sell: a.sell})
			}
			sort.Slice(pts, func(i, j int) bool { return pts[i].Ts < pts[j].Ts })
			grouped[sym] = pts
		}
	}
	return grouped
}

// ---------------------------------------------------------------------------
// GetRealtimeSignals
// ---------------------------------------------------------------------------

func GetRealtimeSignals(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, marketScope string, params SignalLabParams, limit int, minSignalState string, syncToScoreFlow bool) (map[string]interface{}, error) {
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - int64(max(24, params.LookbackMinutes+params.DetectionWindowMinutes))*60*1000

	var allSignals []map[string]interface{}
	marketStats := make(map[string]interface{})
	minStateRank := slSignalStateRank(minSignalState)

	for _, market := range markets(marketScope) {
		symbols := topSymbols(ctx, ch, market, params.SymbolLimit)
		grouped := loadRows(ctx, ch, market, symbols, startMs, nowMs, params.Bucket)
		mcapMap := loadMarketCaps(ctx, ch, symbols)

		var marketSignals []map[string]interface{}
		minRows := max(20, 90/max(1, slBucketMinutes(params.Bucket)))
		for sym, rows := range grouped {
			if binance.IsExcludedSymbol(sym) || len(rows) < minRows {
				continue
			}
			items := scanSymbolSignals(sym, market, rows, params, mcapMap[sym])
			if len(items) == 0 {
				continue
			}
			byType := make(map[string]map[string]interface{})
			for _, item := range items {
				et, _ := item["eventType"].(string)
				byType[et] = item
			}
			for _, sig := range byType {
				st, _ := sig["signalState"].(string)
				if slSignalStateRank(st) >= minStateRank {
					marketSignals = append(marketSignals, sig)
				}
			}
		}

		sort.Slice(marketSignals, func(i, j int) bool {
			si, _ := marketSignals[i]["score"].(float64)
			sj, _ := marketSignals[j]["score"].(float64)
			if si != sj {
				return si > sj
			}
			ti, _ := marketSignals[i]["ts"].(int64)
			tj, _ := marketSignals[j]["ts"].(int64)
			return ti > tj
		})
		if limit > 0 && len(marketSignals) > limit {
			marketSignals = marketSignals[:limit]
		}
		allSignals = append(allSignals, marketSignals...)
		marketStats[market] = map[string]interface{}{
			"symbols": len(symbols), "activeSignals": len(marketSignals),
		}
	}

	sort.Slice(allSignals, func(i, j int) bool {
		si, _ := allSignals[i]["score"].(float64)
		sj, _ := allSignals[j]["score"].(float64)
		if si != sj {
			return si > sj
		}
		ti, _ := allSignals[i]["ts"].(int64)
		tj, _ := allSignals[j]["ts"].(int64)
		return ti > tj
	})
	if limit > 0 && len(allSignals) > limit {
		allSignals = allSignals[:limit]
	}

	inserted := 0
	if syncToScoreFlow && len(allSignals) > 0 {
		inserted, _ = syncSignalAnomalyEvents(ctx, store, allSignals, params.CooldownMinutes)
	}

	return map[string]interface{}{
		"market": marketScope, "limit": limit,
		"minSignalState": strings.ToUpper(minSignalState),
		"signals": allSignals, "stats": marketStats,
		"syncedToScoreFlow": syncToScoreFlow,
		"insertedEvents": inserted, "eventType": sigPersistentBuy, "ts": nowMs,
	}, nil
}

func markets(scope string) []string {
	if scope == "both" {
		return []string{"spot", "swap"}
	}
	return []string{scope}
}

// ---------------------------------------------------------------------------
// ScanClimaxReversal
// ---------------------------------------------------------------------------

func ScanClimaxReversal(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, marketScope string, symbolLimit, lookbackMinutes, avgWindow int, climaxFactor float64, reversalWindowMinutes int, sellCascadeThreshold, buyCascadeThreshold, minCascadeNotional, obImbalanceThreshold float64, cooldownMinutes int) (map[string]interface{}, error) {
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - int64(lookbackMinutes)*60000

	var allSignals []map[string]interface{}
	for _, market := range markets(marketScope) {
		symbols := topSymbols(ctx, ch, market, symbolLimit)
		symbols = binance.FilterExcludedSymbols(symbols)
		if len(symbols) == 0 {
			continue
		}

		tradeRows, err := ch.QueryTradeBuckets(ctx, market, "", symbols, "1m", startMs, nowMs, "asc", 0)
		if err != nil {
			continue
		}
		obRows, err := ch.QueryOrderbookFeatures(ctx, market, "", symbols, "1m", startMs, nowMs, "asc")
		if err != nil {
			continue
		}

		tradeBySym := make(map[string][]model.CHTradeRow)
		for _, r := range tradeRows {
			tradeBySym[r.Symbol] = append(tradeBySym[r.Symbol], r)
		}
		obBySym := make(map[string]map[int64]model.CHOBFeatureRow)
		for _, r := range obRows {
			m, ok := obBySym[r.Symbol]
			if !ok {
				m = make(map[int64]model.CHOBFeatureRow)
				obBySym[r.Symbol] = m
			}
			m[r.BucketStartMs] = r
		}

		for _, sym := range symbols {
			if binance.IsExcludedSymbol(sym) {
				continue
			}
			candles := tradeBySym[sym]
			if len(candles) < avgWindow+5 {
				continue
			}
			sort.Slice(candles, func(i, j int) bool { return candles[i].BucketStartMs < candles[j].BucketStartMs })
			obMap := obBySym[sym]
			sigs := detectClimaxForSymbol(sym, market, candles, obMap, avgWindow, climaxFactor, reversalWindowMinutes, sellCascadeThreshold, buyCascadeThreshold, minCascadeNotional, obImbalanceThreshold)
			allSignals = append(allSignals, sigs...)
		}
	}

	sort.Slice(allSignals, func(i, j int) bool {
		si, _ := allSignals[i]["score"].(float64)
		sj, _ := allSignals[j]["score"].(float64)
		return si > sj
	})

	inserted := 0
	if len(allSignals) > 0 {
		inserted, _ = syncClimaxEvents(ctx, store, allSignals, cooldownMinutes)
	}
	return map[string]interface{}{"candidates": len(allSignals), "insertedEvents": inserted}, nil
}

func detectClimaxForSymbol(symbol, market string, candles []model.CHTradeRow, obMap map[int64]model.CHOBFeatureRow, avgWindow int, climaxFactor float64, revWindowMin int, sellCascadeThresh, buyCascadeThresh, minCascadeNotional, obImbalanceThresh float64) []map[string]interface{} {
	n := len(candles)
	revBars := revWindowMin

	var signals []map[string]interface{}
	for i := avgWindow; i < n-revBars; i++ {
		c := candles[i]
		vol := c.QuoteNotional
		if vol <= 0 {
			continue
		}
		avgVol := 0.0
		for j := i - avgWindow; j < i; j++ {
			avgVol += candles[j].QuoteNotional
		}
		avgVol /= float64(avgWindow)
		if avgVol <= 0 {
			continue
		}
		volRatio := vol / avgVol
		if volRatio < climaxFactor {
			continue
		}

		closeP := ptrF(c.ClosePrice)
		openP := ptrF(c.OpenPrice)
		buyN := c.TakerBuyNotional
		sellN := c.TakerSellNotional
		total := buyN + sellN
		climaxBuyRatio := 0.5
		if total > 0 {
			climaxBuyRatio = buyN / total
		}
		bullish := closeP > openP && climaxBuyRatio > 0.55
		bearish := closeP < openP && climaxBuyRatio < 0.45
		if !bullish && !bearish {
			continue
		}

		window := candles[i+1 : i+1+revBars]
		if len(window) == 0 {
			continue
		}

		cascadeFound := false
		worstBuyRatio := 1.0
		worstSellNotional := 0.0
		var obImbVals []float64

		for _, wc := range window {
			wb := wc.TakerBuyNotional
			ws := wc.TakerSellNotional
			wt := wb + ws
			wBuyRatio := 0.5
			if wt > 0 {
				wBuyRatio = wb / wt
			}
			if bullish {
				if wBuyRatio < sellCascadeThresh && ws >= minCascadeNotional {
					cascadeFound = true
				}
				if wBuyRatio < worstBuyRatio {
					worstBuyRatio = wBuyRatio
					worstSellNotional = ws
				}
			} else if bearish {
				if wBuyRatio > buyCascadeThresh && wb >= minCascadeNotional {
					cascadeFound = true
				}
				if wBuyRatio > worstBuyRatio {
					worstBuyRatio = wBuyRatio
				}
			}
			if ob, ok := obMap[wc.BucketStartMs]; ok && ob.SampleCount > 0 {
				obImbVals = append(obImbVals, ob.DepthImbalanceL20Sum/float64(ob.SampleCount))
			}
		}
		if !cascadeFound {
			continue
		}

		avgImb := 0.0
		if len(obImbVals) > 0 {
			for _, v := range obImbVals {
				avgImb += v
			}
			avgImb /= float64(len(obImbVals))
		}
		obConfirmed := false
		if bullish && avgImb < -obImbalanceThresh {
			obConfirmed = true
		} else if bearish && avgImb > obImbalanceThresh {
			obConfirmed = true
		}
		if !obConfirmed {
			continue
		}

		volScore := 40.0 * clamp(volRatio/10.0, 0, 1)
		var cascadeScore float64
		if bullish {
			cascadeScore = 30.0 * clamp((0.50-worstBuyRatio)/0.30, 0, 1)
		} else {
			cascadeScore = 30.0 * clamp((worstBuyRatio-0.50)/0.30, 0, 1)
		}
		obScore := 30.0 * clamp(math.Abs(avgImb)/0.40, 0, 1)
		score := math.Round((volScore+cascadeScore+obScore)*100) / 100
		state := slSignalState(score)
		if state == "NONE" {
			continue
		}

		direction := "long"
		eventType := sigClimaxLong
		if bullish {
			direction = "short"
			eventType = sigClimaxShort
		}

		signals = append(signals, map[string]interface{}{
			"market": market, "symbol": symbol, "ts": c.BucketStartMs,
			"close": closeP, "direction": direction,
			"climaxVolume": math.Round(vol*100) / 100,
			"avgVolume": math.Round(avgVol*100) / 100,
			"volumeRatio": math.Round(volRatio*100) / 100,
			"climaxBuyRatio": math.Round(climaxBuyRatio*10000) / 10000,
			"cascadeBuyRatio": math.Round(worstBuyRatio*10000) / 10000,
			"cascadeSellNotional": math.Round(worstSellNotional*100) / 100,
			"obImbalance": math.Round(avgImb*10000) / 10000,
			"score": score, "signalState": state, "eventType": eventType,
		})
	}

	if len(signals) > 0 {
		sort.Slice(signals, func(i, j int) bool {
			ti, _ := signals[i]["ts"].(int64)
			tj, _ := signals[j]["ts"].(int64)
			return ti > tj
		})
		return signals[:1]
	}
	return nil
}

func ptrF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---------------------------------------------------------------------------
// Sync events to anomaly_events
// ---------------------------------------------------------------------------

func syncSignalAnomalyEvents(ctx context.Context, store *sqlite.Store, signals []map[string]interface{}, cooldownMinutes int) (int, error) {
	if len(signals) == 0 {
		return 0, nil
	}
	cutoffMs := time.Now().UnixMilli() - int64(max(1, cooldownMinutes))*60*1000
	cooldownMs := int64(max(1, cooldownMinutes)) * 60 * 1000

	latestMap, err := loadLatestEventMap(ctx, store, signals, cutoffMs)
	if err != nil {
		return 0, err
	}

	var values []map[string]interface{}
	for _, item := range signals {
		state, _ := item["signalState"].(string)
		if state != "CONFIRM" && state != "STRONG" && state != "HIGH" {
			continue
		}
		sym, _ := item["symbol"].(string)
		if binance.IsExcludedSymbol(sym) || sym == "" {
			continue
		}
		ts, _ := item["ts"].(int64)
		et, _ := item["eventType"].(string)
		if et == "" {
			et = sigPersistentBuy
		}
		if ts <= 0 {
			continue
		}
		prev := latestMap[sym+"|"+et]
		if prev > 0 && ts-prev < cooldownMs {
			continue
		}

		score, _ := item["score"].(float64)
		direction, _ := item["direction"].(string)
		if direction == "" {
			direction = "buy"
		}
		title := fmt.Sprintf("%s 资金持续吸筹信号", sym)
		if et == sigSingleLarge {
			if direction == "buy" {
				title = fmt.Sprintf("%s 大额主动买入信号", sym)
			} else {
				title = fmt.Sprintf("%s 大额主动卖出信号", sym)
			}
		}
		details := map[string]interface{}{
			"signalState": state, "score": score, "strengthScore": score,
			"zScore": item["zScore"], "netFlow": item["netFlow"],
			"slopeK": item["slopeK"], "slopeR2": item["slopeR2"], "slopeConfirms": item["slopeConfirms"],
		}
		if et == sigSingleLarge {
			details["direction"] = direction
		} else {
			details["buyRatio"] = item["buyRatio"]
			details["largeBuyCount"] = item["largeBuyCount"]
			details["persistentSpanMinutes"] = item["persistentSpanMinutes"]
			details["avgIntervalMinutes"] = item["avgIntervalMinutes"]
			details["distinctTimeBuckets"] = item["distinctTimeBuckets"]
		}
		dj, _ := json.Marshal(details)
		market, _ := item["market"].(string)
		values = append(values, map[string]interface{}{
			"market": market, "symbol": sym, "event_type": et,
			"tf_signal": "1m", "tf_level": nil,
			"event_time_ms": ts, "title": title, "details": string(dj),
		})
		latestMap[sym+"|"+et] = ts
	}
	if len(values) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, values)
}

func syncClimaxEvents(ctx context.Context, store *sqlite.Store, signals []map[string]interface{}, cooldownMinutes int) (int, error) {
	if len(signals) == 0 {
		return 0, nil
	}
	cutoffMs := time.Now().UnixMilli() - int64(max(1, cooldownMinutes))*60000
	cooldownMs := int64(cooldownMinutes) * 60000

	latestMap, err := loadLatestEventMap(ctx, store, signals, cutoffMs)
	if err != nil {
		return 0, err
	}

	var values []map[string]interface{}
	for _, item := range signals {
		state, _ := item["signalState"].(string)
		if state != "CONFIRM" && state != "STRONG" && state != "HIGH" {
			continue
		}
		sym, _ := item["symbol"].(string)
		if binance.IsExcludedSymbol(sym) || sym == "" {
			continue
		}
		ts, _ := item["ts"].(int64)
		et, _ := item["eventType"].(string)
		if ts <= 0 {
			continue
		}
		prev := latestMap[sym+"|"+et]
		if prev > 0 && ts-prev < cooldownMs {
			continue
		}

		direction, _ := item["direction"].(string)
		dirCN := "看多"
		if direction == "short" {
			dirCN = "看空"
		}
		volRatio, _ := item["volumeRatio"].(float64)
		score, _ := item["score"].(float64)
		title := fmt.Sprintf("%s 天量反转%s信号 (%.1fx)", sym, dirCN, volRatio)

		details := map[string]interface{}{
			"signalState": state, "score": score, "strengthScore": score,
			"direction": direction, "climaxVolume": item["climaxVolume"],
			"avgVolume": item["avgVolume"], "volumeRatio": item["volumeRatio"],
			"climaxBuyRatio": item["climaxBuyRatio"], "cascadeBuyRatio": item["cascadeBuyRatio"],
			"cascadeSellNotional": item["cascadeSellNotional"], "obImbalance": item["obImbalance"],
		}
		dj, _ := json.Marshal(details)
		market, _ := item["market"].(string)
		values = append(values, map[string]interface{}{
			"market": market, "symbol": sym, "event_type": et,
			"tf_signal": "1m", "tf_level": nil,
			"event_time_ms": ts, "title": title, "details": string(dj),
		})
		latestMap[sym+"|"+et] = ts
	}
	if len(values) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, values)
}

func loadLatestEventMap(ctx context.Context, store *sqlite.Store, signals []map[string]interface{}, cutoffMs int64) (map[string]int64, error) {
	byMarket := make(map[string][]map[string]interface{})
	for _, s := range signals {
		m, _ := s["market"].(string)
		byMarket[m] = append(byMarket[m], s)
	}
	latestMap := make(map[string]int64)
	for market, rows := range byMarket {
		symSet := make(map[string]struct{})
		etSet := make(map[string]struct{})
		for _, r := range rows {
			s, _ := r["symbol"].(string)
			e, _ := r["eventType"].(string)
			if s != "" {
				symSet[s] = struct{}{}
			}
			if e != "" {
				etSet[e] = struct{}{}
			}
		}
		var syms, ets []string
		for s := range symSet {
			syms = append(syms, s)
		}
		for e := range etSet {
			ets = append(ets, e)
		}
		if len(syms) == 0 || len(ets) == 0 {
			continue
		}

		sql := `SELECT symbol, event_type, MAX(event_time_ms) AS latest
FROM anomaly_events WHERE market = ? AND event_type IN (` + placeholders(len(ets)) + `)
AND symbol IN (` + placeholders(len(syms)) + `) AND event_time_ms >= ?
GROUP BY symbol, event_type`

		args := []interface{}{market}
		for _, e := range ets {
			args = append(args, e)
		}
		for _, s := range syms {
			args = append(args, s)
		}
		args = append(args, cutoffMs)

		type row struct {
			Symbol    string `db:"symbol"`
			EventType string `db:"event_type"`
			Latest    int64  `db:"latest"`
		}
		var results []row
		if err := store.SelectContext(ctx, &results, sql, args...); err != nil {
			return nil, err
		}
		for _, r := range results {
			latestMap[r.Symbol+"|"+r.EventType] = r.Latest
		}
	}
	return latestMap, nil
}

// ---------------------------------------------------------------------------
// Backtest (async run)
// ---------------------------------------------------------------------------

type BacktestRun struct {
	RunID     string                 `json:"runId"`
	Status    string                 `json:"status"`
	CreatedAt int64                  `json:"createdAt"`
	UpdatedAt int64                  `json:"updatedAt"`
	Result    map[string]interface{} `json:"result"`
	Error     string                 `json:"error,omitempty"`
}

var (
	btRuns = make(map[string]*BacktestRun)
	btMu   sync.Mutex
)

func StartBacktestRun(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, marketScope string, days int, params SignalLabParams) string {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	now := time.Now().UnixMilli()
	run := &BacktestRun{RunID: id, Status: "running", CreatedAt: now, UpdatedAt: now}
	btMu.Lock()
	btRuns[id] = run
	btMu.Unlock()

	go func() {
		result, err := runBacktest(ctx, ch, store, marketScope, days, params)
		btMu.Lock()
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
		} else {
			run.Status = "done"
			run.Result = result
		}
		run.UpdatedAt = time.Now().UnixMilli()
		btMu.Unlock()
	}()
	return id
}

func GetBacktestRun(runID string) *BacktestRun {
	btMu.Lock()
	defer btMu.Unlock()
	return btRuns[runID]
}

func runBacktest(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, marketScope string, days int, params SignalLabParams) (map[string]interface{}, error) {
	if days < 1 {
		days = 1
	}
	if days > 30 {
		days = 30
	}
	nowMs := time.Now().UnixMilli()
	startMs := nowMs - int64(days)*24*60*60*1000

	marketResults := make(map[string]interface{})
	var allSamples []map[string]interface{}

	for _, market := range markets(marketScope) {
		symbols := topSymbols(ctx, ch, market, params.SymbolLimit)
		grouped := loadRows(ctx, ch, market, symbols,
			startMs-int64(params.LookbackMinutes)*60*1000, nowMs, params.Bucket)
		mcapMap := loadMarketCaps(ctx, ch, symbols)

		bktMin := max(1, slBucketMinutes(params.Bucket))
		signalCount, winCount := 0, 0
		var returns, drawdowns []float64
		var sampleEvents []map[string]interface{}

		for sym, rows := range grouped {
			if len(rows) < max(30, params.LookbackMinutes/bktMin/3) {
				continue
			}
			sigs := scanSymbolSignals(sym, market, rows, params, mcapMap[sym])
			if len(sigs) == 0 {
				continue
			}
			idxMap := make(map[int64]int)
			for i, r := range rows {
				idxMap[r.Ts] = i
			}
			for _, sig := range sigs {
				ts, _ := sig["ts"].(int64)
				if ts < startMs {
					continue
				}
				idx, ok := idxMap[ts]
				if !ok {
					continue
				}
				ret, dd := evaluateSignalReturn(rows, idx, params.ForecastHorizonMinutes)
				if ret == nil {
					continue
				}
				signalCount++
				returns = append(returns, *ret)
				if dd != nil {
					drawdowns = append(drawdowns, *dd)
				}
				if *ret > 0 {
					winCount++
				}
				if len(sampleEvents) < 200 {
					sampleEvents = append(sampleEvents, map[string]interface{}{
						"market": market, "symbol": sym, "ts": ts,
						"eventType": sig["eventType"], "direction": sig["direction"],
						"score": sig["score"], "signalState": sig["signalState"],
						"retH": math.Round(*ret*1e6) / 1e6,
						"maxDrawdown": math.Round(ptrFNil(dd)*1e6) / 1e6,
						"zScore": sig["zScore"], "buyRatio": sig["buyRatio"],
						"largeBuyCount": sig["largeBuyCount"],
						"slopeK": sig["slopeK"], "slopeR2": sig["slopeR2"],
						"slopeConfirms": sig["slopeConfirms"],
					})
				}
			}
		}

		avgRet, avgDD, winRate := 0.0, 0.0, 0.0
		if len(returns) > 0 {
			for _, r := range returns {
				avgRet += r
			}
			avgRet /= float64(len(returns))
		}
		if len(drawdowns) > 0 {
			for _, d := range drawdowns {
				avgDD += d
			}
			avgDD /= float64(len(drawdowns))
		}
		if signalCount > 0 {
			winRate = float64(winCount) / float64(signalCount)
		}

		marketResults[market] = map[string]interface{}{
			"signals": signalCount, "wins": winCount,
			"winRate": math.Round(winRate*10000) / 10000,
			"avgReturn": math.Round(avgRet*1e6) / 1e6,
			"avgDrawdown": math.Round(avgDD*1e6) / 1e6,
			"symbols": len(symbols),
		}
		allSamples = append(allSamples, sampleEvents...)
	}

	totalSignals, totalWins := 0, 0
	for _, v := range marketResults {
		m, _ := v.(map[string]interface{})
		s, _ := m["signals"].(int)
		w, _ := m["wins"].(int)
		totalSignals += s
		totalWins += w
	}
	totalWinRate := 0.0
	if totalSignals > 0 {
		totalWinRate = float64(totalWins) / float64(totalSignals)
	}

	sort.Slice(allSamples, func(i, j int) bool {
		si, _ := allSamples[i]["score"].(float64)
		sj, _ := allSamples[j]["score"].(float64)
		return si > sj
	})
	if len(allSamples) > 200 {
		allSamples = allSamples[:200]
	}

	return map[string]interface{}{
		"market": marketScope, "days": days,
		"params": map[string]interface{}{
			"zThreshold": params.ZThreshold, "lookbackMinutes": params.LookbackMinutes,
			"detectionWindowMinutes": params.DetectionWindowMinutes,
			"minLargeCount": params.MinLargeCount, "buyRatioThreshold": params.BuyRatioThreshold,
			"minPersistentSpanMinutes": params.MinPersistentSpanMinutes,
			"minAvgIntervalMinutes": params.MinAvgIntervalMinutes,
			"minDistinctTimeBuckets": params.MinDistinctTimeBuckets,
			"forecastHorizonMinutes": params.ForecastHorizonMinutes,
			"cooldownMinutes": params.CooldownMinutes, "symbolLimit": params.SymbolLimit,
		},
		"summary": map[string]interface{}{
			"signals": totalSignals, "wins": totalWins,
			"winRate": math.Round(totalWinRate*10000) / 10000,
		},
		"markets": marketResults, "samples": allSamples,
		"eventType": sigPersistentBuy, "ts": time.Now().UnixMilli(),
	}, nil
}

func evaluateSignalReturn(rows []bucketPoint, signalIdx, horizonMin int) (*float64, *float64) {
	horizon := max(1, horizonMin)
	endIdx := signalIdx + horizon
	if signalIdx < 0 || signalIdx >= len(rows) || endIdx >= len(rows) {
		return nil, nil
	}
	entry := rows[signalIdx].Close
	future := rows[endIdx].Close
	if entry <= 0 || future <= 0 {
		return nil, nil
	}
	ret := (future - entry) / entry
	minClose := entry
	for _, r := range rows[signalIdx+1 : endIdx+1] {
		if r.Close > 0 && r.Close < minClose {
			minClose = r.Close
		}
	}
	dd := 0.0
	if entry > 0 {
		dd = (minClose - entry) / entry
	}
	return &ret, &dd
}

func ptrFNil(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
