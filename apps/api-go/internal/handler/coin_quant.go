package handler

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/model"
)

const (
	fundRepairCooldownMs int64 = 90 * 1000
	fundRepairMaxRangeMs int64 = 6 * 60 * 60 * 1000
)

var fundRepairState = struct {
	mu           sync.Mutex
	lastBySymbol map[string]int64
}{
	lastBySymbol: map[string]int64{},
}

func getFundRepairState(symbol string, nowMs int64) (int64, int64, bool) {
	key := strings.ToUpper(strings.TrimSpace(symbol))
	fundRepairState.mu.Lock()
	last := fundRepairState.lastBySymbol[key]
	fundRepairState.mu.Unlock()

	remaining := int64(0)
	if last > 0 {
		remaining = fundRepairCooldownMs - (nowMs - last)
		if remaining < 0 {
			remaining = 0
		}
	}
	return last, remaining, remaining == 0
}

func markFundRepair(symbol string, ts int64) {
	key := strings.ToUpper(strings.TrimSpace(symbol))
	if key == "" {
		return
	}
	fundRepairState.mu.Lock()
	fundRepairState.lastBySymbol[key] = ts
	fundRepairState.mu.Unlock()
}

func parseHHMM(v string) (int, bool) {
	s := strings.TrimSpace(v)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(parts[0])
	mm, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func parseKlineFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func parseKlineInt64(v interface{}) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return int64(f), true
	default:
		return 0, false
	}
}

func normalizeRepairMarkets(items []string) []string {
	if len(items) == 0 {
		return []string{"spot", "swap"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, it := range items {
		v := strings.ToLower(strings.TrimSpace(it))
		if v != "spot" && v != "swap" {
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return []string{"spot", "swap"}
	}
	return out
}

func buildRepairTradeRows(market, symbol string, klines [][]interface{}, startMs, endMs, lastClosedStartMs int64) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(klines))
	for _, k := range klines {
		if len(k) < 11 {
			continue
		}
		openTime, ok := parseKlineInt64(k[0])
		if !ok {
			continue
		}
		if openTime < startMs || openTime > endMs || openTime > lastClosedStartMs {
			continue
		}
		closeTime, ok := parseKlineInt64(k[6])
		if !ok {
			continue
		}
		openPrice, ok1 := parseKlineFloat(k[1])
		highPrice, ok2 := parseKlineFloat(k[2])
		lowPrice, ok3 := parseKlineFloat(k[3])
		closePrice, ok4 := parseKlineFloat(k[4])
		quoteNotional, ok5 := parseKlineFloat(k[7])
		tradeCount, ok6 := parseKlineInt64(k[8])
		takerBuyNotional, ok7 := parseKlineFloat(k[10])
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
			continue
		}
		if quoteNotional <= 0 || tradeCount <= 0 {
			continue
		}
		takerSellNotional := quoteNotional - takerBuyNotional
		if takerSellNotional < 0 {
			takerSellNotional = 0
		}
		rows = append(rows, map[string]interface{}{
			"market":              market,
			"symbol":              symbol,
			"bucket":              "1m",
			"bucket_start_ms":     openTime,
			"taker_buy_notional":  takerBuyNotional,
			"taker_sell_notional": takerSellNotional,
			"quote_notional":      quoteNotional,
			"trade_count":         tradeCount,
			"first_trade_ms":      openTime,
			"last_trade_ms":       closeTime,
			"open_price":          openPrice,
			"close_price":         closePrice,
			"high_price":          highPrice,
			"low_price":           lowPrice,
		})
	}
	return rows
}

// ---------------------------------------------------------------------------
// /api/coin/detail/fund/snapshot-health
// ---------------------------------------------------------------------------

