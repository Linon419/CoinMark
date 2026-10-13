package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"coinmark/api-go/internal/repo/sqlite"
)

// EMA 回踩：上涨趋势中价格回落到 EMA100 或 EMA200 附近并收在线上方（多）；
// 反过来，下跌趋势中价格反抽到 EMA100 或 EMA200 附近并收在线下方（空）。只用已收盘 K 线。
// 2026-09 全市场试算（多）：每根 K 线平均命中 15m 约 50、30m 约 39、1h 约 34、4h 约 20 个币。每 15 分钟扫一次。
//
// 分数：基础 60；同一个币同方向每多一个周期命中 +10（最多 +20）；上一级周期同方向趋势 +10；
// 触线那根的影线（多看下影、空看上影）≥ 整根一半 +10。没回测过。
const (
	EMAPullbackEventType     = "ema_pullback"
	emaPullbackTouchTol      = 0.003 // 低点 ≤ 线 × 1.003 算碰到
	emaPullbackAboveLookback = 10    // 之前 10 根收盘都在线上方：从上方回落，而不是在线附近来回
	emaPullbackTrendLookback = 10    // EMA200 比 10 根前更高 = 向上
)

type EMAPullbackHit struct {
	Side          string   `json:"side"` // long：回踩；short：反抽
	Timeframe     string   `json:"timeframe"`
	CandleStartMs int64    `json:"candle_start_ms"`
	Line          string   `json:"line"` // 回踩到的线：EMA100 或 EMA200（两条都碰到时取更深的 EMA200）
	Close         float64  `json:"close"`
	Low           float64  `json:"low"`
	High          float64  `json:"high"`
	EMA100        float64  `json:"ema100"`
	EMA200        float64  `json:"ema200"`
	DistancePct   float64  `json:"distance_pct"` // 收盘价离这条线的距离
	WickRatio     float64  `json:"wick_ratio"`   // 触线那根：多为下影线、空为上影线占整根的比例
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons"`
}

type EMAPullbackRow struct {
	Symbol         string           `json:"symbol"`
	QuoteVolume24h float64          `json:"quote_volume_24h"`
	Hits           []EMAPullbackHit `json:"hits"`
	Score          float64          `json:"score"` // 各周期命中里的最高分
}

// EvaluateEMAPullback 多头回踩，判断最后一根已收盘 K 线：上涨趋势（EMA100 > EMA200、EMA200 向上）中，
// 低点碰到 EMA200 或 EMA100（≤ 线 × 1.003），收盘仍在线上方，且之前 10 根收盘都在线上方。
func EvaluateEMAPullback(bars []BollPumpBar) (EMAPullbackHit, bool) {
	return evaluateEMATouch(bars, false)
}

// EvaluateEMAPullbackShort 空头反抽：下跌趋势（EMA100 < EMA200、EMA200 向下）中，
// 高点碰到 EMA200 或 EMA100（≥ 线 × 0.997），收盘仍在线下方，且之前 10 根收盘都在线下方。
func EvaluateEMAPullbackShort(bars []BollPumpBar) (EMAPullbackHit, bool) {
	return evaluateEMATouch(bars, true)
}

// emaPullbackTrend 1 = 上涨趋势（EMA100 > EMA200、EMA200 比 10 根前高），-1 = 下跌趋势，0 = 都不是或 K 线不够。
func emaPullbackTrend(bars []BollPumpBar) int {
	n := len(bars)
	if n < bollSqueezeMinBars {
		return 0
	}
	ema100, ema200 := bollSqueezeEMA(bars, 100), bollSqueezeEMA(bars, 200)
	return emaPullbackTrendAt(ema100, ema200, n-1)
}

func emaPullbackTrendAt(ema100, ema200 []float64, last int) int {
	switch {
	case ema100[last] > ema200[last] && ema200[last] > ema200[last-emaPullbackTrendLookback]:
		return 1
	case ema100[last] < ema200[last] && ema200[last] < ema200[last-emaPullbackTrendLookback]:
		return -1
	}
	return 0
}

