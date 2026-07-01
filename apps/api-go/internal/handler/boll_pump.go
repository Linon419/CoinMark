package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/service"
)

func registerBollPumpRoutes(g *gin.RouterGroup, d *Deps) {
	r := g.Group("/boll-pump")
	r.GET("/signals", handleBollPumpSignals(d))
	r.GET("/states", handleBollPumpStates(d))
	r.GET("/stats", handleBollPumpStats(d))
	r.GET("/settings", handleBollPumpSettings(d))
	r.PUT("/settings", handlePutBollPumpSettings(d))
	r.GET("/flow-snapshots", handleBollPumpFlowSnapshots(d))
	r.GET("/signals/:id/detail", handleBollPumpSignalDetail(d))
}

type bollPumpStateView struct {
	model.BollPumpState
	DominantTimeframe string `json:"dominant_timeframe"`
}

func handleBollPumpSignals(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := service.ListBollPumpSignals(c.Request.Context(), d.Store, service.BollPumpSignalFilter{
			Market:      queryMarket(c),
			Symbol:      strings.ToUpper(c.Query("symbol")),
			Timeframe:   c.Query("timeframe"),
			SignalLevel: c.Query("signal_level"),
			MinScore:    queryFloat(c, "min_score", 0),
			SinceMs:     queryInt64(c, "since", 0),
			Limit:       queryInt(c, "limit", 100, 1, 1000),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows, "limit": len(rows)})
	}
}

func handleBollPumpStates(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := service.ListBollPumpStates(c.Request.Context(), d.Store, service.BollPumpStateFilter{
			Market:           queryMarket(c),
			Symbol:           strings.ToUpper(c.Query("symbol")),
			Timeframe:        c.Query("timeframe"),
			Status:           c.Query("status"),
			MinPriorityScore: queryFloat(c, "min_priority_score", 0),
			Limit:            queryInt(c, "limit", 100, 1, 1000),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		views, err := bollPumpStateViews(c.Request.Context(), d, rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": views, "limit": len(rows)})
	}
}

func handleBollPumpFlowSnapshots(d *Deps) gin.HandlerFunc {
	type item struct {
		Symbol        string  `json:"symbol"`
		BuyNotional   float64 `json:"buyNotional"`
		SellNotional  float64 `json:"sellNotional"`
		NetNotional   float64 `json:"netNotional"`
		QuoteNotional float64 `json:"quoteNotional"`
	}

	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		symbols := bollPumpQuerySymbols(c.Query("symbols"), 60)
		if len(symbols) == 0 {
			c.JSON(http.StatusOK, gin.H{"items": []item{}})
			return
		}

		timeMode := c.DefaultQuery("timeMode", "local")
		if timeMode != "utc" {
			timeMode = "local"
		}
		tzOffsetMin := queryInt(c, "tzOffsetMin", 0, -720, 720)
		offsetMs := int64(0)
		if timeMode == "local" {
			offsetMs = int64(tzOffsetMin) * 60 * 1000
		}

		const dayMs int64 = 24 * 60 * 60 * 1000
		nowMs := time.Now().UnixMilli()
		dayStartMs := floorBucketStartWithOffset(nowMs, dayMs, offsetMs)
		rows, err := d.CH.QueryTradeFlowAggRange(c.Request.Context(), queryMarket(c), symbols, "1m", dayStartMs, nowMs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		bySymbol := make(map[string]item, len(rows))
		for _, row := range rows {
			net := row.BuySum - row.SellSum
			bySymbol[row.Symbol] = item{
				Symbol:        row.Symbol,
				BuyNotional:   row.BuySum,
				SellNotional:  row.SellSum,
				NetNotional:   net,
				QuoteNotional: row.BuySum + row.SellSum,
			}
		}

		items := make([]item, 0, len(symbols))
		for _, symbol := range symbols {
			if got, ok := bySymbol[symbol]; ok {
				items = append(items, got)
				continue
			}
			items = append(items, item{Symbol: symbol})
		}
		c.JSON(http.StatusOK, gin.H{
			"market":      queryMarket(c),
			"timeMode":    timeMode,
			"tzOffsetMin": tzOffsetMin,
			"dayStartMs":  dayStartMs,
			"items":       items,
		})
	}
}

func bollPumpQuerySymbols(raw string, limit int) []string {
	if limit <= 0 {
		limit = 60
	}
	seen := map[string]bool{}
	out := make([]string, 0, limit)
	for _, part := range strings.Split(raw, ",") {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" || seen[symbol] {
			continue
		}
		seen[symbol] = true
		out = append(out, symbol)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func bollPumpStateViews(ctx context.Context, d *Deps, rows []model.BollPumpState) ([]bollPumpStateView, error) {
	out := make([]bollPumpStateView, 0, len(rows))
	bySymbol := map[string][]model.BollPumpState{}
	for _, row := range rows {
		bySymbol[row.Symbol] = append(bySymbol[row.Symbol], row)
	}
	if d != nil && d.Store != nil {
		seen := map[string]bool{}
		for _, row := range rows {
			key := row.Market + ":" + row.Symbol
			if seen[key] {
				continue
			}
			seen[key] = true
			peers, err := service.ListBollPumpStates(ctx, d.Store, service.BollPumpStateFilter{Market: row.Market, Symbol: row.Symbol, Limit: 20})
			if err != nil {
				return nil, err
			}
			bySymbol[row.Symbol] = peers
		}
	}
	for _, row := range rows {
		out = append(out, bollPumpStateView{
			BollPumpState:     row,
			DominantTimeframe: bollPumpDominantTimeframe(row, bySymbol[row.Symbol]),
		})
	}
	return out, nil
}

func bollPumpDominantTimeframe(row model.BollPumpState, peers []model.BollPumpState) string {
	baseRank := bollPumpTimeframeRank(row.Timeframe)
	if baseRank < 0 {
		return row.Timeframe
	}
	best := row.Timeframe
	bestRank := 99
	for _, peer := range peers {
		rank := bollPumpTimeframeRank(peer.Timeframe)
		if rank <= baseRank || rank >= bestRank {
			continue
		}
		if peer.PriorityScore < 70 || peer.Status == string(service.BollPumpStatusInvalidated) {
			continue
		}
		best = peer.Timeframe
		bestRank = rank
	}
	return best
}

func bollPumpTimeframeRank(tf string) int {
	switch tf {
	case "1m":
		return 0
	case "3m":
		return 1
	case "5m":
		return 2
	case "15m":
		return 3
	case "30m":
		return 4
	case "1h":
		return 5
	default:
		return -1
	}
}

func handleBollPumpStats(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := service.BollPumpStats(c.Request.Context(), d.Store, queryMarket(c), queryInt64(c, "since", time.Now().UnixMilli()-30*24*60*60*1000))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, stats)
	}
}

func handleBollPumpSettings(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.LoadBollPumpConfig(c.Request.Context(), d.Store, bollPumpRuntimeConfig(d))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"settings": cfg})
	}
}