func handleFundSnapshotHealth(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		timeMode := c.DefaultQuery("timeMode", "utc")
		if timeMode != "local" {
			timeMode = "utc"
		}
		tzOffsetMin := queryInt(c, "tzOffsetMin", 0, -720, 720)
		nowMs := time.Now().UnixMilli()
		freshWindowSec := 600
		checkWindowHours := 2

		recentStart := nowMs - int64(checkWindowHours)*3600000

		// 1m data for last 2h per market
		markets := []string{"spot", "swap"}
		latest1m := map[string]interface{}{"spot": nil, "swap": nil}
		healthy := true
		reason := "ok"

		var staleMkts []string
		var chErr error
		for _, mkt := range markets {
			rows, err := d.CH.QueryTradeBuckets(ctx, mkt, symbol, nil, "1m", recentStart, 0, "desc", 1)
			if err != nil {
				chErr = err
				continue
			}
			if len(rows) > 0 {
				latest1m[mkt] = rows[0].BucketStartMs
				if nowMs-rows[0].BucketStartMs > int64(freshWindowSec)*1000 {
					staleMkts = append(staleMkts, mkt)
				}
			}
		}
		if chErr != nil && latest1m["spot"] == nil && latest1m["swap"] == nil {
			healthy = false
			reason = "backend_error"
		} else if len(staleMkts) > 0 {
			healthy = false
			reason = "stale_1m_" + strings.Join(staleMkts, "_")
		}
		if latest1m["spot"] == nil && latest1m["swap"] == nil {
			healthy = false
			reason = "no_1m_rows"
		}

		timezone := "UTC"
		if timeMode == "local" {
			timezone = "LOCAL"
		}
		lastRepairAtMs, repairCooldownRemainingMs, canTriggerRepair := getFundRepairState(symbol, nowMs)
		var lastRepairValue interface{} = nil
		if lastRepairAtMs > 0 {
			lastRepairValue = lastRepairAtMs
		}

		c.JSON(http.StatusOK, gin.H{
			"timezone":         timezone,
			"timeMode":         timeMode,
			"tzOffsetMin":      tzOffsetMin,
			"symbol":           symbol,
			"healthy":          healthy,
			"reason":           reason,
			"latest1mByMarket": latest1m,
			"freshWindowSec":   freshWindowSec,
			"checkWindowHours": checkWindowHours,
			"h1m1Consistency": gin.H{
				"compared": 0,
				"mismatch": 0,
			},
			"lastRepairAtMs":            lastRepairValue,
			"repairCooldownMs":          fundRepairCooldownMs,
			"repairCooldownRemainingMs": repairCooldownRemainingMs,
			"canTriggerRepair":          canTriggerRepair,
		})
	}
}

// ---------------------------------------------------------------------------
// /api/coin/detail/fund/repair
// ---------------------------------------------------------------------------

