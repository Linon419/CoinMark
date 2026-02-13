package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/repo/sqlite"
)

const (
	whaleWallFarEventType      = "whale_wall_far"
	whaleWallFarMinDistancePct = 2.0
	whaleWallFarMinNotionalUSD = 1_000_000.0
)

// ---------------------------------------------------------------------------
// Heatmap helpers
// ---------------------------------------------------------------------------

func parseStepOverrides(raw string) map[string]float64 {
	out := make(map[string]float64)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, ":") {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		sym := strings.ToUpper(strings.TrimSpace(kv[0]))
		if sym == "" {
			continue
		}
		var step float64
		if _, err := parseF64(kv[1], &step); err != nil || step <= 0 {
			continue
		}
		if !strings.HasSuffix(sym, "USDT") {
			sym += "USDT"
		}
		out[sym] = step
	}
	return out
}

func parseF64(s string, out *float64) (bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false, nil
	}
	v := 0.0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			frac := 0.0
			div := 1.0
			for j := i + 1; j < len(s); j++ {
				if s[j] < '0' || s[j] > '9' {
					return false, nil
				}
				div *= 10
				frac += float64(s[j]-'0') / div
			}
			v += frac
			break
		}
		if s[i] < '0' || s[i] > '9' {
			return false, nil
		}
		v = v*10 + float64(s[i]-'0')
	}
	*out = v
	return true, nil
}

func calcPriceStep(symbol string, midPrice float64, cfg *config.Config) float64 {
	overrides := parseStepOverrides(cfg.DepthHeatmapStepOverrides)
	if forced, ok := overrides[strings.ToUpper(symbol)]; ok && forced > 0 {
		return forced
	}
	bps := math.Max(1.0, cfg.DepthHeatmapStepBps)
	rawStep := midPrice * (bps / 10000.0)
	if rawStep <= 0 {
		rawStep = midPrice * 0.0008
	}
	absMid := math.Abs(midPrice)
	var tick float64
	switch {
	case absMid >= 10000:
		tick = 10.0
	case absMid >= 1000:
		tick = 1.0
	case absMid >= 100:
		tick = 0.1
	case absMid >= 10:
		tick = 0.01
	case absMid >= 1:
		tick = 0.001
	default:
		tick = 0.0001
	}
	return math.Max(tick, math.Round(rawStep/tick)*tick)
}

type heatmapRow struct {
	Market        string
	Symbol        string
	BucketStartMs int64
	Side          string
	PriceBin      float64
	PriceStep     float64
	Intensity     float64
	LevelCount    int
}

func buildHeatmapRows(market, symbol string, depth map[string]interface{}, tsMs int64, cfg *config.Config) ([]heatmapRow, float64) {
	bids, _ := depth["bids"].([]interface{})
	asks, _ := depth["asks"].([]interface{})
	if len(bids) == 0 || len(asks) == 0 {
		return nil, 0
	}
	bestBid := levelPrice(bids[0])
	bestAsk := levelPrice(asks[0])
	if bestBid <= 0 || bestAsk <= 0 {
		return nil, 0
	}
	mid := (bestBid + bestAsk) / 2.0
	if mid <= 0 {
		return nil, 0
	}
	step := calcPriceStep(symbol, mid, cfg)
	if step <= 0 {
		return nil, 0
	}

	type binKey struct {
		price float64
		side  string
	}
	type binVal struct {
		intensity float64
		count     int
	}
	bins := make(map[binKey]*binVal)
	appendLevels := func(levels []interface{}, side string) {
		for _, lv := range levels {
			arr, ok := lv.([]interface{})
			if !ok || len(arr) < 2 {
				continue
			}
			price := ifaceFloat(arr[0])
			qty := ifaceFloat(arr[1])
			if price <= 0 || qty <= 0 {
				continue
			}
			priceBin := math.Round(math.Floor(price/step)*step*1e10) / 1e10
			notional := price * qty
			key := binKey{priceBin, side}
			if b, ok := bins[key]; ok {
				b.intensity += notional
				b.count++
			} else {
				bins[key] = &binVal{notional, 1}
			}
		}
	}
	appendLevels(bids, "bid")
	appendLevels(asks, "ask")

	minIntensity := math.Max(0, cfg.DepthHeatmapMinIntensityUSD)
	bucketStartMs := (tsMs / 60000) * 60000
	var rows []heatmapRow
	for key, val := range bins {
		if val.intensity < minIntensity {
			continue
		}
		rows = append(rows, heatmapRow{
			Market: market, Symbol: symbol, BucketStartMs: bucketStartMs,
			Side: key.side, PriceBin: key.price, PriceStep: step,
			Intensity: val.intensity, LevelCount: val.count,
		})
	}
	return rows, mid
}

