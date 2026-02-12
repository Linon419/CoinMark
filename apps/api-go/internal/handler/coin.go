package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
)

func registerCoinRoutes(g *gin.RouterGroup, d *Deps) {
	coin := g.Group("/coin/detail")
	coin.GET("/basic", handleCoinBasic(d))
	coin.GET("/flows/hourly", handleCoinFlows(d, "1h"))
	coin.GET("/flows/daily", handleCoinFlows(d, "1d"))
	coin.GET("/oi/hourly", handleCoinOI(d, "1h"))
	coin.GET("/oi/daily", handleCoinOI(d, "1d"))
	coin.GET("/lsr/hourly", handleCoinLSR(d))
	coin.GET("/sr/short", handleCoinSR(d))
	coin.GET("/fund/snapshots", handleFundSnapshots(d))
	coin.GET("/fund/intraday", handleFundIntraday(d))
	coin.GET("/orderbook/intraday", handleOBIntraday(d))
	coin.GET("/orderbook/absorption-signal", handleOBAbsorptionSignal(d))
	coin.GET("/orderbook/spot-heatmap", handleSpotHeatmap(d))
	coin.GET("/orderbook/whale-radar", handleWhaleRadar(d))
	coin.GET("/fund/snapshot-health", handleFundSnapshotHealth(d))
	coin.GET("/quant-dashboard", handleQuantDashboard(d))
	coin.GET("/quant-mlf-compare", handleQuantMLFCompare(d))
	coin.GET("/recent", handleRecent(d))
	coin.GET("/recent/daily", handleRecentDaily(d))
}

func requireSymbol(c *gin.Context) string {
	s := strings.ToUpper(c.Query("symbol"))
	if s == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
	}
	return s
}

func handleCoinBasic(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestedMarket := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		timeMode := c.DefaultQuery("timeMode", "utc")
		tzOffsetMin := queryInt(c, "tzOffsetMin", 0, -720, 720)

		// market fallback
		market := requestedMarket
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

		asset := strings.TrimSuffix(symbol, "USDT")

		ticker, tickerErr := d.BN.GetTicker24h(ctx, market, symbol)
		warnings := make([]string, 0)
		if tickerErr != nil {
			ticker = nil
			warnings = append(warnings, "ticker unavailable")
		}

		num := func(key string) *float64 {
			if ticker == nil {
				return nil
			}
			v, ok := ticker[key]
			if !ok || v == nil {
				return nil
			}
			f := parseF(v)
			return &f
		}

		var eventTimeMs int64
		if ct := num("closeTime"); ct != nil {
			eventTimeMs = int64(*ct)
		} else {
			eventTimeMs = time.Now().UnixMilli()
		}

		basic := gin.H{
			"symbol":                symbol,
			"asset":                 asset,
			"market":                market,
			"lastPrice":             num("lastPrice"),
			"priceChangePercent24h": num("priceChangePercent"),
			"highPrice24h":          num("highPrice"),
			"lowPrice24h":           num("lowPrice"),
			"quoteVolume24h":        num("quoteVolume"),
			"eventTimeMs":           eventTimeMs,
			"source":                "binance_ticker_24h",
		}

		status, statusErr := d.BN.GetSymbolStatus(ctx, market, symbol)
		if statusErr != nil {
			warnings = append(warnings, "symbol status unavailable")
		}

		var funding interface{}
		var openInterest interface{}
		var fundingRow *model.CHFundingRow
		var oiRow *model.CHOIRow
		if market == "swap" && d.CH != nil {
			if fr, e := d.CH.QueryFundingBySymbol(ctx, symbol); e == nil && fr != nil {
				fundingRow = fr
				funding = gin.H{
					"lastFundingRate": fr.LastFundingRate,
					"markPrice":       fr.MarkPrice,
					"timeMs":          fr.EventTimeMs,
				}
			}
			if oi, e := d.CH.QueryOIBySymbol(ctx, symbol); e == nil && oi != nil {
				oiRow = oi
				openInterest = gin.H{
					"openInterest":  oi.OpenInterest,
					"markPrice":     oi.MarkPrice,
					"oiNotionalUsd": oi.OINotionalUSD,
					"timeMs":        oi.EventTimeMs,
				}
			}
		}

		var marketCap interface{}
		var capRow *model.CHMarketCapRow
		if d.CH != nil {
			if cap, e := d.CH.QueryMarketCapByAsset(ctx, asset); e == nil && cap != nil {
				capRow = cap
				marketCap = gin.H{
					"priceUsd":          cap.PriceUSD,
					"circulatingSupply": cap.CirculatingSupply,
					"marketCapUsd":      cap.MarketCapUSD,
					"source":            cap.Source,
					"timeMs":            cap.EventTimeMs,
				}
			}
		}

		lastPrice := num("lastPrice")
		priceChangePct := num("priceChangePercent")
		lowPrice := num("lowPrice")
		highPrice := num("highPrice")
		quoteVolume := num("quoteVolume")
		formatPtr := func(v *float64, fmtStr string) string {
			if v == nil {
				return "-"
			}
			return fmt.Sprintf(fmtStr, *v)
		}
		rangeLabel := "-"
		if lowPrice != nil && highPrice != nil {
			rangeLabel = fmt.Sprintf("%s ~ %s", formatPtr(lowPrice, "%.6f"), formatPtr(highPrice, "%.6f"))
		}
		fundingRateLabel := "-"
		if fundingRow != nil {
			fundingRateLabel = fmt.Sprintf("%+.4f%%", fundingRow.LastFundingRate*100)
		}
		oiLabel := "-"
		oiNotionalLabel := "-"
		if oiRow != nil {
			oiLabel = fmt.Sprintf("%.2f", oiRow.OpenInterest)
			oiNotionalLabel = fmt.Sprintf("%.0f", oiRow.OINotionalUSD)
		}
		marketCapLabel := "-"
		if capRow != nil {
			marketCapLabel = fmt.Sprintf("%.0f", capRow.MarketCapUSD)
		}
		basicKvp := []gin.H{
			{"label": "最新价", "value": formatPtr(lastPrice, "%.6f")},
			{"label": "24h涨跌", "value": formatPtr(priceChangePct, "%+.2f%%")},
			{"label": "24h区间", "value": rangeLabel},
			{"label": "24h成交额(USDT)", "value": formatPtr(quoteVolume, "%.0f")},
			{"label": "资金费率", "value": fundingRateLabel},
			{"label": "持仓量", "value": oiLabel},
			{"label": "持仓价值(USD)", "value": oiNotionalLabel},
			{"label": "流通市值(USD)", "value": marketCapLabel},
		}

		var levels []model.SRLevel
		d.Store.SelectContext(ctx, &levels,
			`SELECT * FROM sr_levels WHERE market = ? AND symbol = ? AND timeframe = '4h' ORDER BY strength_score DESC LIMIT 12`,
			market, symbol)
		srOut := make([]gin.H, 0, len(levels))
		for _, l := range levels {
			srOut = append(srOut, gin.H{
				"levelPrice":    l.LevelPrice,
				"touches":       l.Touches,
				"strengthScore": l.StrengthScore,
				"lastTouchMs":   l.LastTouchMs,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"basic":           basic,
			"requestedMarket": requestedMarket,
			"effectiveMarket": market,
			"marketFallback":  marketFallback,
			"symbolStatus":    status,
			"timeMode":        timeMode,
			"tzOffsetMin":     tzOffsetMin,
			"funding":         funding,
			"openInterest":    openInterest,
			"marketCap":       marketCap,
			"srLevels":        srOut,
			"basicKvp":        basicKvp,
			"degraded":        tickerErr != nil,
			"warnings":        warnings,
		})
	}
}

