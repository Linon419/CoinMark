package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerPotentialRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/potential", handlePotential(d))
	g.GET("/potential/notify", handleGetPotentialNotify(d))
	g.PUT("/potential/notify", handlePutPotentialNotify(d))
}

// handlePotential 潜力区：观察名单（低位高积累）、短时暴涨（4 小时涨 ≥ 10%），以及上榜记录和实盘统计。
func handlePotential(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		scanner := d.Hub.Potential()
		if scanner == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "potential disabled (BOLL_PUMP_WS_ENABLED / ClickHouse)"})
			return
		}
		watch, pump, updatedMs := scanner.Snapshot()
		history, stats, err := service.GetPotentialHistory(c.Request.Context(), d.Store, queryInt(c, "history", 100, 0, 500))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"watch": watch, "pump": pump, "updated_ms": updatedMs, "history": history, "stats": stats})
	}
}

// handleGetPotentialNotify 短时暴涨的 Telegram 通知开关。
func handleGetPotentialNotify(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.LoadPotentialNotifySettings(c.Request.Context(), d.Store)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writePotentialNotify(c, d, cfg)
	}
}

func handlePutPotentialNotify(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body service.PotentialNotifySettings
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		cfg, err := service.SavePotentialNotifySettings(c.Request.Context(), d.Store, body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writePotentialNotify(c, d, cfg)
	}
}

func writePotentialNotify(c *gin.Context, d *Deps, cfg service.PotentialNotifySettings) {
	_, chatConfigured := tgNotifyChatID(d)
	c.JSON(http.StatusOK, gin.H{"enabled": cfg.Enabled, "tg_configured": chatConfigured && d.Cfg.TGEnabled})
}
