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
			"lastRepairAtMs":            nil,
			"repairCooldownMs":          90 * 1000,
			"repairCooldownRemainingMs": 0,
			"canTriggerRepair":          true,
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
