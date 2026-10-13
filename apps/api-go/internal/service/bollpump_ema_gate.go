package service

import (
	"context"

	"coinmark/api-go/internal/model"
)

// BOLL 泵盘信号的 EMA 限制：触发周期和主导周期的最新一根已收盘 K 线，收盘价都要在 EMA100 和 EMA200 之上。
// 缓存里 1m/3m 有 260 根，其他周期 499 根，按 499 取即可，不会多发 REST。
const bollPumpEMAGateBars = 499

// bollPumpAboveEMA100And200 最新一根收盘价同时高于 EMA100 和 EMA200；不足 200 根时 EMA200 不可靠，不算通过。
func bollPumpAboveEMA100And200(bars []BollPumpBar) bool {
	if len(bars) < 200 {
		return false
	}
	last := len(bars) - 1
	c := bars[last].Close
	return c > bollSqueezeEMA(bars, 100)[last] && c > bollSqueezeEMA(bars, 200)[last]
}

// emaGate 触发周期和主导周期都在 EMA100、EMA200 之上才放行信号。
func (s *BollPumpScanner) emaGate(ctx context.Context, symbol, timeframe string) bool {
	market := normalizeBollPumpMarket(s.cfg.Market)
	tfs := []string{timeframe}
	if s.store != nil {
		peers, err := ListBollPumpStates(ctx, s.store, BollPumpStateFilter{Market: market, Symbol: symbol, Limit: 20})
		if err != nil {
			return false
		}
		if d := BollPumpDominantTimeframe(model.BollPumpState{Timeframe: timeframe}, peers); d != timeframe {
			tfs = append(tfs, d)
		}
	}
	for _, tf := range tfs {
		bars, err := s.source.Klines(ctx, market, symbol, tf, bollPumpEMAGateBars)
		if err != nil || !bollPumpAboveEMA100And200(bollPumpClosedBars(bars)) {
			return false
		}
	}
	return true
}

// BollPumpDominantTimeframe 主导周期：比 row 大的周期里，最近的一个活跃周期（优先分 ≥ 70 且未失效）；没有就是自己。
func BollPumpDominantTimeframe(row model.BollPumpState, peers []model.BollPumpState) string {
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
		if peer.PriorityScore < 70 || peer.Status == string(BollPumpStatusInvalidated) {
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
