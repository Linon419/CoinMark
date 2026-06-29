package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/service"
)

func registerBollPumpRoutes(g *gin.RouterGroup, d *Deps) {
	r := g.Group("/boll-pump")
	r.GET("/signals", handleBollPumpSignals(d))
	r.GET("/states", handleBollPumpStates(d))
	r.GET("/stats", handleBollPumpStats(d))
	r.GET("/signals/:id/detail", handleBollPumpSignalDetail(d))
}

func handleBollPumpSignals(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := service.ListBollPumpSignals(c.Request.Context(), d.Store, service.BollPumpSignalFilter{
			Market:      queryMarket(c),
			Symbol:      strings.ToUpper(c.Query("symbol")),
			Timeframe:   c.Query("timeframe"),
			SignalLevel: c.Query("signal_level"),
			MinScore:    queryFloat(c, "min_score", 0),
			SinceMs:     queryInt64(c, "since", 0),
			Limit:       queryInt(c, "limit", 100, 1, 1000),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows, "limit": len(rows)})
	}
}

func handleBollPumpStates(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := service.ListBollPumpStates(c.Request.Context(), d.Store, service.BollPumpStateFilter{
			Market:           queryMarket(c),
			Symbol:           strings.ToUpper(c.Query("symbol")),
			Timeframe:        c.Query("timeframe"),
			Status:           c.Query("status"),
			MinPriorityScore: queryFloat(c, "min_priority_score", 0),
			Limit:            queryInt(c, "limit", 100, 1, 1000),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows, "limit": len(rows)})
	}
}

func handleBollPumpStats(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := service.BollPumpStats(c.Request.Context(), d.Store, queryMarket(c), queryInt64(c, "since", time.Now().UnixMilli()-30*24*60*60*1000))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, stats)
	}
}

func handleBollPumpSignalDetail(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signal id"})
			return
		}
		sig, err := service.GetBollPumpSignal(c.Request.Context(), d.Store, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if sig == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "signal not found"})
			return
		}
		var st *model.BollPumpState
		st, _ = service.GetBollPumpState(c.Request.Context(), d.Store, sig.Market, sig.Symbol, sig.Timeframe)
		candles, indicators := bollPumpDetailSeries(c.Request.Context(), d, *sig)
		c.JSON(http.StatusOK, gin.H{
			"signal":     sig,
			"state":      st,
			"candles":    candles,
			"indicators": indicators,
			"markers":    bollPumpMarkers(*sig),
		})
	}
}

func bollPumpDetailSeries(ctx context.Context, d *Deps, sig model.BollPumpSignal) ([]map[string]interface{}, []map[string]interface{}) {
	if d == nil || d.BN == nil {
		return []map[string]interface{}{}, []map[string]interface{}{}
	}
	source := service.NewBinanceBollPumpSource(d.BN, 1)
	bars, err := source.Klines(ctx, sig.Market, sig.Symbol, sig.Timeframe, 260)
	if err != nil {
		return []map[string]interface{}{}, []map[string]interface{}{}
	}
	ind := service.ComputeBollPumpIndicators(bars, 20, 2, 14)
	candles := make([]map[string]interface{}, 0, len(bars))
	indicators := make([]map[string]interface{}, 0, len(ind))
	for i, b := range bars {
		candles = append(candles, map[string]interface{}{
			"time": b.OpenTimeMs, "open": b.Open, "high": b.High, "low": b.Low, "close": b.Close, "volume": b.Volume,
		})
		in := ind[i]
		indicators = append(indicators, map[string]interface{}{
			"time": b.OpenTimeMs, "middle": in.Middle, "upper": in.Upper, "lower": in.Lower, "atr14": in.ATR14, "bandwidth": in.Bandwidth,
		})
	}
	return candles, indicators
}

func bollPumpMarkers(sig model.BollPumpSignal) []map[string]interface{} {
	out := []map[string]interface{}{
		{"time": sig.CandleStartMs, "label": sig.SignalLevel, "price": sig.Price, "kind": "signal"},
	}
	if sig.PullbackCandleStartMs != nil {
		out = append(out, map[string]interface{}{"time": *sig.PullbackCandleStartMs, "label": "pullback", "price": sig.Price, "kind": "pullback"})
	}
	return out
}

func queryInt64(c *gin.Context, key string, def int64) int64 {
	s := c.Query(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