func handleCoinFlows(d *Deps, bucket string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}

		type flowItem struct {
			BucketStartMs     int64   `json:"bucketStartMs"`
			SpotNetNotional   float64 `json:"spotNetNotional"`
			SwapNetNotional   float64 `json:"swapNetNotional"`
			SpotQuoteNotional float64 `json:"spotQuoteNotional"`
			SwapQuoteNotional float64 `json:"swapQuoteNotional"`
			LivePartial       bool    `json:"livePartial,omitempty"`
		}

		type flowKey struct {
			Market string
			Ts     int64
		}

		ctx := c.Request.Context()
		nowMs := time.Now().UnixMilli()

		if bucket == "1h" {
			hours := queryInt(c, "hours", 24, 6, 168)
			const hourMs int64 = 60 * 60 * 1000
			const minuteMs int64 = 60 * 1000
			cur := (nowMs / hourMs) * hourMs
			lastClosed := cur - hourMs
			startMs := lastClosed - int64(hours)*hourMs

			rows, err := d.CH.QueryTradeBuckets(ctx, "", symbol, nil, "1h", startMs, lastClosed, "asc", 0)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			byKey := make(map[flowKey]model.CHTradeRow, len(rows))
			for _, r := range rows {
				byKey[flowKey{Market: r.Market, Ts: r.BucketStartMs}] = r
			}

			items := make([]flowItem, 0, hours+2)
			for ts := startMs; ts <= lastClosed; ts += hourMs {
				spot := byKey[flowKey{Market: "spot", Ts: ts}]
				swap := byKey[flowKey{Market: "swap", Ts: ts}]
				items = append(items, flowItem{
					BucketStartMs:     ts,
					SpotNetNotional:   spot.TakerBuyNotional - spot.TakerSellNotional,
					SwapNetNotional:   swap.TakerBuyNotional - swap.TakerSellNotional,
					SpotQuoteNotional: spot.QuoteNotional,
					SwapQuoteNotional: swap.QuoteNotional,
				})
			}

			liveCutoffMs := (nowMs / minuteMs) * minuteMs
			if liveCutoffMs-cur >= minuteMs {
				if liveRows, err := d.CH.QueryTradeBuckets(ctx, "", symbol, nil, "1m", cur, liveCutoffMs-1, "asc", 0); err == nil {
					var spotNet, spotQuote, swapNet, swapQuote float64
					for _, row := range liveRows {
						net := row.TakerBuyNotional - row.TakerSellNotional
						if row.Market == "spot" {
							spotNet += net
							spotQuote += row.QuoteNotional
						} else if row.Market == "swap" {
							swapNet += net
							swapQuote += row.QuoteNotional
						}
					}
					items = append(items, flowItem{
						BucketStartMs:     nowMs,
						SpotNetNotional:   spotNet,
						SwapNetNotional:   swapNet,
						SpotQuoteNotional: spotQuote,
						SwapQuoteNotional: swapQuote,
						LivePartial:       true,
					})
				}
			}

			c.JSON(http.StatusOK, gin.H{"symbol": symbol, "hours": hours, "items": items})
			return
		}

		days := queryInt(c, "days", 30, 7, 365)
		includeToday := queryBool(c, "includeToday", false)
		const dayMs int64 = 24 * 60 * 60 * 1000
		cur := (nowMs / dayMs) * dayMs
		lastClosed := cur - dayMs
		startMs := lastClosed - int64(days-1)*dayMs

		rows, err := d.CH.QueryTradeBuckets(ctx, "", symbol, nil, "1d", startMs, lastClosed, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		byKey := make(map[flowKey]model.CHTradeRow, len(rows))
		for _, r := range rows {
			byKey[flowKey{Market: r.Market, Ts: r.BucketStartMs}] = r
		}

		items := make([]flowItem, 0, days+1)
		for ts := startMs; ts <= lastClosed; ts += dayMs {
			spot := byKey[flowKey{Market: "spot", Ts: ts}]
			swap := byKey[flowKey{Market: "swap", Ts: ts}]
			items = append(items, flowItem{
				BucketStartMs:     ts,
				SpotNetNotional:   spot.TakerBuyNotional - spot.TakerSellNotional,
				SwapNetNotional:   swap.TakerBuyNotional - swap.TakerSellNotional,
				SpotQuoteNotional: spot.QuoteNotional,
				SwapQuoteNotional: swap.QuoteNotional,
			})
		}

		if includeToday {
			spotK, _ := d.BN.GetKlinesRange(ctx, "spot", symbol, "1m", cur, nowMs, 1500)
			swapK, _ := d.BN.GetKlinesRange(ctx, "swap", symbol, "1m", cur, nowMs, 1500)
			spotNet, spotQuote := sumNetFromKlines(spotK)
			swapNet, swapQuote := sumNetFromKlines(swapK)
			if len(spotK) > 0 || len(swapK) > 0 {
				items = append(items, flowItem{
					BucketStartMs:     cur,
					SpotNetNotional:   spotNet,
					SwapNetNotional:   swapNet,
					SpotQuoteNotional: spotQuote,
					SwapQuoteNotional: swapQuote,
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{"symbol": symbol, "days": days, "items": items})
	}
}

func handleCoinOI(d *Deps, period string) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestedMarket := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}

		ctx := c.Request.Context()
		effectiveMarket, marketFallback := resolveEffectiveMarket(d, ctx, requestedMarket, symbol)
		if effectiveMarket != "swap" {
			resp := gin.H{
				"symbol":          symbol,
				"requestedMarket": requestedMarket,
				"effectiveMarket": effectiveMarket,
				"marketFallback":  marketFallback,
				"items":           []gin.H{},
			}
			if period == "1d" {
				resp["days"] = queryInt(c, "days", 30, 7, 120)
			} else {
				resp["hours"] = queryInt(c, "hours", 24, 6, 168)
			}
			c.JSON(http.StatusOK, resp)
			return
		}

		limit := 24
		if period == "1d" {
			limit = queryInt(c, "days", 30, 7, 120)
		} else {
			limit = queryInt(c, "hours", 24, 6, 168)
		}

		oiHist, err := d.BN.GetOpenInterestHist(ctx, symbol, period, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type oiItem struct {
			BucketStartMs   int64    `json:"bucketStartMs"`
			OpenInterestUsd float64  `json:"openInterestUsd"`
			OpenInterest    float64  `json:"openInterest"`
			ClosePrice      *float64 `json:"closePrice,omitempty"`
		}

		items := make([]oiItem, 0, len(oiHist))
		var startMs int64
		var endMs int64
		for _, row := range oiHist {
			ts, ok := parseInt64Maybe(row["timestamp"])
			if !ok || ts <= 0 {
				continue
			}
			oi, okOI := mapFloat(row, "openInterest", "sumOpenInterest")
			oiValue, okVal := mapFloat(row, "sumOpenInterestValue")
			if !okOI || !okVal {
				continue
			}
			items = append(items, oiItem{
				BucketStartMs:   ts,
				OpenInterestUsd: oiValue,
				OpenInterest:    oi,
			})
			if startMs == 0 || ts < startMs {
				startMs = ts
			}
			if ts > endMs {
				endMs = ts
			}
		}

		if len(items) > 0 && d.CH != nil {
			if period == "1h" {
				rows1m, _ := d.CH.QueryTradeBuckets(ctx, "swap", symbol, nil, "1m", startMs, endMs, "asc", 0)
				rows1h, _ := d.CH.QueryTradeBuckets(ctx, "swap", symbol, nil, "1h", startMs, endMs, "asc", 0)
				price1m := make(map[int64]float64, len(rows1m))
				for _, r := range rows1m {
					if r.ClosePrice != nil {
						price1m[r.BucketStartMs] = *r.ClosePrice
					}
				}
				price1h := make(map[int64]float64, len(rows1h))
				for _, r := range rows1h {
					if r.ClosePrice != nil {
						price1h[r.BucketStartMs] = *r.ClosePrice
					}
				}
				for i := range items {
					if v, ok := price1m[items[i].BucketStartMs]; ok {
						cv := v
						items[i].ClosePrice = &cv
						continue
					}
					hourTs := floorBucketStart(items[i].BucketStartMs, 60*60*1000)
					if v, ok := price1h[hourTs]; ok {
						cv := v
						items[i].ClosePrice = &cv
					}
				}
			} else {
				rows1d, _ := d.CH.QueryTradeBuckets(ctx, "swap", symbol, nil, "1d", startMs, endMs, "asc", 0)
				price1d := make(map[int64]float64, len(rows1d))
				for _, r := range rows1d {
					if r.ClosePrice != nil {
						price1d[r.BucketStartMs] = *r.ClosePrice
					}
				}
				for i := range items {
					dayTs := floorBucketStart(items[i].BucketStartMs, 24*60*60*1000)
					if v, ok := price1d[dayTs]; ok {
						cv := v
						items[i].ClosePrice = &cv
					}
				}
			}
		}

		sort.Slice(items, func(i, j int) bool { return items[i].BucketStartMs < items[j].BucketStartMs })
		resp := gin.H{
			"symbol":          symbol,
			"requestedMarket": requestedMarket,
			"effectiveMarket": effectiveMarket,
			"marketFallback":  marketFallback,
			"items":           items,
		}
		if period == "1d" {
			resp["days"] = limit
		} else {
			resp["hours"] = limit
		}
		c.JSON(http.StatusOK, resp)
	}
}