func handlePutBollPumpSettings(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d.Store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "settings store not configured"})
			return
		}
		var cfg service.BollPumpConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		cfg, err := service.SaveBollPumpConfig(c.Request.Context(), d.Store, cfg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"settings": cfg})
	}
}

func handleBollPumpSignalDetail(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signal id"})
			return
		}
		sig, err := service.GetBollPumpSignal(c.Request.Context(), d.Store, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if sig == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "signal not found"})
			return
		}
		var st *model.BollPumpState
		st, _ = service.GetBollPumpState(c.Request.Context(), d.Store, sig.Market, sig.Symbol, sig.Timeframe)
		candles, indicators := bollPumpDetailSeries(c.Request.Context(), d, *sig)
		c.JSON(http.StatusOK, gin.H{
			"signal":     sig,
			"state":      st,
			"candles":    candles,
			"indicators": indicators,
			"markers":    bollPumpMarkers(*sig),
		})
	}
}

func bollPumpDetailSeries(ctx context.Context, d *Deps, sig model.BollPumpSignal) ([]map[string]interface{}, []map[string]interface{}) {
	if d == nil || d.BN == nil {
		return []map[string]interface{}{}, []map[string]interface{}{}
	}
	source := service.NewBinanceBollPumpSource(d.BN, 1)
	bars, err := source.Klines(ctx, sig.Market, sig.Symbol, sig.Timeframe, 260)
	if err != nil {
		return []map[string]interface{}{}, []map[string]interface{}{}
	}
	cfg, _ := service.LoadBollPumpConfig(ctx, d.Store, bollPumpRuntimeConfig(d))
	ind := service.ComputeBollPumpIndicators(bars, cfg.BollPeriod, cfg.BollStdDev, cfg.ATRPeriod)
	candles := make([]map[string]interface{}, 0, len(bars))
	indicators := make([]map[string]interface{}, 0, len(ind))
	for i, b := range bars {
		candles = append(candles, map[string]interface{}{
			"time": b.OpenTimeMs, "open": b.Open, "high": b.High, "low": b.Low, "close": b.Close, "volume": b.Volume,
		})
		in := ind[i]
		indicators = append(indicators, map[string]interface{}{
			"time": b.OpenTimeMs, "middle": in.Middle, "upper": in.Upper, "lower": in.Lower, "atr14": in.ATR14, "bandwidth": in.Bandwidth,
		})
	}
	return candles, indicators
}

func bollPumpRuntimeConfig(d *Deps) service.BollPumpConfig {
	if d == nil {
		return service.DefaultBollPumpConfig()
	}
	return service.BollPumpConfigFromRuntime(d.Cfg)
}

func bollPumpMarkers(sig model.BollPumpSignal) []map[string]interface{} {
	out := []map[string]interface{}{
		{"time": sig.CandleStartMs, "label": sig.SignalLevel, "price": sig.Price, "kind": "signal"},
	}
	if sig.PullbackCandleStartMs != nil {
		out = append(out, map[string]interface{}{"time": *sig.PullbackCandleStartMs, "label": "pullback", "price": sig.Price, "kind": "pullback"})
	}
	return out
}

func queryInt64(c *gin.Context, key string, def int64) int64 {
	s := c.Query(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
