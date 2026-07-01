package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/service"
)

func registerAnomalyRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/aggregate/orderbookAbsorptionSignals", handleAbsorptionSignals(d))
	g.GET("/aggregate/hotMarkets", handleHotMarkets(d))
	g.GET("/aggregate/anomalyStats", handleAnomalyStats(d))
	g.GET("/aggregate/srLevels", handleSRLevels(d))
	g.POST("/aggregate/whaleWallScan", handleWhaleWallScan(d))
}

func handleWhaleWallScan(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d == nil || d.BN == nil || d.Store == nil || d.Cfg == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "depth scanner not configured"})
			return
		}
		market := queryMarket(c)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
		defer cancel()
		scanner := service.NewDepthScanner(d.BN, d.Store, d.Cfg)
		result := scanner.ScanOnce(ctx, market)
		c.JSON(http.StatusOK, gin.H{
			"market": market,
			"result": result,
		})
	}
}

func handleAbsorptionSignals(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := c.DefaultQuery("market", "swap")
		if market != "spot" && market != "swap" {
			market = "swap"
		}
		limit := queryInt(c, "limit", 100, 10, 500)
		onlySignals := queryBool(c, "onlySignals", true)
		lookback := queryInt(c, "signalLookbackMinutes", 4320, 60, 10080)
		direction := c.DefaultQuery("direction", "all")
		if direction != "long" && direction != "short" && direction != "all" {
			direction = "all"
		}

		items, err := service.ListLatestAbsorptionSignals(c.Request.Context(), d.Store, market, onlySignals, limit, lookback, direction)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if items == nil {
			items = []model.AbsorptionSignalSnapshot{}
		}
		c.JSON(http.StatusOK, gin.H{
			"market": market, "onlySignals": onlySignals,
			"signalLookbackMinutes": lookback, "direction": direction,
			"items": items,
		})
	}
}

func handleHotMarkets(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		limit := queryInt(c, "limit", 50, 1, 500)
		sinceMinutes := queryInt(c, "sinceMinutes", 360, 5, 10080)
		eventType := c.Query("eventType")
		timeMode := strings.ToLower(c.DefaultQuery("timeMode", "local"))
		if timeMode != "utc" {
			timeMode = "local"
		}
		tzOffsetMin := queryInt(c, "tzOffsetMin", 0, -720, 720)
		offsetMs := int64(0)
		if timeMode == "local" {
			offsetMs = int64(tzOffsetMin) * 60 * 1000
		}

		nowMs := time.Now().UnixMilli()
		cutoffMs := nowMs - int64(sinceMinutes)*60*1000
		dayStartMs := floorBucketStartWithOffset(nowMs, 24*60*60*1000, offsetMs)
		args := []interface{}{market, cutoffMs}
		where := "market = ? AND event_time_ms >= ?"
		if eventType != "" {
			where += " AND event_type = ?"
			args = append(args, eventType)
		}
		args = append(args, limit)
		q := `SELECT * FROM anomaly_events WHERE ` + where + ` ORDER BY event_time_ms DESC LIMIT ?`

		var rows []model.AnomalyEvent
		if err := d.Store.SelectContext(c.Request.Context(), &rows, q, args...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var filtered []model.AnomalyEvent
		for _, r := range rows {
			if !binance.IsExcludedSymbol(r.Symbol) {
				filtered = append(filtered, r)
			}
		}
		if filtered == nil {
			filtered = []model.AnomalyEvent{}
		}

		type countRow struct {
			Symbol    string `db:"symbol"`
			EventType string `db:"event_type"`
			Cnt       int    `db:"cnt"`
		}
		var countRows []countRow
		_ = d.Store.SelectContext(
			c.Request.Context(),
			&countRows,
			`SELECT symbol, event_type, COUNT(*) AS cnt
FROM anomaly_events
WHERE market = ? AND event_time_ms >= ? AND event_time_ms <= ?
GROUP BY symbol, event_type`,
			market, dayStartMs, nowMs,
		)
		countMap := make(map[string]int, len(countRows))
		for _, r := range countRows {
			key := strings.ToUpper(r.Symbol) + "|" + strings.ToLower(r.EventType)
			countMap[key] = r.Cnt
		}

		type hotMarketItem struct {
			model.AnomalyEvent
			DailyAlertCount int `json:"dailyAlertCount"`
		}
		items := make([]hotMarketItem, 0, len(filtered))
		for _, r := range filtered {
			key := strings.ToUpper(r.Symbol) + "|" + strings.ToLower(r.EventType)
			items = append(items, hotMarketItem{
				AnomalyEvent:    r,
				DailyAlertCount: countMap[key],
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"market": market, "sinceMinutes": sinceMinutes,
			"eventType": eventType, "items": items,
			"timeMode": timeMode, "tzOffsetMin": tzOffsetMin, "dayStartMs": dayStartMs,
		})
	}
}

func handleAnomalyStats(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		sinceMinutes := queryInt(c, "sinceMinutes", 1440, 15, 20160)
		cutoffMs := time.Now().UnixMilli() - int64(sinceMinutes)*60*1000

		type countRow struct {
			EventType string `db:"event_type"`
			Cnt       int    `db:"cnt"`
		}
		var rows []countRow
		q := `SELECT event_type, COUNT(*) as cnt FROM anomaly_events WHERE market = ? AND event_time_ms >= ? GROUP BY event_type`
		if err := d.Store.SelectContext(c.Request.Context(), &rows, q, market, cutoffMs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		counts := make(map[string]int)
		for _, r := range rows {
			counts[r.EventType] = r.Cnt
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "sinceMinutes": sinceMinutes, "counts": counts})
	}
}

func handleSRLevels(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		symbol := c.Query("symbol")
		if symbol == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
			return
		}
		timeframe := c.DefaultQuery("timeframe", "4h")
		limit := queryInt(c, "limit", 30, 1, 200)

		var levels []model.SRLevel
		q := `SELECT * FROM sr_levels WHERE market = ? AND symbol = ? AND timeframe = ? ORDER BY strength_score DESC LIMIT ?`
		if err := d.Store.SelectContext(c.Request.Context(), &levels, q, market, symbol, timeframe, limit); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if levels == nil {
			levels = []model.SRLevel{}
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "symbol": symbol, "timeframe": timeframe, "items": levels})
	}
}