func handleFundRepair(d *Deps) gin.HandlerFunc {
	type reqBody struct {
		Symbol      string   `json:"symbol"`
		Day         string   `json:"day"`
		StartHM     string   `json:"startHm"`
		EndHM       string   `json:"endHm"`
		StartAtMs   *int64   `json:"startAtMs"`
		EndAtMs     *int64   `json:"endAtMs"`
		TimeMode    string   `json:"timeMode"`
		TzOffsetMin int      `json:"tzOffsetMin"`
		Markets     []string `json:"markets"`
		Chunked     bool     `json:"chunked"`
	}
	type marketResult struct {
		Market       string `json:"market"`
		KlineCount   int    `json:"klineCount"`
		InsertedRows int    `json:"insertedRows"`
		Error        string `json:"error,omitempty"`
	}

	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		var req reqBody
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body"})
			return
		}

		symbol := strings.ToUpper(strings.TrimSpace(req.Symbol))
		if symbol == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
			return
		}
		timeMode := strings.ToLower(strings.TrimSpace(req.TimeMode))
		if timeMode != "local" {
			timeMode = "utc"
		}
		tzOffsetMin := req.TzOffsetMin
		if tzOffsetMin < -720 {
			tzOffsetMin = -720
		}
		if tzOffsetMin > 720 {
			tzOffsetMin = 720
		}
		offsetMs := int64(0)
		if timeMode == "local" {
			offsetMs = int64(tzOffsetMin) * 60 * 1000
		}

		const minuteMs int64 = 60 * 1000
		const dayMs int64 = 24 * 60 * 60 * 1000
		nowMs := time.Now().UnixMilli()
		todayStartMs := floorBucketStartWithOffset(nowMs, dayMs, offsetMs)
		minAllowedDayStartMs := todayStartMs - 29*dayMs
		day := strings.TrimSpace(req.Day)
		startMs := int64(0)
		endMs := int64(0)
		if req.StartAtMs != nil || req.EndAtMs != nil {
			if req.StartAtMs == nil || req.EndAtMs == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "startAtMs and endAtMs must be provided together"})
				return
			}
			startMs = *req.StartAtMs
			endMs = *req.EndAtMs
			if startMs <= 0 || endMs <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid startAtMs/endAtMs"})
				return
			}
			if endMs < startMs {
				c.JSON(http.StatusBadRequest, gin.H{"error": "endAtMs must be >= startAtMs"})
				return
			}
			if startMs < minAllowedDayStartMs {
				c.JSON(http.StatusBadRequest, gin.H{"error": "startAtMs out of allowed range"})
				return
			}
		} else {
			if day == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "day required, use YYYY-MM-DD"})
				return
			}
			dayParsed, err := time.Parse("2006-01-02", day)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid day, use YYYY-MM-DD"})
				return
			}
			startMin, ok := parseHHMM(req.StartHM)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid startHm, use HH:MM"})
				return
			}
			endMin, ok := parseHHMM(req.EndHM)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid endHm, use HH:MM"})
				return
			}
			if endMin < startMin {
				c.JSON(http.StatusBadRequest, gin.H{"error": "endHm must be >= startHm"})
				return
			}
			dayStartMs := dayParsed.UnixMilli() + offsetMs
			if dayStartMs < minAllowedDayStartMs || dayStartMs > todayStartMs {
				c.JSON(http.StatusBadRequest, gin.H{"error": "day out of allowed range"})
				return
			}
			startMs = dayStartMs + int64(startMin)*minuteMs
			endMs = dayStartMs + int64(endMin)*minuteMs
		}
		if endMs-startMs > fundRepairMaxRangeMs && !req.Chunked {
			c.JSON(http.StatusBadRequest, gin.H{"error": "time range too large, max 6h"})
			return
		}
		lastClosedStartMs := floorBucketStart(nowMs, minuteMs) - minuteMs
		if endMs > lastClosedStartMs {
			endMs = lastClosedStartMs
		}
		if startMs > endMs {
			c.JSON(http.StatusBadRequest, gin.H{"error": "selected range has no closed 1m buckets yet"})
			return
		}
		if day == "" {
			day = time.UnixMilli(startMs - offsetMs).UTC().Format("2006-01-02")
		}
		type repairSegment struct {
			StartMs int64
			EndMs   int64
		}
		segments := make([]repairSegment, 0, 4)
		if req.Chunked && endMs-startMs > fundRepairMaxRangeMs {
			for segStart := startMs; segStart <= endMs; {
				segEnd := segStart + fundRepairMaxRangeMs
				if segEnd > endMs {
					segEnd = endMs
				}
				segments = append(segments, repairSegment{StartMs: segStart, EndMs: segEnd})
				segStart = segEnd + minuteMs
			}
		} else {
			segments = append(segments, repairSegment{StartMs: startMs, EndMs: endMs})
		}

		lastRepairAtMs, cooldownRemainingMs, canTriggerRepair := getFundRepairState(symbol, nowMs)
		if !canTriggerRepair {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":                     "repair in cooldown",
				"symbol":                    symbol,
				"lastRepairAtMs":            lastRepairAtMs,
				"repairCooldownMs":          fundRepairCooldownMs,
				"repairCooldownRemainingMs": cooldownRemainingMs,
				"canTriggerRepair":          false,
			})
			return
		}

		markets := normalizeRepairMarkets(req.Markets)
		results := make([]marketResult, 0, len(markets))
		totalInserted := 0
		ctx := c.Request.Context()
		for _, market := range markets {
			item := marketResult{Market: market}
			errs := make([]string, 0, 2)
			for _, seg := range segments {
				klines, err := d.BN.GetKlinesRange(ctx, market, symbol, "1m", seg.StartMs, seg.EndMs, 1000)
				if err != nil {
					errs = append(errs, err.Error())
					continue
				}
				item.KlineCount += len(klines)
				repairRows := buildRepairTradeRows(market, symbol, klines, seg.StartMs, seg.EndMs, lastClosedStartMs)
				if len(repairRows) == 0 {
					continue
				}
				inserted, err := d.CH.InsertTradeBuckets(ctx, repairRows)
				if err != nil {
					errs = append(errs, err.Error())
					continue
				}
				item.InsertedRows += inserted
				totalInserted += inserted
			}
			if len(errs) > 0 {
				item.Error = strings.Join(errs, " | ")
			}
			results = append(results, item)
		}

		if totalInserted > 0 {
			markFundRepair(symbol, nowMs)
		}
		lastRepairAtMs, cooldownRemainingMs, canTriggerRepair = getFundRepairState(symbol, time.Now().UnixMilli())
		var lastRepairValue interface{} = nil
		if lastRepairAtMs > 0 {
			lastRepairValue = lastRepairAtMs
		}

		statusCode := http.StatusOK
		if totalInserted == 0 {
			allFailed := true
			for _, it := range results {
				if it.Error == "" {
					allFailed = false
					break
				}
			}
			if allFailed {
				statusCode = http.StatusBadGateway
			}
		}
		c.JSON(statusCode, gin.H{
			"symbol":                    symbol,
			"day":                       day,
			"timeMode":                  timeMode,
			"tzOffsetMin":               tzOffsetMin,
			"startMs":                   startMs,
			"endMs":                     endMs,
			"chunked":                   len(segments) > 1,
			"segmentCount":              len(segments),
			"requestedMarkets":          markets,
			"results":                   results,
			"insertedRows":              totalInserted,
			"lastRepairAtMs":            lastRepairValue,
			"repairCooldownMs":          fundRepairCooldownMs,
			"repairCooldownRemainingMs": cooldownRemainingMs,
			"canTriggerRepair":          canTriggerRepair,
		})
	}
}