func writeHeatmapRows(ctx context.Context, store *sqlite.Store, rows []heatmapRow) error {
	if len(rows) == 0 {
		return nil
	}
	sql := `INSERT INTO orderbook_heatmap_1m
(market, symbol, bucket_start_ms, side, price_bin, price_step, intensity, level_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(market, symbol, bucket_start_ms, side, price_bin) DO UPDATE SET
  price_step = excluded.price_step, intensity = excluded.intensity, level_count = excluded.level_count`

	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(sql,
				r.Market, r.Symbol, r.BucketStartMs, r.Side,
				r.PriceBin, r.PriceStep, r.Intensity, r.LevelCount,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

type whaleWallFarEvent struct {
	Side        string
	WallPrice   float64
	LatestPrice float64
	DistancePct float64
	NotionalUSD float64
	BucketStart int64
}

func pickWhaleWallFar(rows []heatmapRow, latestPrice float64) *whaleWallFarEvent {
	if latestPrice <= 0 || len(rows) == 0 {
		return nil
	}
	var best *whaleWallFarEvent
	for _, row := range rows {
		if row.PriceBin <= 0 || row.Intensity <= 0 {
			continue
		}
		distancePct := math.Abs((row.PriceBin-latestPrice)/latestPrice) * 100
		if distancePct <= whaleWallFarMinDistancePct {
			continue
		}
		if row.Intensity < whaleWallFarMinNotionalUSD {
			continue
		}
		candidate := whaleWallFarEvent{
			Side:        strings.ToLower(strings.TrimSpace(row.Side)),
			WallPrice:   row.PriceBin,
			LatestPrice: latestPrice,
			DistancePct: distancePct,
			NotionalUSD: row.Intensity,
			BucketStart: row.BucketStartMs,
		}
		if best == nil || candidate.NotionalUSD > best.NotionalUSD {
			tmp := candidate
			best = &tmp
		}
	}
	return best
}

func insertWhaleWallFarEvent(ctx context.Context, store *sqlite.Store, market, symbol string, event whaleWallFarEvent) error {
	if event.BucketStart <= 0 {
		return nil
	}
	sideText := "ask"
	if event.Side == "bid" {
		sideText = "bid"
	}
	title := fmt.Sprintf("%s %s wall %.2fM USDT, %.2f%% away",
		symbol, sideText, event.NotionalUSD/1_000_000.0, event.DistancePct)
	detailBytes, _ := json.Marshal(map[string]interface{}{
		"side":        sideText,
		"wallPrice":   event.WallPrice,
		"latestPrice": event.LatestPrice,
		"distancePct": math.Round(event.DistancePct*100) / 100,
		"valueUSDT":   math.Round(event.NotionalUSD*100) / 100,
		"signalState": "ALERT",
		"score":       90,
	})
	_, err := insertAnomalyEvents(ctx, store, []map[string]interface{}{
		{
			"market":        market,
			"symbol":        symbol,
			"event_type":    whaleWallFarEventType,
			"tf_signal":     "1m",
			"tf_level":      nil,
			"event_time_ms": event.BucketStart,
			"title":         title,
			"details":       string(detailBytes),
		},
	})
	return err
}

// ---------------------------------------------------------------------------
// Symbol parsing + tiers
// ---------------------------------------------------------------------------

func parseSymbols(raw string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, item := range strings.Split(raw, ",") {
		sym := strings.ToUpper(strings.TrimSpace(item))
		if sym == "" {
			continue
		}
		if !strings.HasSuffix(sym, "USDT") {
			sym += "USDT"
		}
		if _, ok := seen[sym]; ok {
			continue
		}
		seen[sym] = struct{}{}
		out = append(out, sym)
	}
	return binance.FilterExcludedSymbols(out)
}

func splitTiers(all []string, fastRaw string) (fast, slow []string) {
	fastSet := make(map[string]struct{})
	for _, s := range parseSymbols(fastRaw) {
		fastSet[s] = struct{}{}
	}
	for _, s := range all {
		if _, ok := fastSet[s]; ok {
			fast = append(fast, s)
		} else {
			slow = append(slow, s)
		}
	}
	return
}

// ---------------------------------------------------------------------------
// DepthFullscan runtime
// ---------------------------------------------------------------------------

type DepthScanner struct {
	bn    *binance.Client
	store *sqlite.Store
	cfg   *config.Config
}

func NewDepthScanner(bn *binance.Client, store *sqlite.Store, cfg *config.Config) *DepthScanner {
	return &DepthScanner{bn: bn, store: store, cfg: cfg}
}

func (ds *DepthScanner) Run(ctx context.Context) {
	if !ds.cfg.DepthFullscanEnabled {
		return
	}
	market := strings.ToLower(strings.TrimSpace(ds.cfg.DepthFullscanMarket))
	if market != "swap" && market != "spot" {
		log.Printf("depth_scan: unsupported market=%s", market)
		return
	}
	allSymbols := parseSymbols(ds.cfg.DepthFullscanSymbols)
	fast, slow := splitTiers(allSymbols, ds.cfg.DepthFullscanFastSymbols)
	limit := ds.cfg.DepthFullscanLimit()
	concurrency := ds.cfg.DepthFullscanConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	jitter := ds.cfg.DepthFullscanJitterSec
	if jitter < 0 {
		jitter = 0
	}

	log.Printf("depth_scan: market=%s total=%d fast=%d slow=%d limit=%d concurrency=%d",
		market, len(allSymbols), len(fast), len(slow), limit, concurrency)

	var wg sync.WaitGroup
	if len(fast) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds.loop(ctx, "fast", market, fast, ds.cfg.DepthFullscanFastIntervalSec, limit, concurrency, jitter)
		}()
	}
	if len(slow) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds.loop(ctx, "slow", market, slow, ds.cfg.DepthFullscanSlowIntervalSec, limit, concurrency, jitter)
		}()
	}
	wg.Wait()
}

