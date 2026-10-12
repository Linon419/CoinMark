package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func registerCachedKlineRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/klines/cached/symbols", handleCachedKlineSymbols(d))
	g.GET("/klines/cached", handleCachedKlines(d))
}

// handleCachedKlineSymbols WS K 线缓存覆盖的合约和周期（strategy2_ml 升档扫描用，不请求币安）。
func handleCachedKlineSymbols(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		live := d.Hub.LiveKlines()
		if live == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kline cache disabled (BOLL_PUMP_WS_ENABLED)"})
			return
		}
		symbols, intervals := live.CachedSymbols()
		c.JSON(http.StatusOK, gin.H{"symbols": symbols, "intervals": intervals})
	}
}

// handleCachedKlines 某个合约某个周期缓存里已收盘的 K 线：[开盘毫秒, 开, 高, 低, 收, 成交量, 成交额]，按时间升序。
func handleCachedKlines(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		live := d.Hub.LiveKlines()
		if live == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kline cache disabled (BOLL_PUMP_WS_ENABLED)"})
			return
		}
		symbol := strings.ToUpper(strings.TrimSpace(c.Query("symbol")))
		interval := strings.TrimSpace(c.Query("interval"))
		if symbol == "" || interval == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol and interval are required"})
			return
		}
		bars := live.CachedKlines(symbol, interval, queryInt(c, "limit", 200, 1, 499))
		rows := make([][]float64, 0, len(bars))
		for _, b := range bars {
			rows = append(rows, []float64{float64(b.OpenTimeMs), b.Open, b.High, b.Low, b.Close, b.Volume, b.QuoteVolume})
		}
		c.JSON(http.StatusOK, gin.H{"symbol": symbol, "interval": interval, "bars": rows})
	}
}