func handleCoinLSR(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		limit := queryInt(c, "limit", 6, 3, 48)
		ctx := c.Request.Context()

		global, _ := d.BN.GetGlobalLongShortAccountRatio(ctx, symbol, "1h", limit)
		topAccount, _ := d.BN.GetTopLongShortAccountRatio(ctx, symbol, "1h", limit)
		topPosition, _ := d.BN.GetTopLongShortPositionRatio(ctx, symbol, "1h", limit)

		gMap := ratioMap(global)
		tMap := ratioMap(topAccount)
		pMap := ratioMap(topPosition)

		timestamps := make([]int64, 0, len(gMap))
		for ts := range gMap {
			if _, ok := tMap[ts]; !ok {
				continue
			}
			if _, ok := pMap[ts]; !ok {
				continue
			}
			timestamps = append(timestamps, ts)
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })

		type lsrItem struct {
			BucketStartMs    int64   `json:"bucketStartMs"`
			AccountRatio     float64 `json:"accountRatio"`
			TopAccountRatio  float64 `json:"topAccountRatio"`
			TopPositionRatio float64 `json:"topPositionRatio"`
		}
		items := make([]lsrItem, 0, len(timestamps))
		for _, ts := range timestamps {
			items = append(items, lsrItem{
				BucketStartMs:    ts,
				AccountRatio:     gMap[ts],
				TopAccountRatio:  tMap[ts],
				TopPositionRatio: pMap[ts],
			})
		}

		c.JSON(http.StatusOK, gin.H{"symbol": symbol, "period": "1h", "items": items})
	}
}