func (ds *DepthScanner) loop(ctx context.Context, tag, market string, symbols []string, intervalSec, limit, concurrency, jitterSec int) {
	for {
		ds.runBatch(ctx, tag, market, symbols, limit, concurrency)
		timeout := intervalSec
		if timeout < 30 {
			timeout = 30
		}
		if jitterSec > 0 {
			timeout += rand.Intn(2*jitterSec+1) - jitterSec
			if timeout < 15 {
				timeout = 15
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(timeout) * time.Second):
		}
	}
}

func (ds *DepthScanner) runBatch(ctx context.Context, tag, market string, symbols []string, limit, concurrency int) {
	sem := make(chan struct{}, concurrency)
	type result struct {
		symbol string
		ok     bool
		costMs float64
	}
	results := make([]result, len(symbols))
	var wg sync.WaitGroup

	for i, sym := range symbols {
		wg.Add(1)
		go func(idx int, s string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			ok := ds.fetchOne(ctx, market, s, limit)
			results[idx] = result{s, ok, float64(time.Since(start).Milliseconds())}
		}(i, sym)
	}
	wg.Wait()

	okCount := 0
	totalMs := 0.0
	for _, r := range results {
		if r.ok {
			okCount++
		}
		totalMs += r.costMs
	}
	failCount := len(results) - okCount
	avgMs := 0.0
	if len(results) > 0 {
		avgMs = totalMs / float64(len(results))
	}
	log.Printf("depth_scan %s market=%s symbols=%d ok=%d fail=%d avg_ms=%.1f", tag, market, len(symbols), okCount, failCount, avgMs)
}

func (ds *DepthScanner) fetchOne(ctx context.Context, market, symbol string, limit int) bool {
	depth, err := ds.bn.GetOrderbookDepth(ctx, market, symbol, limit)
	if err != nil {
		return false
	}

	if ds.cfg.DepthHeatmapEnabled {
		targetMarket := market
		if ds.cfg.DepthHeatmapForceSpot {
			targetMarket = "spot"
		}
		heatDepth := depth
		if targetMarket != market {
			d, err := ds.bn.GetOrderbookDepth(ctx, targetMarket, symbol, limit)
			if err == nil {
				heatDepth = d
			} else {
				heatDepth = nil
			}
		}
		if heatDepth != nil {
			rows, mid := buildHeatmapRows(targetMarket, symbol, heatDepth, time.Now().UnixMilli(), ds.cfg)
			if len(rows) > 0 {
				if err := writeHeatmapRows(ctx, ds.store, rows); err != nil {
					log.Printf("depth_scan: heatmap write failed market=%s symbol=%s: %v", targetMarket, symbol, err)
				}
				if evt := pickWhaleWallFar(rows, mid); evt != nil {
					if err := insertWhaleWallFarEvent(ctx, ds.store, targetMarket, symbol, *evt); err != nil {
						log.Printf("depth_scan: whale wall event write failed market=%s symbol=%s: %v", targetMarket, symbol, err)
					}
				}
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func levelPrice(v interface{}) float64 {
	arr, ok := v.([]interface{})
	if !ok || len(arr) < 1 {
		return 0
	}
	return ifaceFloat(arr[0])
}

func ifaceFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f := 0.0
		for i := 0; i < len(t); i++ {
			if t[i] == '.' {
				frac := 0.0
				div := 1.0
				for j := i + 1; j < len(t); j++ {
					if t[j] < '0' || t[j] > '9' {
						break
					}
					div *= 10
					frac += float64(t[j]-'0') / div
				}
				return f + frac
			}
			if t[i] < '0' || t[i] > '9' {
				return 0
			}
			f = f*10 + float64(t[i]-'0')
		}
		return f
	default:
		return 0
	}
}
