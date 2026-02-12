package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerSignalLabRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/signal-lab/realtime", handleSignalLabRealtime(d))
	g.POST("/signal-lab/backtest", handleSignalLabBacktest(d))
	g.GET("/signal-lab/runs/:runId", handleSignalLabRunStatus(d))
}

func handleSignalLabRealtime(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		marketScope := c.DefaultQuery("market", "both")
		if marketScope != "spot" && marketScope != "swap" && marketScope != "both" {
			marketScope = "both"
		}
		limit := queryInt(c, "limit", 100, 10, 300)
		minState := c.DefaultQuery("minSignalState", "CONFIRM")
		syncScoreFlow := queryBool(c, "syncScoreFlow", true)

		params := service.DefaultSignalLabParams()
		params.Bucket = c.DefaultQuery("bucket", params.Bucket)
		params.ZThreshold = queryFloat(c, "zThreshold", params.ZThreshold)
		params.LookbackMinutes = queryInt(c, "lookbackMinutes", params.LookbackMinutes, 30, 1440)
		params.DetectionWindowMinutes = queryInt(c, "detectionWindowMinutes", params.DetectionWindowMinutes, 5, 120)
		params.MinLargeCount = queryInt(c, "minLargeCount", params.MinLargeCount, 1, 50)
		params.BuyRatioThreshold = queryFloat(c, "buyRatioThreshold", params.BuyRatioThreshold)
		params.MinPersistentSpanMinutes = queryInt(c, "minPersistentSpanMinutes", params.MinPersistentSpanMinutes, 1, 120)
		params.MinAvgIntervalMinutes = queryInt(c, "minAvgIntervalMinutes", params.MinAvgIntervalMinutes, 1, 1440)
		params.MinDistinctTimeBuckets = queryInt(c, "minDistinctTimeBuckets", params.MinDistinctTimeBuckets, 1, 30)
		params.CooldownMinutes = queryInt(c, "cooldownMinutes", params.CooldownMinutes, 1, 1440)
		params.SingleLargeZThreshold = queryFloat(c, "singleLargeZThreshold", params.SingleLargeZThreshold)
		params.SingleLargeMinNotional = queryFloat(c, "singleLargeMinNotional", params.SingleLargeMinNotional)
		params.SingleLargeCooldownMin = queryInt(c, "singleLargeCooldownMinutes", params.SingleLargeCooldownMin, 1, 1440)
		params.SlopeWindowMinutes = queryInt(c, "slopeWindowMinutes", params.SlopeWindowMinutes, 5, 120)
		params.SlopeR2Threshold = queryFloat(c, "slopeR2Threshold", params.SlopeR2Threshold)
		params.SymbolLimit = queryInt(c, "symbolLimit", params.SymbolLimit, 10, 500)

		result, err := service.GetRealtimeSignals(c.Request.Context(), d.CH, d.Store, marketScope, params, limit, minState, syncScoreFlow)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func handleSignalLabBacktest(d *Deps) gin.HandlerFunc {
	type body struct {
		Market string                  `json:"market"`
		Days   int                     `json:"days"`
		Params *service.SignalLabParams `json:"params"`
	}
	return func(c *gin.Context) {
		var b body
		if err := c.ShouldBindJSON(&b); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if b.Market == "" {
			b.Market = "both"
		}
		if b.Days < 1 {
			b.Days = 7
		}
		if b.Days > 30 {
			b.Days = 30
		}
		params := service.DefaultSignalLabParams()
		if b.Params != nil {
			params = *b.Params
		}

		runID := service.StartBacktestRun(c.Request.Context(), d.CH, d.Store, b.Market, b.Days, params)
		c.JSON(http.StatusOK, gin.H{"runId": runID, "status": "running"})
	}
}

func handleSignalLabRunStatus(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID := c.Param("runId")
		run := service.GetBacktestRun(runID)
		if run == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		c.JSON(http.StatusOK, run)
	}
}
