package handler

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
)

func registerAggregateRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/aggregate/basicinfo", handleBasicInfo(d))
	g.GET("/aggregate/returns", handleReturns(d))
	g.GET("/kline/GetKlines", handleKlines(d))
}

func handleBasicInfo(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		limit := queryInt(c, "limit", 50, 1, 500)

		tickers, err := d.BN.GetTicker24hAll(c.Request.Context(), market)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type item struct {
			Symbol      string  `json:"symbol"`
			PriceChange float64 `json:"priceChangePercent"`
			LastPrice   float64 `json:"lastPrice"`
			Volume      float64 `json:"quoteVolume"`
		}
		var items []item
		for _, t := range tickers {
			sym, _ := t["symbol"].(string)
			if sym == "" || binance.IsExcludedSymbol(sym) {
				continue
			}
			items = append(items, item{
				Symbol:      sym,
				PriceChange: toF(t["priceChangePercent"]),
				LastPrice:   toF(t["lastPrice"]),
				Volume:      toF(t["quoteVolume"]),
			})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].PriceChange > items[j].PriceChange })
		gainers := items
		if len(gainers) > limit {
			gainers = gainers[:limit]
		}
		sort.Slice(items, func(i, j int) bool { return items[i].PriceChange < items[j].PriceChange })
		losers := items
		if len(losers) > limit {
			losers = losers[:limit]
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "gainers": gainers, "losers": losers})
	}
}

func handleReturns(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		market := queryMarket(c)
		bucket := c.DefaultQuery("bucket", "1h")
		if bucket != "15m" && bucket != "1h" && bucket != "4h" && bucket != "1d" {
			bucket = "1h"
		}
		limit := queryInt(c, "limit", 50, 1, 500)

		bucketMs := bucketToMs(bucket)
		nowMs := time.Now().UnixMilli()
		bucketEnd := (nowMs / bucketMs) * bucketMs
		bucketStart := bucketEnd - bucketMs

		rows, err := d.CH.QueryTradeBuckets(c.Request.Context(), market, "", nil, bucket, bucketStart, bucketEnd, "asc", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type retItem struct {
			Symbol string  `json:"symbol"`
			RetPct float64 `json:"retPct"`
			QV     float64 `json:"quoteNotional"`
		}
		var items []retItem
		for _, r := range rows {
			if r.OpenPrice == nil || r.ClosePrice == nil || *r.OpenPrice <= 0 {
				continue
			}
			if binance.IsExcludedSymbol(r.Symbol) {
				continue
			}
			ret := (*r.ClosePrice / *r.OpenPrice - 1) * 100
			items = append(items, retItem{Symbol: r.Symbol, RetPct: math.Round(ret*100) / 100, QV: r.QuoteNotional})
		}

		sort.Slice(items, func(i, j int) bool { return items[i].RetPct > items[j].RetPct })
		gainers := items
		if len(gainers) > limit {
			gainers = gainers[:limit]
		}
		losersAll := make([]retItem, len(items))
		copy(losersAll, items)
		sort.Slice(losersAll, func(i, j int) bool { return losersAll[i].RetPct < losersAll[j].RetPct })
		losers := losersAll
		if len(losers) > limit {
			losers = losers[:limit]
		}

		c.JSON(http.StatusOK, gin.H{
			"market": market, "bucket": bucket,
			"bucketStartMs": bucketStart, "bucketEndMs": bucketEnd,
			"gainers": gainers, "losers": losers,
		})
	}
}

func handleKlines(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := strings.ToUpper(c.Query("symbol"))
		interval := c.DefaultQuery("interval", "1h")
		limit := queryInt(c, "limit", 200, 1, 1500)

		if symbol == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
			return
		}

		klines, err := d.BN.GetKlines(c.Request.Context(), market, symbol, interval, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "interval": interval, "klines": klines})
	}
}

func bucketToMs(b string) int64 {
	switch b {
	case "15m":
		return 15 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	default:
		return 60 * 60 * 1000
	}
}

func toF(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := parseSimpleFloat(t)
		return f
	default:
		return 0
	}
}

func parseSimpleFloat(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	v := 0.0
	for ; i < len(s); i++ {
		if s[i] == '.' {
			frac := 0.0
			div := 1.0
			for j := i + 1; j < len(s); j++ {
				if s[j] < '0' || s[j] > '9' {
					break
				}
				div *= 10
				frac += float64(s[j]-'0') / div
			}
			v += frac
			break
		}
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		v = v*10 + float64(s[i]-'0')
	}
	if neg {
		v = -v
	}
	return v, true
}