// ---------------------------------------------------------------------------
// /api/coin/detail/quant-dashboard
// ---------------------------------------------------------------------------

var bucketMinutesMap = map[string]int{"1m": 1, "15m": 15, "1h": 60, "4h": 240, "1d": 1440}

func handleQuantDashboard(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		market := queryMarket(c)
		bucket := c.DefaultQuery("bucket", "1m")
		if _, ok := bucketMinutesMap[bucket]; !ok {
			bucket = "1m"
		}
		limit := queryInt(c, "limit", 120, 24, 720)

		bucketMin := bucketMinutesMap[bucket]
		vwapLookback := 24 * 60 / bucketMin
		if vwapLookback < 6 {
			vwapLookback = 6
		}
		if vwapLookback > 240 {
			vwapLookback = 240
		}

		// market fallback: if spot requested but not available, use swap
		requestedMarket := market
		marketFallback := false
		if market == "spot" {
			pairs, _ := d.BN.GetPairs(ctx, "spot")
			found := false
			for _, p := range pairs {
				if p == symbol {
					found = true
					break
				}
			}
			if !found {
				market = "swap"
				marketFallback = true
			}
		}

		fetchLimit := limit + vwapLookback
		rows, err := d.CH.QueryTradeBuckets(ctx, market, symbol, nil, bucket, 0, 0, "desc", fetchLimit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// reverse to chronological order
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}

		type candle struct {
			Ts       int64    `json:"ts"`
			O        *float64 `json:"o"`
			H        *float64 `json:"h"`
			L        *float64 `json:"l"`
			C        *float64 `json:"c"`
			VolBuy   float64  `json:"vol_buy"`
			VolSell  float64  `json:"vol_sell"`
			VolTotal float64  `json:"vol_total"`
			Delta    float64  `json:"delta"`
			CVD      float64  `json:"cvd"`
			VWAP     *float64 `json:"vwap"`
			Count    int64    `json:"count"`
		}

		// compute VWAP + CVD
		items := make([]candle, 0, len(rows))
		cvd := 0.0
		for i, r := range rows {
			delta := r.TakerBuyNotional - r.TakerSellNotional
			cvd += delta

			var vwap *float64
			if r.QuoteNotional > 0 {
				// rolling VWAP over lookback window
				start := i - vwapLookback
				if start < 0 {
					start = 0
				}
				var volSum, qvSum float64
				for j := start; j <= i; j++ {
					qvSum += rows[j].QuoteNotional
					if rows[j].ClosePrice != nil && *rows[j].ClosePrice > 0 {
						volSum += rows[j].QuoteNotional / *rows[j].ClosePrice
					}
				}
				if volSum > 0 {
					v := qvSum / volSum
					vwap = &v
				}
			}

			items = append(items, candle{
				Ts: r.BucketStartMs, O: r.OpenPrice, H: r.HighPrice, L: r.LowPrice, C: r.ClosePrice,
				VolBuy: r.TakerBuyNotional, VolSell: r.TakerSellNotional, VolTotal: r.QuoteNotional,
				Delta: delta, CVD: cvd, VWAP: vwap, Count: r.TradeCount,
			})
		}

		// trim to last `limit` items with non-null OHLC
		output := make([]candle, 0)
		for _, it := range items {
			if it.O != nil {
				output = append(output, it)
			}
		}
		if len(output) > limit {
			output = output[len(output)-limit:]
		}
		// re-base CVD to start from 0
		if len(output) > 0 {
			base := output[0].CVD - (output[0].VolBuy - output[0].VolSell)
			for i := range output {
				output[i].CVD -= base
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":          symbol,
			"market":          market,
			"requestedMarket": requestedMarket,
			"marketFallback":  marketFallback,
			"bucket":          bucket,
			"items":           output,
		})
	}
}