func evaluateEMATouch(bars []BollPumpBar, short bool) (EMAPullbackHit, bool) {
	n := len(bars)
	if n < bollSqueezeMinBars {
		return EMAPullbackHit{}, false
	}
	ema100 := bollSqueezeEMA(bars, 100)
	ema200 := bollSqueezeEMA(bars, 200)
	last := n - 1
	want, side := 1, "long"
	if short {
		want, side = -1, "short"
	}
	if emaPullbackTrendAt(ema100, ema200, last) != want {
		return EMAPullbackHit{}, false
	}
	cur := bars[last]
	for _, line := range []struct {
		name string
		ema  []float64
	}{{"EMA200", ema200}, {"EMA100", ema100}} {
		l := line.ema[last]
		// 多：低点碰线、收在线上；空：高点碰线、收在线下
		if !short && (cur.Low > l*(1+emaPullbackTouchTol) || cur.Close < l) {
			continue
		}
		if short && (cur.High < l*(1-emaPullbackTouchTol) || cur.Close > l) {
			continue
		}
		onSide := true
		for i := last - emaPullbackAboveLookback; i < last; i++ {
			if (!short && bars[i].Close <= line.ema[i]) || (short && bars[i].Close >= line.ema[i]) {
				onSide = false
				break
			}
		}
		if !onSide {
			continue
		}
		wick := 0.0
		if rng := cur.High - cur.Low; rng > 0 {
			if short {
				wick = (cur.High - math.Max(cur.Open, cur.Close)) / rng
			} else {
				wick = (math.Min(cur.Open, cur.Close) - cur.Low) / rng
			}
		}
		return EMAPullbackHit{
			Side:          side,
			CandleStartMs: cur.OpenTimeMs,
			Line:          line.name,
			Close:         cur.Close,
			Low:           cur.Low,
			High:          cur.High,
			EMA100:        ema100[last],
			EMA200:        ema200[last],
			DistancePct:   cur.Close/l - 1,
			WickRatio:     wick,
		}, true
	}
	return EMAPullbackHit{}, false
}

// emaPullbackHigherTimeframe 上一级周期；4h 没有上一级。
func emaPullbackHigherTimeframe(tf string) string {
	switch tf {
	case "15m":
		return "30m"
	case "30m":
		return "1h"
	case "1h":
		return "4h"
	}
	return ""
}

// scoreEMAPullbackHits 同一个币各周期的命中打分；trends 为各周期的趋势方向（1 / -1 / 0）。
func scoreEMAPullbackHits(hits []EMAPullbackHit, trends map[string]int) {
	count := map[string]int{}
	for _, h := range hits {
		count[h.Side]++
	}
	for i := range hits {
		h := &hits[i]
		dir := 1
		if h.Side == "short" {
			dir = -1
		}
		score := 60.0
		var reasons []string
		if extra := count[h.Side] - 1; extra > 0 {
			score += float64(min(extra, 2)) * 10
			reasons = append(reasons, fmt.Sprintf("%d 个周期共振", extra+1))
		}
		if up := emaPullbackHigherTimeframe(h.Timeframe); up != "" && trends[up] == dir {
			score += 10
			reasons = append(reasons, up+" 同向")
		}
		if h.WickRatio >= 0.5 {
			score += 10
			reasons = append(reasons, "影线拒绝")
		}
		h.Score, h.Reasons = score, reasons
	}
}

func formatEMAPullbackNotify(symbol string, hit EMAPullbackHit) string {
	if hit.Side == "short" {
		return fmt.Sprintf("EMA 反抽 · %s %s\n反抽 %s，₮%s（离线 %+.2f%%，最高 %s）\nEMA100 %s < EMA200 %s",
			strings.TrimSuffix(symbol, "USDT"), hit.Timeframe, hit.Line,
			bollSqueezeNum(hit.Close), hit.DistancePct*100, bollSqueezeNum(hit.High),
			bollSqueezeNum(hit.EMA100), bollSqueezeNum(hit.EMA200))
	}
	return fmt.Sprintf("EMA 回踩 · %s %s\n回踩 %s，₮%s（离线 %+.2f%%，最低 %s）\nEMA100 %s > EMA200 %s",
		strings.TrimSuffix(symbol, "USDT"), hit.Timeframe, hit.Line,
		bollSqueezeNum(hit.Close), hit.DistancePct*100, bollSqueezeNum(hit.Low),
		bollSqueezeNum(hit.EMA100), bollSqueezeNum(hit.EMA200))
}