func handleCoinSR(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestedMarket := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		timeframe := strings.ToLower(c.DefaultQuery("timeframe", "1h"))
		if timeframe != "1h" && timeframe != "15m" {
			timeframe = "1h"
		}
		limit := queryInt(c, "limit", 5, 1, 10)
		ctx := c.Request.Context()

		effectiveMarket, marketFallback := resolveEffectiveMarket(d, ctx, requestedMarket, symbol)

		var levels []model.SRLevel
		q := `SELECT * FROM sr_levels WHERE market = ? AND symbol = ? AND timeframe = ? ORDER BY strength_score DESC LIMIT 30`
		d.Store.SelectContext(ctx, &levels, q, effectiveMarket, symbol, timeframe)
		if levels == nil {
			levels = []model.SRLevel{}
		}

		var lastClose *float64
		if d.CH != nil {
			if rows, err := d.CH.QueryTradeBuckets(ctx, effectiveMarket, symbol, nil, timeframe, 0, 0, "desc", 1); err == nil && len(rows) > 0 && rows[0].ClosePrice != nil {
				lastClose = rows[0].ClosePrice
			}
		}

		type srBand struct {
			Low  float64 `json:"low"`
			High float64 `json:"high"`
		}
		type srCandidate struct {
			Band srBand
			Dist float64
		}
		supports := make([]srCandidate, 0, len(levels))
		resistances := make([]srCandidate, 0, len(levels))
		bandPct := 0.003
		if timeframe == "15m" {
			bandPct = 0.0015
		}

		for _, lv := range levels {
			if lv.LevelPrice <= 0 {
				continue
			}
			half := lv.LevelPrice * bandPct
			b := srBand{Low: lv.LevelPrice - half, High: lv.LevelPrice + half}
			if lastClose == nil {
				continue
			}
			dist := math.Abs(lv.LevelPrice - *lastClose)
			if lv.LevelPrice <= *lastClose {
				supports = append(supports, srCandidate{Band: b, Dist: dist})
			} else {
				resistances = append(resistances, srCandidate{Band: b, Dist: dist})
			}
		}

		sort.Slice(supports, func(i, j int) bool { return supports[i].Dist < supports[j].Dist })
		sort.Slice(resistances, func(i, j int) bool { return resistances[i].Dist < resistances[j].Dist })
		if len(supports) > limit {
			supports = supports[:limit]
		}
		if len(resistances) > limit {
			resistances = resistances[:limit]
		}
		supportBands := make([]srBand, 0, len(supports))
		for _, it := range supports {
			supportBands = append(supportBands, it.Band)
		}
		resistanceBands := make([]srBand, 0, len(resistances))
		for _, it := range resistances {
			resistanceBands = append(resistanceBands, it.Band)
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":          symbol,
			"market":          effectiveMarket,
			"requestedMarket": requestedMarket,
			"effectiveMarket": effectiveMarket,
			"marketFallback":  marketFallback,
			"timeframe":       timeframe,
			"supports":        supportBands,
			"resistances":     resistanceBands,
		})
	}
}

