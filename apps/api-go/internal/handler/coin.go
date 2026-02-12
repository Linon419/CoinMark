package handler

import (
	"math"
	"net/http"
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

		ticker, err := d.BN.GetTicker24h(ctx, market, symbol)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
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

		status, _ := d.BN.GetSymbolStatus(ctx, market, symbol)

		var funding interface{}
		var openInterest interface{}
		if market == "swap" {
			if fr, e := d.CH.QueryFundingBySymbol(ctx, symbol); e == nil && fr != nil {
				funding = gin.H{
					"lastFundingRate": fr.LastFundingRate,
					"markPrice":       fr.MarkPrice,
					"timeMs":          fr.EventTimeMs,
				}
			}
			if oi, e := d.CH.QueryOIBySymbol(ctx, symbol); e == nil && oi != nil {
				openInterest = gin.H{
					"openInterest":  oi.OpenInterest,
					"markPrice":     oi.MarkPrice,
					"oiNotionalUsd": oi.OINotionalUSD,
					"timeMs":        oi.EventTimeMs,
				}
			}
		}

		var marketCap interface{}
		if cap, e := d.CH.QueryMarketCapByAsset(ctx, asset); e == nil && cap != nil {
			marketCap = gin.H{
				"priceUsd":          cap.PriceUSD,
				"circulatingSupply": cap.CirculatingSupply,
				"marketCapUsd":      cap.MarketCapUSD,
				"source":            cap.Source,
				"timeMs":            cap.EventTimeMs,
			}
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
			"basicKvp":        make([]gin.H, 0),
		})
	}
}

func handleCoinFlows(d *Deps, bucket string) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		defLimit := 168
		maxLimit := 500
		if bucket == "1d" {
			defLimit = 365
		}
		limit := queryInt(c, "limit", defLimit, 1, maxLimit)

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, symbol, nil, bucket, 0, 0, "desc", limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type flowItem struct {
			BucketStartMs     int64    `json:"bucket_start_ms"`
			TakerBuyNotional  float64  `json:"taker_buy_notional"`
			TakerSellNotional float64  `json:"taker_sell_notional"`
			NetBuyNotional    float64  `json:"net_buy_notional"`
			QuoteNotional     float64  `json:"quote_notional"`
			ClosePrice        *float64 `json:"close_price"`
		}
		items := make([]flowItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, flowItem{
				BucketStartMs:     r.BucketStartMs,
				TakerBuyNotional:  r.TakerBuyNotional,
				TakerSellNotional: r.TakerSellNotional,
				NetBuyNotional:    r.TakerBuyNotional - r.TakerSellNotional,
				QuoteNotional:     r.QuoteNotional,
				ClosePrice:        r.ClosePrice,
			})
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": items})
	}
}

func handleCoinOI(d *Deps, period string) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		defLimit := 168
		if period == "1d" {
			defLimit = 365
		}
		limit := queryInt(c, "limit", defLimit, 1, 500)

		oiHist, err := d.BN.GetOpenInterestHist(c.Request.Context(), symbol, period, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": oiHist})
	}
}

func handleCoinLSR(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		limit := queryInt(c, "limit", 168, 1, 500)

		global, _ := d.BN.GetGlobalLongShortAccountRatio(c.Request.Context(), symbol, "1h", limit)
		top, _ := d.BN.GetTopLongShortAccountRatio(c.Request.Context(), symbol, "1h", limit)

		c.JSON(http.StatusOK, gin.H{
			"market": market, "symbol": symbol,
			"items": gin.H{"global": global, "top": top},
		})
	}
}

func handleCoinSR(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}

		var levels []model.SRLevel
		q := `SELECT * FROM sr_levels WHERE market = ? AND symbol = ? AND timeframe = '4h' ORDER BY strength_score DESC LIMIT 30`
		d.Store.SelectContext(c.Request.Context(), &levels, q, market, symbol)
		if levels == nil {
			levels = []model.SRLevel{}
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": levels})
	}
}

func handleFundSnapshots(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}
		limit := queryInt(c, "limit", 200, 1, 500)

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, symbol, nil, "8h", 0, 0, "desc", limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": rows})
	}
}

func handleFundIntraday(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := requireSymbol(c)
		if symbol == "" {
			return
		}

		nowMs := time.Now().UnixMilli()
		startMs := nowMs - 24*60*60*1000
		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, symbol, nil, "1m", startMs, 0, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "items": rows})
	}
}

func handleOBIntraday(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		market := queryMarket(c)
		limit := queryInt(c, "limit", 100, 1, 500)

		nowMs := time.Now().UnixMilli()
		dayStart := (nowMs / 86400000) * 86400000
		startMs := dayStart - 86400000

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, "", nil, "1d", startMs, dayStart, "asc", 0)
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
