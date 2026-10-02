package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerPotentialRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/potential", handlePotential(d))
}

// handlePotential 潜力区：观察名单（低位高积累）、别追名单（没积累的暴涨），以及上榜记录和实盘统计。
func handlePotential(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		scanner := d.Hub.Potential()
		if scanner == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "potential disabled (BOLL_PUMP_WS_ENABLED / ClickHouse)"})
			return
		}
		watch, avoid, updatedMs := scanner.Snapshot()
		history, stats, err := service.GetPotentialHistory(c.Request.Context(), d.Store, queryInt(c, "history", 100, 0, 500))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"watch": watch, "avoid": avoid, "updated_ms": updatedMs, "history": history, "stats": stats})
	}
}