func handleFundSnapshots(d *Deps) gin.HandlerFunc {
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
		offsetMs := int64(0)
		if timeMode == "local" {
			offsetMs = int64(tzOffsetMin) * 60 * 1000
		}
		const hourMs int64 = 60 * 60 * 1000
		const dayMs int64 = 24 * 60 * 60 * 1000
		dayStartMs := floorBucketStartWithOffset(nowMs, dayMs, offsetMs)
		currentHourStartMs := floorBucketStartWithOffset(nowMs, hourMs, offsetMs)

		rows, err := d.CH.QueryTradeBuckets(ctx, "", symbol, nil, "1m", dayStartMs, nowMs, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type netEntry struct {
			Ts  int64
			Net float64
		}
		byMarket := map[string][]netEntry{"spot": {}, "swap": {}}
		for _, r := range rows {
			byMarket[r.Market] = append(byMarket[r.Market], netEntry{
				Ts:  r.BucketStartMs,
				Net: r.TakerBuyNotional - r.TakerSellNotional,
			})
		}

		closedCutoffs := make([]int64, 0, 24)
		for ts := dayStartMs + hourMs; ts <= currentHourStartMs; ts += hourMs {
			closedCutoffs = append(closedCutoffs, ts)
		}

		cumAt := func(entries []netEntry, cutoffs []int64) []float64 {
			if len(cutoffs) == 0 {
				return []float64{}
			}
			if len(entries) == 0 {
				return make([]float64, len(cutoffs))
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Ts < entries[j].Ts })
			out := make([]float64, 0, len(cutoffs))
			idx := 0
			total := 0.0
			for _, cutoff := range cutoffs {
				for idx < len(entries) && entries[idx].Ts < cutoff {
					total += entries[idx].Net
					idx++
				}
				out = append(out, total)
			}
			return out
		}

		spotClosed := cumAt(byMarket["spot"], closedCutoffs)
		swapClosed := cumAt(byMarket["swap"], closedCutoffs)
		spotNow := cumAt(byMarket["spot"], []int64{nowMs + 1})
		swapNow := cumAt(byMarket["swap"], []int64{nowMs + 1})

		type snapshotItem struct {
			Key       int     `json:"key"`
			LabelTsMs int64   `json:"labelTsMs"`
			SwapValue float64 `json:"swapValue"`
			SpotValue float64 `json:"spotValue"`
		}
		items := make([]snapshotItem, 0, len(closedCutoffs)+1)
		for i, ts := range closedCutoffs {
			swapVal := 0.0
			spotVal := 0.0
			if i < len(swapClosed) {
				swapVal = swapClosed[i]
			}
			if i < len(spotClosed) {
				spotVal = spotClosed[i]
			}
			items = append(items, snapshotItem{
				Key:       i + 1,
				LabelTsMs: ts,
				SwapValue: swapVal,
				SpotValue: spotVal,
			})
		}
		if nowMs-currentHourStartMs >= 60*1000 {
			swapVal := 0.0
			spotVal := 0.0
			if len(swapNow) > 0 {
				swapVal = swapNow[0]
			}
			if len(spotNow) > 0 {
				spotVal = spotNow[0]
			}
			items = append(items, snapshotItem{
				Key:       len(items) + 1,
				LabelTsMs: nowMs,
				SwapValue: swapVal,
				SpotValue: spotVal,
			})
		}

		timezone := "UTC"
		if timeMode == "local" {
			timezone = "LOCAL"
		}
		c.JSON(http.StatusOK, gin.H{
			"symbol":      symbol,
			"timezone":    timezone,
			"timeMode":    timeMode,
			"tzOffsetMin": tzOffsetMin,
			"source":      "trade_buckets_1m",
			"items":       items,
		})
	}
}

