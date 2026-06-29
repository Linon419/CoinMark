package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/hub"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

type Deps struct {
	Cfg   *config.Config
	Store *sqlite.Store
	CH    *chrepo.Client
	BN    *binance.Client
	Hub   *hub.Runtime
}

func RegisterRoutes(r *gin.Engine, d *Deps) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api := r.Group("/api")
	registerSymbolRoutes(api, d)
	registerUserRoutes(api, d)
	registerAggregateRoutes(api, d)
	registerTGRankRoutes(api, d)
	registerAnomalyRoutes(api, d)
	registerCoinRoutes(api, d)
	registerSignalLabRoutes(api, d)
	registerBollPumpRoutes(api, d)
	registerHubRoutes(api, d)
	registerTGNotifyPrefsRoutes(api, d)

	bot := api.Group("/bot")
	registerBotRoutes(bot, d)
}

// shared helpers

func queryInt(c *gin.Context, key string, def, min, max int) int {
	s := c.Query(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func queryFloat(c *gin.Context, key string, def float64) float64 {
	s := c.Query(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func queryMarket(c *gin.Context) string {
	m := strings.ToLower(c.DefaultQuery("market", "swap"))
	if m != "spot" && m != "swap" {
		return "swap"
	}
	return m
}

func queryBool(c *gin.Context, key string, def bool) bool {
	s := c.Query(key)
	if s == "" {
		return def
	}
	return s == "true" || s == "1"
}

func requireClickHouse(c *gin.Context, ch *chrepo.Client) bool {
	if ch != nil {
		return true
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured"})
	return false
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func filterExcluded(symbols []string) []string {
	return binance.FilterExcludedSymbols(symbols)
}
