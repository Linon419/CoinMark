package service

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
)

// 布林回踩：上涨趋势中 BOLL 缩口、价格回到下轨附近。只用已收盘 K 线。
var BollSqueezeTimeframes = []string{"15m", "30m", "1h", "4h"}

const (
	bollSqueezeMinBars        = 400 // EMA200 预热：400 根后初值影响 <2%，需要 BOLL_PUMP_WS_BOOTSTRAP_LIMIT ≥ 400
	bollSqueezeTrendLookback  = 10  // EMA200 比 10 根前更高 = 向上
	bollSqueezeShrinkLookback = 3   // 带宽比 3 根前更窄 = 正在收口
	bollSqueezeMaxBWRatio     = 0.8 // 带宽 ≤ 近 20 根最大带宽的 80%
	bollSqueezePercentBMin    = -0.2
	bollSqueezePercentBMax    = 0.35
	bollSqueezeScanInterval   = time.Minute
)

type BollSqueezeHit struct {
	Timeframe     string  `json:"timeframe"`
	CandleStartMs int64   `json:"candle_start_ms"`
	Close         float64 `json:"close"`
	Lower         float64 `json:"lower"`
	Upper         float64 `json:"upper"`
	EMA100        float64 `json:"ema100"`
	EMA200        float64 `json:"ema200"`
	PercentB      float64 `json:"percent_b"`
	Bandwidth     float64 `json:"bandwidth"`
	BandwidthMax  float64 `json:"bandwidth_max20"`
}

type BollSqueezeRow struct {
	Symbol         string           `json:"symbol"`
	QuoteVolume24h float64          `json:"quote_volume_24h"`
	Hits           []BollSqueezeHit `json:"hits"`
}

// EvaluateBollSqueeze 判断最后一根已收盘 K 线是否满足：
// 上涨趋势（EMA100 > EMA200、EMA200 向上、收盘 > EMA200）+ 缩口 + 收盘接近下轨（%B 在 -0.2~0.35）。
func EvaluateBollSqueeze(bars []BollPumpBar) (BollSqueezeHit, bool) {
	n := len(bars)
	if n < bollSqueezeMinBars {
		return BollSqueezeHit{}, false
	}
	ind := ComputeBollPumpIndicators(bars, 20, 2, 0)
	ema100 := bollSqueezeEMA(bars, 100)
	ema200 := bollSqueezeEMA(bars, 200)
	last := n - 1
	cur := ind[last]
	if !cur.ValidBoll || cur.Upper <= cur.Lower {
		return BollSqueezeHit{}, false
	}
	closePx := bars[last].Close
	if !(ema100[last] > ema200[last] && ema200[last] > ema200[last-bollSqueezeTrendLookback] && closePx > ema200[last]) {
		return BollSqueezeHit{}, false
	}
	bwMax := 0.0
	for i := n - 20; i < n; i++ {
		if ind[i].Bandwidth > bwMax {
			bwMax = ind[i].Bandwidth
		}
	}
	if !(cur.Bandwidth < ind[last-bollSqueezeShrinkLookback].Bandwidth && cur.Bandwidth <= bollSqueezeMaxBWRatio*bwMax) {
		return BollSqueezeHit{}, false
	}
	pb := (closePx - cur.Lower) / (cur.Upper - cur.Lower)
	if pb < bollSqueezePercentBMin || pb > bollSqueezePercentBMax {
		return BollSqueezeHit{}, false
	}
	return BollSqueezeHit{
		CandleStartMs: bars[last].OpenTimeMs,
		Close:         closePx,
		Lower:         cur.Lower,
		Upper:         cur.Upper,
		EMA100:        ema100[last],
		EMA200:        ema200[last],
		PercentB:      pb,
		Bandwidth:     cur.Bandwidth,
		BandwidthMax:  bwMax,
	}, true
}

func bollSqueezeEMA(bars []BollPumpBar, period int) []float64 {
	out := make([]float64, len(bars))
	alpha := 2.0 / float64(period+1)
	for i, b := range bars {
		if i == 0 {
			out[i] = b.Close
			continue
		}
		out[i] = b.Close*alpha + out[i-1]*(1-alpha)
	}
	return out
}

// BollSqueezeScanner 每分钟用 BOLL 扫描的 K 线缓存扫一遍全部合约，结果只放内存。
type BollSqueezeScanner struct {
	source BollPumpSource
	market string

	mu        sync.RWMutex
	rows      []BollSqueezeRow
	updatedMs int64
}

func NewBollSqueezeScanner(source BollPumpSource, market string) *BollSqueezeScanner {
	return &BollSqueezeScanner{source: source, market: market}
}

func (s *BollSqueezeScanner) Snapshot() ([]BollSqueezeRow, int64) {
	if s == nil {
		return nil, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rows, s.updatedMs
}

func (s *BollSqueezeScanner) Run(ctx context.Context, stopCh <-chan struct{}) {
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

func (s *BollSqueezeScanner) scan(ctx context.Context) {
	symbols, err := s.source.Symbols(ctx, s.market, 0)
	if err != nil {
		log.Printf("boll_squeeze: symbols error: %v", err)
		return
	}
	nowMs := time.Now().UnixMilli()
	rows := make([]BollSqueezeRow, 0)
	for _, symbol := range symbols {
		var row BollSqueezeRow
		for _, tf := range BollSqueezeTimeframes {
			bars, err := s.source.Klines(ctx, s.market, symbol, tf, 499)
			if err != nil {
				continue // 缓存预热中或拉取失败，下一轮再试
			}
			hit, ok := EvaluateBollSqueeze(bollPumpClosedBarsBefore(bars, nowMs))
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
}
