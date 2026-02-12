package handler

import (
	"net/http"
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

		cutoffMs := time.Now().UnixMilli() - int64(sinceMinutes)*60*1000
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
		c.JSON(http.StatusOK, gin.H{
			"market": market, "sinceMinutes": sinceMinutes,
			"eventType": eventType, "items": filtered,
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
