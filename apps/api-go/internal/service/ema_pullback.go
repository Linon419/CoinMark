package service

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"coinmark/api-go/internal/repo/sqlite"
)

// EMA 回踩：上涨趋势中价格回落到 EMA100 或 EMA200 附近并收在线上方。只用已收盘 K 线。
// 2026-09 全市场试算：每根 K 线平均命中 15m 约 50、30m 约 39、1h 约 34、4h 约 20 个币。
const (
	EMAPullbackEventType     = "ema_pullback"
	emaPullbackTouchTol      = 0.003 // 低点 ≤ 线 × 1.003 算碰到
	emaPullbackAboveLookback = 10    // 之前 10 根收盘都在线上方：从上方回落，而不是在线附近来回
	emaPullbackTrendLookback = 10    // EMA200 比 10 根前更高 = 向上
)

type EMAPullbackHit struct {
	Timeframe     string  `json:"timeframe"`
	CandleStartMs int64   `json:"candle_start_ms"`
	Line          string  `json:"line"` // 回踩到的线：EMA100 或 EMA200（两条都碰到时取更深的 EMA200）
	Close         float64 `json:"close"`
	Low           float64 `json:"low"`
	EMA100        float64 `json:"ema100"`
	EMA200        float64 `json:"ema200"`
	DistancePct   float64 `json:"distance_pct"` // 收盘价离这条线的距离
}

type EMAPullbackRow struct {
	Symbol         string           `json:"symbol"`
	QuoteVolume24h float64          `json:"quote_volume_24h"`
	Hits           []EMAPullbackHit `json:"hits"`
}

// EvaluateEMAPullback 判断最后一根已收盘 K 线：上涨趋势（EMA100 > EMA200、EMA200 向上）中，
// 低点碰到 EMA200 或 EMA100（≤ 线 × 1.003），收盘仍在线上方，且之前 10 根收盘都在线上方。
func EvaluateEMAPullback(bars []BollPumpBar) (EMAPullbackHit, bool) {
	n := len(bars)
	if n < bollSqueezeMinBars {
		return EMAPullbackHit{}, false
	}
	ema100 := bollSqueezeEMA(bars, 100)
	ema200 := bollSqueezeEMA(bars, 200)
	last := n - 1
	if !(ema100[last] > ema200[last] && ema200[last] > ema200[last-emaPullbackTrendLookback]) {
		return EMAPullbackHit{}, false
	}
	cur := bars[last]
	for _, line := range []struct {
		name string
		ema  []float64
	}{{"EMA200", ema200}, {"EMA100", ema100}} {
		l := line.ema[last]
		if cur.Low > l*(1+emaPullbackTouchTol) || cur.Close < l {
			continue
		}
		above := true
		for i := last - emaPullbackAboveLookback; i < last; i++ {
			if bars[i].Close <= line.ema[i] {
				above = false
				break
			}
		}
		if !above {
			continue
		}
		return EMAPullbackHit{
			CandleStartMs: cur.OpenTimeMs,
			Line:          line.name,
			Close:         cur.Close,
			Low:           cur.Low,
			EMA100:        ema100[last],
			EMA200:        ema200[last],
			DistancePct:   cur.Close/l - 1,
		}, true
	}
	return EMAPullbackHit{}, false
}

func formatEMAPullbackNotify(symbol string, hit EMAPullbackHit) string {
	return fmt.Sprintf("EMA 回踩 · %s %s\n回踩 %s，₮%s（离线 %+.2f%%，最低 %s）\nEMA100 %s > EMA200 %s",
		strings.TrimSuffix(symbol, "USDT"), hit.Timeframe, hit.Line,
		bollSqueezeNum(hit.Close), hit.DistancePct*100, bollSqueezeNum(hit.Low),
		bollSqueezeNum(hit.EMA100), bollSqueezeNum(hit.EMA200))
}

func emaPullbackSignal(symbol, tf string, bars []BollPumpBar) (pullbackSignal, bool) {
	hit, ok := EvaluateEMAPullback(bars)
	if !ok {
		return pullbackSignal{}, false
	}
	hit.Timeframe = tf
	return pullbackSignal{
		CandleStartMs: hit.CandleStartMs,
		Title:         formatEMAPullbackNotify(symbol, hit),
		Details: map[string]interface{}{
			"line": hit.Line, "close": hit.Close, "low": hit.Low, "ema100": hit.EMA100, "ema200": hit.EMA200, "distancePct": hit.DistancePct,
		},
	}, true
}

// EMAPullbackScanner 每分钟用 BOLL 扫描的 K 线缓存扫一遍全部合约，结果只放内存；收藏币刚回踩时写通知事件。
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
	ticker := time.NewTicker(bollSqueezeScanInterval)
	defer ticker.Stop()
	for {
		s.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
		}
	}
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
		for _, tf := range PullbackTimeframes {
			bars, err := s.source.Klines(ctx, s.market, symbol, tf, 499)
			if err != nil {
				continue // 缓存预热中或拉取失败，下一轮再试
			}
			hit, ok := EvaluateEMAPullback(bollPumpClosedBarsBefore(bars, nowMs))
			if !ok {
				continue
			}
			hit.Timeframe = tf
			row.Hits = append(row.Hits, hit)
		}
		if len(row.Hits) == 0 {
			continue
		}
		row.Symbol = symbol
		if hourly, err := s.source.Klines(ctx, s.market, symbol, "1h", 24); err == nil {
			for _, b := range hourly {
				row.QuoteVolume24h += b.QuoteVolume
			}
		}
		rows = append(rows, row)
	}
	// 命中周期多的在前，同样多的按成交额
	sort.Slice(rows, func(i, j int) bool {
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
