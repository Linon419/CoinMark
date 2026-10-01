package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerEtfRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/etf/flows", handleEtfFlows(d))
}

// handleEtfFlows 美国现货加密 ETF 每日资金流，按币汇总，按最新一日净流入排序。
func handleEtfFlows(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		days := queryInt(c, "days", 20, 1, 60)
		items, err := service.GetEtfFlowSummaries(c.Request.Context(), d.Store, days)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "days": days, "source": "sosovalue"})
	}
}
