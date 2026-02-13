package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/service"
)

const tgRankDefaultPageSize = 15

type tgRankItem struct {
	Rank          int     `json:"rank"`
	Symbol        string  `json:"symbol"`
	Score         float64 `json:"score"`
	RetPct        float64 `json:"retPct"`
	FlowBiasPct   float64 `json:"flowBiasPct"`
	ChangePct     float64 `json:"changePct"`
	OINotionalUSD float64 `json:"oiNotionalUsd"`
	NetUSD        float64 `json:"netUsd"`
	BiasPct       float64 `json:"biasPct"`
	RatioPct      float64 `json:"ratioPct"`
	MarketCapUSD  float64 `json:"marketCapUsd"`
	QuoteNotional float64 `json:"quoteNotional"`
}

type tgRankResponse struct {
	Kind       string       `json:"kind"`
	Title      string       `json:"title"`
	Limit      int          `json:"limit"`
	Page       int          `json:"page"`
	PageSize   int          `json:"pageSize"`
	Total      int          `json:"total"`
	TotalPages int          `json:"totalPages"`
	AsOfMs     int64        `json:"asOfMs"`
	Items      []tgRankItem `json:"items"`
}

func registerTGRankRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/tg/rank", handleTGRank(d))
}

func handleTGRank(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireClickHouse(c, d.CH) {
			return
		}
		if d.BN == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "binance client not configured"})
			return
		}

		kind := strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "openinterest")))
		limit := queryInt(c, "limit", 30, 1, 120)
		page := queryInt(c, "page", 1, 1, 100000)
		pageSize := queryInt(c, "pageSize", tgRankDefaultPageSize, 1, 50)

		ctx := c.Request.Context()
		resp := tgRankResponse{
			Kind:     kind,
			Limit:    limit,
			Page:     page,
			PageSize: pageSize,
			Items:    []tgRankItem{},
		}

		var (
			all []tgRankItem
			err error
		)

		switch kind {
		case "oicapratio":
			resp.Title = "oicapratio"
			all, err = buildOICapRatioRank(ctx, d.CH, limit)
		case "openinterest":
			resp.Title = "openinterest_1d"
			all, err = buildOpenInterestGrowthRank(ctx, d.CH, d.BN, limit)
		case "bullindex":
			resp.Title = "bullindex_1h"
			resp.AsOfMs, all, err = buildBullIndexRank(ctx, d.CH, limit)
		case "fi1d":
			resp.Title = "swap_flow_in_1d"
			all, err = buildFlowRank(ctx, d.CH, d.BN, "swap", "in", limit)
		case "fo1d":
			resp.Title = "swap_flow_out_1d"
			all, err = buildFlowRank(ctx, d.CH, d.BN, "swap", "out", limit)
		case "si1d":
			resp.Title = "spot_flow_in_1d"
			all, err = buildFlowRank(ctx, d.CH, d.BN, "spot", "in", limit)
		case "so1d":
			resp.Title = "spot_flow_out_1d"
			all, err = buildFlowRank(ctx, d.CH, d.BN, "spot", "out", limit)
		case "r15m":
			resp.Title = "returns_15m"
			resp.AsOfMs, all, err = buildReturnRank(ctx, d.CH, "15m", limit)
		case "r1h":
			resp.Title = "returns_1h"
			resp.AsOfMs, all, err = buildReturnRank(ctx, d.CH, "1h", limit)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported kind"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		resp.Total = len(all)
		start, end, totalPages, actualPage := paginateRank(len(all), page, pageSize)
		resp.Page = actualPage
		resp.TotalPages = totalPages
		if start >= 0 {
			resp.Items = all[start:end]
		}
		c.JSON(http.StatusOK, resp)
	}
}

func paginateRank(total, page, pageSize int) (start, end, totalPages, actualPage int) {
	if pageSize <= 0 {
		pageSize = tgRankDefaultPageSize
	}
	if total <= 0 {
		return -1, -1, 0, 1
	}
	totalPages = (total + pageSize - 1) / pageSize
	actualPage = page
	if actualPage < 1 {
		actualPage = 1
	}
	if actualPage > totalPages {
		actualPage = totalPages
	}
	start = (actualPage - 1) * pageSize
	end = start + pageSize
	if end > total {
		end = total
	}
	return start, end, totalPages, actualPage
}

func buildOICapRatioRank(ctx context.Context, ch *chrepo.Client, limit int) ([]tgRankItem, error) {
	rows, err := service.GetOIMarketCapRank(ctx, ch, limit)
	if err != nil {
		return nil, err
	}
	items := make([]tgRankItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, tgRankItem{
			Symbol:        row.Symbol,
			OINotionalUSD: row.OINotionalUSD,
			MarketCapUSD:  row.MarketCapUSD,
			RatioPct:      row.Ratio * 100,
		})
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, nil
}

