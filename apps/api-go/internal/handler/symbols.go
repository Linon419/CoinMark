package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func registerSymbolRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/symbol/getpairs", func(c *gin.Context) {
		market := queryMarket(c)
		pairs, err := d.BN.GetPairs(c.Request.Context(), market)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"market": market, "pairs": pairs})
	})
}