func handleFundIntraday(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		bucket := strings.ToLower(c.DefaultQuery("bucket", "1h"))
		if bucket != "1m" && bucket != "5m" && bucket != "15m" && bucket != "1h" {
			bucket = "1h"
		}
		limit := queryInt(c, "limit", 60, 12, 1440)
		bucketMs := int64(60 * 60 * 1000)
		switch bucket {
		case "1m":
			bucketMs = 60 * 1000
		case "5m":
			bucketMs = 5 * 60 * 1000
		case "15m":
			bucketMs = 15 * 60 * 1000
		case "1h":
			bucketMs = 60 * 60 * 1000
		}

		nowMs := time.Now().UnixMilli()
		dayMs := int64(24 * 60 * 60 * 1000)
		dayStartMs := floorBucketStart(nowMs, dayMs)
		lastBucketStart := floorBucketStart(nowMs, bucketMs)
		if lastBucketStart < dayStartMs {
			c.JSON(http.StatusOK, gin.H{
				"symbol":        symbol,
				"bucket":        bucket,
				"dayStartMs":    dayStartMs,
				"lastBucketMs":  lastBucketStart,
				"spotAvailable": false,
				"swapAvailable": false,
				"source":        "trade_buckets",
				"items":         []gin.H{},
			})
			return
		}

		totalCount := int((lastBucketStart-dayStartMs)/bucketMs) + 1
		startIndex := totalCount - limit
		if startIndex < 0 {
			startIndex = 0
		}
		timeline := make([]int64, 0, totalCount-startIndex)
		for i := startIndex; i < totalCount; i++ {
			timeline = append(timeline, dayStartMs+int64(i)*bucketMs)
		}

		queryBucket := bucket
		if bucket == "5m" {
			queryBucket = "1m"
		}

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), "", symbol, nil, queryBucket, dayStartMs, lastBucketStart, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		byMarket := map[string]map[int64]float64{"spot": {}, "swap": {}}
		for _, row := range rows {
			ts := row.BucketStartMs
			if bucket == "5m" {
				ts = floorBucketStart(ts, bucketMs)
			}
			net := row.TakerBuyNotional - row.TakerSellNotional
			marketMap := byMarket[row.Market]
			if marketMap == nil {
				marketMap = map[int64]float64{}
				byMarket[row.Market] = marketMap
			}
			marketMap[ts] += net
		}

		spotAvailable := len(byMarket["spot"]) > 0
		swapAvailable := len(byMarket["swap"]) > 0
		allStarts := make([]int64, 0, totalCount)
		for ts := dayStartMs; ts <= lastBucketStart; ts += bucketMs {
			allStarts = append(allStarts, ts)
		}
		cumMap := func(entries map[int64]float64) map[int64]float64 {
			total := 0.0
			out := make(map[int64]float64, len(allStarts))
			for _, ts := range allStarts {
				total += entries[ts]
				out[ts] = total
			}
			return out
		}
		spotCum := cumMap(byMarket["spot"])
		swapCum := cumMap(byMarket["swap"])

		type intradayItem struct {
			BucketStartMs int64   `json:"bucketStartMs"`
			SwapValue     float64 `json:"swapValue"`
			SpotValue     float64 `json:"spotValue"`
			SwapDelta     float64 `json:"swapDelta"`
			SpotDelta     float64 `json:"spotDelta"`
		}
		items := make([]intradayItem, 0, len(timeline))
		for _, ts := range timeline {
			items = append(items, intradayItem{
				BucketStartMs: ts,
				SwapValue:     swapCum[ts],
				SpotValue:     spotCum[ts],
				SwapDelta:     byMarket["swap"][ts],
				SpotDelta:     byMarket["spot"][ts],
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":        symbol,
			"bucket":        bucket,
			"dayStartMs":    dayStartMs,
			"lastBucketMs":  lastBucketStart,
			"spotAvailable": spotAvailable,
			"swapAvailable": swapAvailable,
			"source":        fmt.Sprintf("trade_buckets_%s", queryBucket),
			"items":         items,
		})
	}
}

