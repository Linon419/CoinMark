package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/service"
)

type tgNotifyPrefsPatch struct {
	AbnormalEventsEnabled *bool `json:"abnormalEventsEnabled"`
	WhaleWallEnabled      *bool `json:"whaleWallEnabled"`
	AbsorptionEnabled     *bool `json:"absorptionEnabled"`
	BollPumpEnabled       *bool `json:"bollPumpEnabled"`
	MuteAll               *bool `json:"muteAll"`
}

func registerTGNotifyPrefsRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/telegram/notify-prefs", handleGetTGNotifyPrefs(d))
	g.PATCH("/telegram/notify-prefs", handlePatchTGNotifyPrefs(d))
}

func handleGetTGNotifyPrefs(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		chatID, configured := tgNotifyChatID(d)
		if d.Store == nil || !configured {
			writeTGNotifyPrefsResponse(c, d, service.DefaultTGNotifyPrefs(chatID), configured)
			return
		}

		prefs, err := service.LoadTGNotifyPrefs(c.Request.Context(), d.Store, chatID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load telegram notify prefs failed"})
			return
		}
		writeTGNotifyPrefsResponse(c, d, prefs, configured)
	}
}

func handlePatchTGNotifyPrefs(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		chatID, configured := tgNotifyChatID(d)
		if d.Store == nil || !configured {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram notify chat not configured"})
			return
		}

		var patch tgNotifyPrefsPatch
		if err := c.ShouldBindJSON(&patch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}

		prefs, err := service.LoadTGNotifyPrefs(context.Background(), d.Store, chatID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load telegram notify prefs failed"})
			return
		}
		if patch.AbnormalEventsEnabled != nil {
			prefs.MarketAnomalyEnabled = *patch.AbnormalEventsEnabled
		}
		if patch.WhaleWallEnabled != nil {
			prefs.WhaleWallEnabled = *patch.WhaleWallEnabled
		}
		if patch.AbsorptionEnabled != nil {
			prefs.AbsorptionEnabled = *patch.AbsorptionEnabled
		}
		if patch.BollPumpEnabled != nil {
			prefs.BollPumpEnabled = *patch.BollPumpEnabled
		}
		if patch.MuteAll != nil {
			prefs.MuteAll = *patch.MuteAll
		}

		if err := service.SaveTGNotifyPrefs(c.Request.Context(), d.Store, prefs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save telegram notify prefs failed"})
			return
		}
		writeTGNotifyPrefsResponse(c, d, prefs, configured)
	}
}

func tgNotifyChatID(d *Deps) (int64, bool) {
	if d == nil || d.Cfg == nil {
		return 0, false
	}
	raw := strings.TrimSpace(d.Cfg.TGNotifyChatID)
	if raw == "" {
		return 0, false
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	return chatID, err == nil && chatID != 0
}

func writeTGNotifyPrefsResponse(c *gin.Context, d *Deps, prefs service.TGNotifyPrefs, configured bool) {
	enabled := false
	market := "swap"
	if d != nil && d.Cfg != nil {
		enabled = d.Cfg.TGEnabled
		if strings.TrimSpace(d.Cfg.TGNotifyMarket) != "" {
			market = d.Cfg.TGNotifyMarket
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"configured":            configured,
		"enabled":               enabled,
		"market":                market,
		"abnormalEventsEnabled": prefs.MarketAnomalyEnabled,
		"whaleWallEnabled":      prefs.WhaleWallEnabled,
		"absorptionEnabled":     prefs.AbsorptionEnabled,
		"bollPumpEnabled":       prefs.BollPumpEnabled,
		"muteAll":               prefs.MuteAll,
	})
}