// ---------------------------------------------------------------------------
// /api/coin/detail/quant-mlf-compare
// ---------------------------------------------------------------------------

func handleQuantMLFCompare(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		bucket := c.DefaultQuery("bucket", "1h")
		if _, ok := bucketMinutesMap[bucket]; !ok {
			bucket = "1h"
		}
		lookbackHours := queryInt(c, "lookbackHours", 24, 4, 168)
		domThresh := queryFloat(c, "dominanceThreshold", 0.0)

		bucketMin := bucketMinutesMap[bucket]
		lookbackBuckets := lookbackHours * 60 / bucketMin
		if lookbackBuckets < 1 {
			lookbackBuckets = 1
		}

		type mktResult struct {
			Market    string   `json:"market"`
			Available bool     `json:"available"`
			Close     *float64 `json:"closePrice"`
			WallRatio float64  `json:"wallStrengthRatio"`
			BuyWall   float64  `json:"buyWallUsd"`
			SellWall  float64  `json:"sellWallUsd"`
			CVDDelta  float64  `json:"cvdDelta"`
			CVDNorm   float64  `json:"cvdNorm"`
			Count     int      `json:"bucketCount"`
		}

		fetchOne := func(mkt string) mktResult {
			r := mktResult{Market: mkt}
			pairs, _ := d.BN.GetPairs(ctx, mkt)
			for _, p := range pairs {
				if p == symbol {
					r.Available = true
					break
				}
			}
			if !r.Available {
				return r
			}

			rows, _ := d.CH.QueryTradeBuckets(ctx, mkt, symbol, nil, bucket, 0, 0, "desc", lookbackBuckets)
			r.Count = len(rows)
			var volSum float64
			for _, row := range rows {
				r.CVDDelta += row.TakerBuyNotional - row.TakerSellNotional
				volSum += row.QuoteNotional
			}
			if len(rows) > 0 {
				r.Close = rows[0].ClosePrice
			}
			if volSum > 0 {
				r.CVDNorm = r.CVDDelta / volSum
			}

			// orderbook wall strength
			depthLimit := 1000
			if mkt == "spot" {
				depthLimit = 5000
			}
			ob, err := d.BN.GetOrderbookDepth(ctx, mkt, symbol, depthLimit)
			if err == nil && r.Close != nil {
				mid := *r.Close
				pctRange := 0.05
				lo := mid * (1 - pctRange)
				hi := mid * (1 + pctRange)
				r.BuyWall = sumWall(ob["bids"], lo, hi)
				r.SellWall = sumWall(ob["asks"], lo, hi)
				if r.SellWall > 0 {
					r.WallRatio = r.BuyWall / r.SellWall
				}
			}
			return r
		}

		var spot, swap mktResult
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); spot = fetchOne("spot") }()
		go func() { defer wg.Done(); swap = fetchOne("swap") }()
		wg.Wait()

		dominanceFactor := spot.CVDNorm - swap.CVDNorm
		wallDiff := spot.WallRatio - swap.WallRatio
		state := "UNKNOWN"
		if spot.Available && swap.Available {
			if dominanceFactor > domThresh {
				state = "SPOT_DOMINANT"
			} else if dominanceFactor < -domThresh {
				state = "SWAP_DOMINANT"
			} else {
				state = "NEUTRAL"
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":                symbol,
			"bucket":                bucket,
			"lookbackHours":         lookbackHours,
			"dominanceThreshold":    domThresh,
			"spot":                  spot,
			"swap":                  swap,
			"spotDominanceFactor":   dominanceFactor,
			"wallStrengthRatioDiff": wallDiff,
			"dominanceState":        state,
			"ts":                    time.Now().UnixMilli(),
		})
	}
}