func handleOBIntraday(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		ctx := c.Request.Context()
		limit := queryInt(c, "limit", 60, 12, 240)

		const bucketMs int64 = 60000 // 1m
		nowMs := time.Now().UnixMilli()
		lastBucket := (nowMs / bucketMs) * bucketMs
		startMs := lastBucket - int64(limit-1)*bucketMs

		rows, err := d.CH.QueryOrderbookFeatures(ctx, "", symbol, nil, "1m", startMs, lastBucket, "asc")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// group by market → ts
		type mktMap = map[int64]*model.CHOBFeatureRow
		byMkt := map[string]mktMap{"spot": {}, "swap": {}}
		for i := range rows {
			r := &rows[i]
			if byMkt[r.Market] == nil {
				byMkt[r.Market] = mktMap{}
			}
			byMkt[r.Market][r.BucketStartMs] = r
		}

		type obSide struct {
			SpreadBps          *float64 `json:"spreadBps"`
			DepthImbalanceL20  *float64 `json:"depthImbalanceL20"`
			MicropriceShiftBps *float64 `json:"micropriceShiftBps"`
			WallPressureL20    *float64 `json:"wallPressureL20"`
			AggrBuyRatio       *float64 `json:"aggrBuyRatio"`
			ReplenishScore     *float64 `json:"replenishScore"`
			SampleCount        int      `json:"sampleCount"`
		}

		buildSide := func(row *model.CHOBFeatureRow) obSide {
			if row == nil || row.SampleCount <= 0 {
				return obSide{}
			}
			sc := float64(row.SampleCount)
			avg := func(v float64) *float64 { r := v / sc; return &r }

			side := obSide{
				SpreadBps:          avg(row.SpreadBpsSum),
				DepthImbalanceL20:  avg(row.DepthImbalanceL20Sum),
				MicropriceShiftBps: avg(row.MicropriceShiftBpsSum),
				WallPressureL20:    avg(row.WallPressureL20Sum),
				SampleCount:        row.SampleCount,
			}
			if d := row.TakerBuyNotional + row.TakerSellNotional; d > 0 {
				v := row.TakerBuyNotional / d
				side.AggrBuyRatio = &v
			}
			dep, rep := row.DepletionEvents, row.ReplenishmentEvents
			if dep <= 0 {
				v := 50.0
				side.ReplenishScore = &v
			} else {
				v := math.Min(100, math.Max(0, float64(rep)/float64(dep)*100))
				side.ReplenishScore = &v
			}
			return side
		}

		type obItem struct {
			BucketStartMs int64  `json:"bucketStartMs"`
			Swap          obSide `json:"swap"`
			Spot          obSide `json:"spot"`
		}
		items := make([]obItem, 0, limit)
		for ts := startMs; ts <= lastBucket; ts += bucketMs {
			items = append(items, obItem{
				BucketStartMs: ts,
				Swap:          buildSide(byMkt["swap"][ts]),
				Spot:          buildSide(byMkt["spot"][ts]),
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"symbol":        symbol,
			"bucket":        "1m",
			"spotAvailable": len(byMkt["spot"]) > 0,
			"swapAvailable": len(byMkt["swap"]) > 0,
			"source":        "orderbook_feature_buckets_1m",
			"items":         items,
		})
	}
}

func handleOBAbsorptionSignal(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		hours := queryInt(c, "hours", 24, 1, 720)
		limit := queryInt(c, "limit", 100, 1, 500)

		cutoffMs := time.Now().UnixMilli() - int64(hours)*60*60*1000
		var items []model.AbsorptionSignalSnapshot
		q := `SELECT * FROM absorption_signal_snapshots WHERE market = ? AND symbol = ? AND bucket_start_ms >= ? ORDER BY bucket_start_ms DESC LIMIT ?`
		d.Store.SelectContext(c.Request.Context(), &items, q, market, symbol, cutoffMs, limit)
		if items == nil {
			items = []model.AbsorptionSignalSnapshot{}
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": items})
	}
}

func handleSpotHeatmap(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		bucket := c.DefaultQuery("bucket", "1m")
		limit := queryInt(c, "limit", 50, 1, 200)

		var items []model.OrderbookHeatmapSnapshot
		q := `SELECT * FROM orderbook_heatmap_1m WHERE market = ? AND symbol = ? ORDER BY bucket_start_ms DESC LIMIT ?`
		d.Store.SelectContext(c.Request.Context(), &items, q, market, symbol, limit)
		if items == nil {
			items = []model.OrderbookHeatmapSnapshot{}
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "bucket": bucket, "heatmap": items})
	}
}