// emaPullbackSignal 收藏币通知：多头回踩、空头反抽都推。
func emaPullbackSignal(symbol, tf string, bars []BollPumpBar) (pullbackSignal, bool) {
	hit, ok := EvaluateEMAPullback(bars)
	if !ok {
		hit, ok = EvaluateEMAPullbackShort(bars)
	}
	if !ok {
		return pullbackSignal{}, false
	}
	hit.Timeframe = tf
	return pullbackSignal{
		CandleStartMs: hit.CandleStartMs,
		Title:         formatEMAPullbackNotify(symbol, hit),
		Details: map[string]interface{}{
			"side": hit.Side, "line": hit.Line, "close": hit.Close, "low": hit.Low, "high": hit.High, "ema100": hit.EMA100, "ema200": hit.EMA200, "distancePct": hit.DistancePct,
		},
	}, true
}

// EMAPullbackScanner 每 15 分钟用 BOLL 扫描的 K 线缓存扫一遍全部合约，结果只放内存；收藏币刚回踩时写通知事件。
type EMAPullbackScanner struct {
	source BollPumpSource
	market string
	store  *sqlite.Store

	mu        sync.RWMutex
	rows      []EMAPullbackRow
	updatedMs int64
}

func NewEMAPullbackScanner(source BollPumpSource, market string, store *sqlite.Store) *EMAPullbackScanner {
	return &EMAPullbackScanner{source: source, market: market, store: store}
}

func (s *EMAPullbackScanner) Snapshot() ([]EMAPullbackRow, int64) {
	if s == nil {
		return nil, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rows, s.updatedMs
}

func (s *EMAPullbackScanner) Run(ctx context.Context, stopCh <-chan struct{}) {
	runPullbackScans(ctx, stopCh, s.scan)
}

func (s *EMAPullbackScanner) scan(ctx context.Context) {
	symbols, err := s.source.Symbols(ctx, s.market, 0)
	if err != nil {
		log.Printf("ema_pullback: symbols error: %v", err)
		return
	}
	nowMs := time.Now().UnixMilli()
	rows := make([]EMAPullbackRow, 0)
	for _, symbol := range symbols {
		var row EMAPullbackRow
		trends := map[string]int{}
		for _, tf := range PullbackTimeframes {
			bars, err := s.source.Klines(ctx, s.market, symbol, tf, 499)
			if err != nil {
				continue // 缓存预热中或拉取失败，下一轮再试
			}
			bars = bollPumpClosedBarsBefore(bars, nowMs)
			trends[tf] = emaPullbackTrend(bars)
			hit, ok := EvaluateEMAPullback(bars)
			if !ok {
				hit, ok = EvaluateEMAPullbackShort(bars)
			}
			if !ok {
				continue
			}
			hit.Timeframe = tf
			row.Hits = append(row.Hits, hit)
		}
		if len(row.Hits) == 0 {
			continue
		}
		scoreEMAPullbackHits(row.Hits, trends)
		for _, h := range row.Hits {
			row.Score = math.Max(row.Score, h.Score)
		}
		row.Symbol = symbol
		if hourly, err := s.source.Klines(ctx, s.market, symbol, "1h", 24); err == nil {
			for _, b := range hourly {
				row.QuoteVolume24h += b.QuoteVolume
			}
		}
		rows = append(rows, row)
	}
	// 分数高的在前，同分时命中周期多的在前，再按成交额
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		if len(rows[i].Hits) != len(rows[j].Hits) {
			return len(rows[i].Hits) > len(rows[j].Hits)
		}
		return rows[i].QuoteVolume24h > rows[j].QuoteVolume24h
	})
	s.mu.Lock()
	s.rows, s.updatedMs = rows, nowMs
	s.mu.Unlock()
	if n, err := notifyPullbacks(ctx, s.store, s.source, s.market, EMAPullbackNotifyName, EMAPullbackEventType, nowMs, emaPullbackSignal); err != nil {
		log.Printf("ema_pullback: notify error: %v", err)
	} else if n > 0 {
		log.Printf("ema_pullback: notify events=%d", n)
	}
}
