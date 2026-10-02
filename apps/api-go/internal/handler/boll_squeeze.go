package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func registerBollSqueezeRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/boll-squeeze", handleBollSqueeze(d))
}

// handleBollSqueeze 上涨趋势中 BOLL 缩口、价格接近下轨的合约（15m/30m/1h/4h）。
func handleBollSqueeze(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		scanner := d.Hub.BollSqueeze()
		if scanner == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "boll squeeze disabled (BOLL_PUMP_WS_ENABLED)"})
			return
		}
		rows, updatedMs := scanner.Snapshot()
		c.JSON(http.StatusOK, gin.H{"items": rows, "updated_ms": updatedMs})
	}
}