func handleRecent(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		market := queryMarket(c)
		limit := queryInt(c, "limit", 100, 1, 500)

		nowMs := time.Now().UnixMilli()
		hourStart := (nowMs / 3600000) * 3600000
		startMs := hourStart - 3600000

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, "", nil, "1h", startMs, hourStart, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type recentItem struct {
			Symbol string  `json:"symbol"`
			QV     float64 `json:"quoteNotional"`
			RetPct float64 `json:"retPct"`
		}
		var items []recentItem
		for _, r := range rows {
			if binance.IsExcludedSymbol(r.Symbol) {
				continue
			}
			ret := 0.0
			if r.OpenPrice != nil && r.ClosePrice != nil && *r.OpenPrice > 0 {
				ret = (*r.ClosePrice / *r.OpenPrice - 1) * 100
			}
			items = append(items, recentItem{r.Symbol, r.QuoteNotional, math.Round(ret*100) / 100})
		}
		if len(items) > limit {
			items = items[:limit]
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "items": items})
	}
}

func handleRecentDaily(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		requestedMarket := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		days := queryInt(c, "days", 20, 10, 90)
		includeToday := queryBool(c, "includeToday", false)
		ctx := c.Request.Context()

		effectiveMarket, marketFallback := resolveEffectiveMarket(d, ctx, requestedMarket, symbol)
		rows, err := d.CH.QueryTradeBuckets(ctx, effectiveMarket, symbol, nil, "1d", 0, 0, "desc", days)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type dailyItem struct {
			BucketStartMs int64    `json:"bucketStartMs"`
			Open          *float64 `json:"open"`
			High          *float64 `json:"high"`
			Low           *float64 `json:"low"`
			Close         *float64 `json:"close"`
		}
		items := make([]dailyItem, 0, len(rows)+1)
		for i := len(rows) - 1; i >= 0; i-- {
			r := rows[i]
			items = append(items, dailyItem{
				BucketStartMs: r.BucketStartMs,
				Open:          r.OpenPrice,
				High:          r.HighPrice,
				Low:           r.LowPrice,
				Close:         r.ClosePrice,
			})
		}

		if includeToday {
			nowMs := time.Now().UnixMilli()
			dayStartMs := floorBucketStart(nowMs, 24*60*60*1000)
			needAppend := len(items) == 0 || items[len(items)-1].BucketStartMs < dayStartMs
			if needAppend {
				klines, _ := d.BN.GetKlinesRange(ctx, effectiveMarket, symbol, "1m", dayStartMs, nowMs, 1500)
				if len(klines) > 0 {
					open := klineNum(klines[0], 1)
					close := klineNum(klines[len(klines)-1], 4)
					high := open
					low := open
					for _, k := range klines {
						if v := klineNum(k, 2); v != nil && (high == nil || *v > *high) {
							high = v
						}
						if v := klineNum(k, 3); v != nil && (low == nil || *v < *low) {
							low = v
						}
					}
					items = append(items, dailyItem{
						BucketStartMs: dayStartMs,
						Open:          open,
						High:          high,
						Low:           low,
						Close:         close,
					})
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"market":          effectiveMarket,
			"requestedMarket": requestedMarket,
			"effectiveMarket": effectiveMarket,
			"marketFallback":  marketFallback,
			"symbol":          symbol,
			"items":           items,
		})
	}
}

func resolveEffectiveMarket(d *Deps, ctx context.Context, requestedMarket, symbol string) (string, bool) {
	market := requestedMarket
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
	return market, marketFallback
}

func parseFloatMaybe(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false
		}
		return parseF(t), true
	default:
		return 0, false
	}
}

func parseInt64Maybe(v interface{}) (int64, bool) {
	f, ok := parseFloatMaybe(v)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func mapFloat(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if f, ok := parseFloatMaybe(v); ok {
			return f, true
		}
	}
	return 0, false
}

func ratioMap(rows []map[string]interface{}) map[int64]float64 {
	out := make(map[int64]float64, len(rows))
	for _, r := range rows {
		ts, okTs := parseInt64Maybe(r["timestamp"])
		ratio, okRatio := mapFloat(r, "longShortRatio")
		if !okTs || !okRatio {
			continue
		}
		out[ts] = ratio
	}
	return out
}

func sumNetFromKlines(klines [][]interface{}) (float64, float64) {
	net := 0.0
	quote := 0.0
	for _, k := range klines {
		qv := klineNum(k, 7)
		tbq := klineNum(k, 10)
		if qv == nil || tbq == nil {
			continue
		}
		quote += *qv
		net += 2*(*tbq) - *qv
	}
	return net, quote
}

func klineNum(k []interface{}, idx int) *float64 {
	if idx < 0 || idx >= len(k) {
		return nil
	}
	f, ok := parseFloatMaybe(k[idx])
	if !ok {
		return nil
	}
	return &f
}

func floorBucketStart(ts, bucketMs int64) int64 {
	if bucketMs <= 0 {
		return ts
	}
	return (ts / bucketMs) * bucketMs
}

func floorBucketStartWithOffset(ts, bucketMs, offsetMs int64) int64 {
	if bucketMs <= 0 {
		return ts
	}
	return ((ts-offsetMs)/bucketMs)*bucketMs + offsetMs
}