func buildOpenInterestGrowthRank(ctx context.Context, ch *chrepo.Client, bn *binance.Client, limit int) ([]tgRankItem, error) {
	rows, err := ch.QueryOISnapshots(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	candidateCount := limit * 3
	if candidateCount < 40 {
		candidateCount = 40
	}
	if candidateCount > 120 {
		candidateCount = 120
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].OINotionalUSD > rows[j].OINotionalUSD })
	if len(rows) > candidateCount {
		rows = rows[:candidateCount]
	}

	type result struct {
		item tgRankItem
		ok   bool
	}

	sem := make(chan struct{}, 8)
	outCh := make(chan result, len(rows))
	var wg sync.WaitGroup
	for _, row := range rows {
		r := row
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			hist, e := bn.GetOpenInterestHist(ctx, r.Symbol, "1d", 2)
			if e != nil || len(hist) < 2 {
				outCh <- result{}
				return
			}
			sort.Slice(hist, func(i, j int) bool { return tgToI64(hist[i]["timestamp"]) < tgToI64(hist[j]["timestamp"]) })
			prev := tgToF64(hist[len(hist)-2]["sumOpenInterestValue"])
			curr := tgToF64(hist[len(hist)-1]["sumOpenInterestValue"])
			if prev <= 0 || curr <= 0 {
				outCh <- result{}
				return
			}
			outCh <- result{
				ok: true,
				item: tgRankItem{
					Symbol:        r.Symbol,
					ChangePct:     (curr - prev) / prev * 100,
					OINotionalUSD: r.OINotionalUSD,
				},
			}
		}()
	}
	wg.Wait()
	close(outCh)

	items := make([]tgRankItem, 0, len(rows))
	for r := range outCh {
		if r.ok {
			items = append(items, r.item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ChangePct > items[j].ChangePct })
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, nil
}

func buildBullIndexRank(ctx context.Context, ch *chrepo.Client, limit int) (int64, []tgRankItem, error) {
	const hourMs int64 = 60 * 60 * 1000
	nowMs := time.Now().UnixMilli()
	lastClosed := (nowMs/hourMs)*hourMs - hourMs

	target := int64(0)
	for i := 0; i < 24; i++ {
		start := lastClosed - int64(i)*hourMs
		end := start + hourMs - 1
		rows, _ := ch.QueryTradeBuckets(ctx, "swap", "", nil, "1h", start, end, "asc", 1)
		if len(rows) > 0 {
			target = start
			break
		}
	}
	if target == 0 {
		return 0, nil, nil
	}

	rows, err := ch.QueryTradeBuckets(ctx, "swap", "", nil, "1h", target, target+hourMs-1, "asc", 0)
	if err != nil || len(rows) == 0 {
		return target, nil, err
	}

	items := make([]tgRankItem, 0, len(rows))
	for _, row := range rows {
		if row.OpenPrice == nil || row.ClosePrice == nil || *row.OpenPrice <= 0 || binance.IsExcludedSymbol(row.Symbol) {
			continue
		}
		retPct := (*row.ClosePrice - *row.OpenPrice) / *row.OpenPrice * 100
		flowBias := 0.0
		if denom := row.TakerBuyNotional + row.TakerSellNotional; denom > 0 {
			flowBias = (row.TakerBuyNotional - row.TakerSellNotional) / denom
		}
		score := 50 + retPct*2 + flowBias*50
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		items = append(items, tgRankItem{
			Symbol:      row.Symbol,
			Score:       score,
			RetPct:      retPct,
			FlowBiasPct: flowBias * 100,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return target, items, nil
}

func buildFlowRank(ctx context.Context, ch *chrepo.Client, bn *binance.Client, market, direction string, limit int) ([]tgRankItem, error) {
	tickers, err := bn.GetTicker24hAll(ctx, market)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(tickers))
	for _, t := range tickers {
		symbol, _ := t["symbol"].(string)
		if symbol == "" || binance.IsExcludedSymbol(symbol) {
			continue
		}
		symbols = append(symbols, symbol)
	}
	if len(symbols) == 0 {
		return nil, nil
	}

	rows, err := ch.QueryTradeFlowAgg(ctx, market, symbols, "1m", time.Now().UnixMilli()-24*60*60*1000)
	if err != nil {
		return nil, err
	}
	items := make([]tgRankItem, 0, len(rows))
	for _, row := range rows {
		if binance.IsExcludedSymbol(row.Symbol) {
			continue
		}
		net := row.BuySum - row.SellSum
		biasPct := 0.0
		if total := row.BuySum + row.SellSum; total > 0 {
			biasPct = net / total * 100
		}
		if direction == "in" && net > 0 {
			items = append(items, tgRankItem{Symbol: row.Symbol, NetUSD: net, BiasPct: biasPct})
		}
		if direction == "out" && net < 0 {
			items = append(items, tgRankItem{Symbol: row.Symbol, NetUSD: net, BiasPct: biasPct})
		}
	}
	if direction == "in" {
		sort.Slice(items, func(i, j int) bool { return items[i].NetUSD > items[j].NetUSD })
	} else {
		sort.Slice(items, func(i, j int) bool { return items[i].NetUSD < items[j].NetUSD })
	}
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, nil
}

func buildReturnRank(ctx context.Context, ch *chrepo.Client, bucket string, limit int) (int64, []tgRankItem, error) {
	bucketMs := tgBucketToMs(bucket)
	nowMs := time.Now().UnixMilli()
	bucketEnd := (nowMs / bucketMs) * bucketMs
	bucketStart := bucketEnd - bucketMs

	rows, err := ch.QueryTradeBuckets(ctx, "swap", "", nil, bucket, bucketStart, bucketEnd-1, "asc", 0)
	if err != nil {
		return bucketStart, nil, err
	}

	items := make([]tgRankItem, 0, len(rows))
	for _, row := range rows {
		if binance.IsExcludedSymbol(row.Symbol) || row.OpenPrice == nil || row.ClosePrice == nil || *row.OpenPrice <= 0 {
			continue
		}
		retPct := (*row.ClosePrice / *row.OpenPrice - 1) * 100
		items = append(items, tgRankItem{
			Symbol:        row.Symbol,
			RetPct:        retPct,
			QuoteNotional: row.QuoteNotional,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RetPct > items[j].RetPct })
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return bucketStart, items, nil
}

func tgBucketToMs(bucket string) int64 {
	switch bucket {
	case "15m":
		return 15 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	default:
		return 60 * 60 * 1000
	}
}

func tgToI64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func tgToF64(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}
