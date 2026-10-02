package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

func registerBollSqueezeRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/boll-squeeze", handleBollSqueeze(d))
	g.GET("/boll-squeeze/notify", handleGetPullbackNotify(d, service.BollSqueezeNotifyName))
	g.PUT("/boll-squeeze/notify", handlePutPullbackNotify(d, service.BollSqueezeNotifyName))
	g.GET("/ema-pullback", handleEMAPullback(d))
	g.GET("/ema-pullback/notify", handleGetPullbackNotify(d, service.EMAPullbackNotifyName))
	g.PUT("/ema-pullback/notify", handlePutPullbackNotify(d, service.EMAPullbackNotifyName))
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

// handleEMAPullback 上涨趋势中回踩 EMA100/EMA200 的合约（15m/30m/1h/4h）。
func handleEMAPullback(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		scanner := d.Hub.EMAPullback()
		if scanner == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ema pullback disabled (BOLL_PUMP_WS_ENABLED)"})
			return
		}
		rows, updatedMs := scanner.Snapshot()
		c.JSON(http.StatusOK, gin.H{"items": rows, "updated_ms": updatedMs})
	}
}

// handleGetPullbackNotify 回踩类 Telegram 通知设置（开关、周期、收藏币），name 区分布林回踩 / EMA 回踩。
func handleGetPullbackNotify(d *Deps, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.LoadPullbackNotifySettings(c.Request.Context(), d.Store, name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writePullbackNotify(c, d, cfg)
	}
}

func handlePutPullbackNotify(d *Deps, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body service.PullbackNotifySettings
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		cfg, err := service.SavePullbackNotifySettings(c.Request.Context(), d.Store, name, body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writePullbackNotify(c, d, cfg)
	}
}

func writePullbackNotify(c *gin.Context, d *Deps, cfg service.PullbackNotifySettings) {
	_, chatConfigured := tgNotifyChatID(d)
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"timeframes":     cfg.Timeframes,
		"favorites":      cfg.Favorites,
		"tg_configured":  chatConfigured && d.Cfg.TGEnabled,
		"all_timeframes": service.PullbackTimeframes,
	})
}
