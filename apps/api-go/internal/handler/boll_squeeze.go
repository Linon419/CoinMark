package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerBollSqueezeRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/boll-squeeze", handleBollSqueeze(d))
	g.GET("/boll-squeeze/notify", handleGetBollSqueezeNotify(d))
	g.PUT("/boll-squeeze/notify", handlePutBollSqueezeNotify(d))
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

// handleGetBollSqueezeNotify 布林回踩 Telegram 通知设置（开关、周期、收藏币）。
func handleGetBollSqueezeNotify(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.LoadBollSqueezeNotifySettings(c.Request.Context(), d.Store)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writeBollSqueezeNotify(c, d, cfg)
	}
}

func handlePutBollSqueezeNotify(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body service.BollSqueezeNotifySettings
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		cfg, err := service.SaveBollSqueezeNotifySettings(c.Request.Context(), d.Store, body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writeBollSqueezeNotify(c, d, cfg)
	}
}

func writeBollSqueezeNotify(c *gin.Context, d *Deps, cfg service.BollSqueezeNotifySettings) {
	_, chatConfigured := tgNotifyChatID(d)
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"timeframes":     cfg.Timeframes,
		"favorites":      cfg.Favorites,
		"tg_configured":  chatConfigured && d.Cfg.TGEnabled,
		"all_timeframes": service.BollSqueezeTimeframes,
	})
}