func sumWall(raw interface{}, lo, hi float64) float64 {
	levels, ok := raw.([]interface{})
	if !ok {
		return 0
	}
	var total float64
	for _, lv := range levels {
		arr, ok := lv.([]interface{})
		if !ok || len(arr) < 2 {
			continue
		}
		price := parseF(arr[0])
		qty := parseF(arr[1])
		if price >= lo && price <= hi {
			total += price * qty
		}
	}
	return total
}

func parseF(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// /api/coin/detail/orderbook/whale-radar
// ---------------------------------------------------------------------------

var whaleTierMin = map[string]float64{
	"BTCUSDT":  5_000_000,
	"ETHUSDT":  5_000_000,
	"SOLUSDT":  1_000_000,
	"BNBUSDT":  1_000_000,
	"DOGEUSDT": 1_000_000,
	"XRPUSDT":  1_000_000,
}

const whaleDefaultMin = 200_000

func handleWhaleRadar(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		lookbackMin := queryInt(c, "lookbackMinutes", 240, 30, 1440)
		topK := queryInt(c, "topK", 20, 5, 100)

		nowMs := time.Now().UnixMilli()
		startMs := nowMs - int64(lookbackMin)*60000

		// whale threshold
		minLimit := float64(whaleDefaultMin)
		if v, ok := whaleTierMin[symbol]; ok {
			minLimit = v
		}
		// check coin_info override
		var coinInfo model.CoinInfo
		if err := d.Store.GetContext(ctx, &coinInfo, `SELECT * FROM coin_info WHERE symbol = ?`, symbol); err == nil && coinInfo.WhaleMinVal != nil {
			minLimit = *coinInfo.WhaleMinVal
		}
		spoofLimit := minLimit * 1.5

		// fetch heatmap snapshots
		var heatRows []model.OrderbookHeatmapSnapshot
		d.Store.SelectContext(ctx, &heatRows, `SELECT * FROM orderbook_heatmap_1m WHERE symbol = ? AND bucket_start_ms >= ? AND bucket_start_ms <= ? ORDER BY bucket_start_ms ASC`,
			symbol, startMs, nowMs)

		sourceMarkets := map[string]bool{}
		type priceKey struct {
			price float64
			side  string
		}
		type wallState struct {
			latestValue float64
			lastSeenTs  int64
			durationMin int
			timestamps  []int64
		}
		walls := map[priceKey]*wallState{}

		for _, r := range heatRows {
			sourceMarkets[r.Market] = true
			k := priceKey{r.PriceBin, strings.ToUpper(r.Side)}
			ws, exists := walls[k]
			if !exists {
				ws = &wallState{}
				walls[k] = ws
			}
			ws.latestValue = r.Intensity
			ws.lastSeenTs = r.BucketStartMs
			ws.timestamps = append(ws.timestamps, r.BucketStartMs)
		}

		// calculate duration from timestamps
		for _, ws := range walls {
			if len(ws.timestamps) < 2 {
				ws.durationMin = len(ws.timestamps)
				continue
			}
			dur := 0
			for i := len(ws.timestamps) - 1; i > 0; i-- {
				gap := ws.timestamps[i] - ws.timestamps[i-1]
				if gap <= 120000 { // 2 minutes tolerance
					dur++
				} else {
					break
				}
			}
			ws.durationMin = dur + 1
		}

		// get latest price
		ticker, _ := d.BN.GetTicker24h(ctx, "swap", symbol)
		latestPrice := 0.0
		if ticker != nil {
			latestPrice = parseF(ticker["lastPrice"])
		}

		type radarItem struct {
			Price       float64 `json:"price"`
			DistancePct float64 `json:"distancePct"`
			Value       float64 `json:"value"`
			DurationMin int     `json:"durationMin"`
			Side        string  `json:"side"`
			Nature      string  `json:"nature"`
			Label       string  `json:"label"`
			LastSeenTs  int64   `json:"lastSeenTs"`
		}

		items := make([]radarItem, 0)
		for k, ws := range walls {
			if ws.latestValue < minLimit {
				continue
			}
			nature := "WATCH"
			label := "🐋 大户关注"
			if ws.latestValue >= spoofLimit && ws.durationMin <= 1 {
				nature = "SPOOF"
				label = "👻 疑似骗线"
			} else if ws.durationMin >= 60 {
				nature = "REAL"
				label = "🛡 铁底/铁顶"
			}

			dist := 0.0
			if latestPrice > 0 {
				dist = (k.price - latestPrice) / latestPrice * 100
			}
			items = append(items, radarItem{
				Price: k.price, DistancePct: math.Round(dist*1000) / 1000,
				Value: ws.latestValue, DurationMin: ws.durationMin,
				Side: k.side, Nature: nature, Label: label, LastSeenTs: ws.lastSeenTs,
			})
		}

		// sort: REAL > SPOOF > WATCH, then value desc
		natureOrder := map[string]int{"REAL": 0, "SPOOF": 1, "WATCH": 2}
		sort.Slice(items, func(i, j int) bool {
			if natureOrder[items[i].Nature] != natureOrder[items[j].Nature] {
				return natureOrder[items[i].Nature] < natureOrder[items[j].Nature]
			}
			return items[i].Value > items[j].Value
		})
		if len(items) > topK {
			items = items[:topK]
		}

		marketLabel := "swap"
		smList := make([]string, 0)
		for m := range sourceMarkets {
			smList = append(smList, m)
		}
		sort.Strings(smList)
		if len(smList) > 1 {
			marketLabel = strings.Join(smList, "+")
		} else if len(smList) == 1 {
			marketLabel = smList[0]
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":          symbol,
			"market":          marketLabel,
			"sourceMarkets":   smList,
			"lookbackMinutes": lookbackMin,
			"minLimit":        minLimit,
			"spoofLimit":      spoofLimit,
			"latestPrice":     latestPrice,
			"snapshotTs":      nowMs,
			"items":           items,
			"ts":              nowMs,
		})
	}
}
