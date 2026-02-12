package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerBotRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/fundingRateTop", handleFundingRateTop(d))
	g.GET("/longShortVolumeRank", handleLSVolumeRank(d))
	g.GET("/oiMarketCapRank", handleOIMcapRank(d))
}

func handleFundingRateTop(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := queryInt(c, "limit", 15, 1, 200)
		order := c.DefaultQuery("order", "abs")
		items, err := service.GetFundingRateTop(c.Request.Context(), d.CH, limit, order)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func handleLSVolumeRank(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		market := queryMarket(c)
		bucket := c.DefaultQuery("bucket", "1h")
		if bucket != "15m" && bucket != "1h" && bucket != "1d" {
			bucket = "1h"
		}
		limit := queryInt(c, "limit", 10, 1, 200)
		result, err := service.GetLongShortVolumeRank(c.Request.Context(), d.Store, d.Cfg, market, bucket, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func handleOIMcapRank(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := queryInt(c, "limit", 15, 1, 200)
		items, err := service.GetOIMarketCapRank(c.Request.Context(), d.CH, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}
