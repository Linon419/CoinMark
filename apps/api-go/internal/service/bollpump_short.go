package service

import (
	"context"
	"fmt"
	"strings"

	"coinmark/api-go/internal/model"
)

// BOLL 做空：碰上轨回落。不走做多的状态机，每根 K 线收盘时看一次：
//   - 压力 K：最近 3 根里，最高价碰到上轨、收盘收回上轨下方；
//   - 触发：之后没有收盘站上上轨，也没有 K 线跌破过压力 K 的最低价，最新一根第一次跌破它、收在上轨下方；
//   - EMA：触发周期和上一级周期最新一根都收在 EMA100、EMA200 之下（做空没有状态和分数，主导周期用上一级周期）。
const (
	BollShortEventType      = "boll_short"
	bollShortRejectLookback = 3
)

// bollShortTrigger 最新一根是否刚确认“碰上轨回落”，返回压力 K 的下标。
func bollShortTrigger(bars []BollPumpBar, ind []BollPumpIndicator) (int, bool) {
	last := len(bars) - 1
	if last < 1 || len(ind) != len(bars) || !ind[last].ValidBoll || bars[last].Close >= ind[last].Upper {
		return -1, false
	}
	for r := last - 1; r >= 0 && r >= last-bollShortRejectLookback; r-- {
		if !ind[r].ValidBoll {
			return -1, false
		}
		if bars[r].Close >= ind[r].Upper {
			return -1, false // 收盘站上了上轨，不是回落
		}
		if bars[r].High < ind[r].Upper {
			continue
		}
		for j := r + 1; j < last; j++ {
			if bars[j].Low < bars[r].Low {
				return -1, false // 已经跌破过，信号出过了
			}
		}
		if bars[last].Low < bars[r].Low {
			return r, true
		}
		return -1, false
	}
	return -1, false
}

// bollShortHigherTimeframe 上一级周期；1h 没有上一级。
func bollShortHigherTimeframe(tf string) string {
	order := []string{"1m", "3m", "5m", "15m", "30m", "1h"}
	for i := 0; i+1 < len(order); i++ {
		if order[i] == tf {
			return order[i+1]
		}
	}
	return ""
}

// shortEMAGate 触发周期和上一级周期都收在 EMA100、EMA200 之下。
func (s *BollPumpScanner) shortEMAGate(ctx context.Context, symbol, timeframe string) bool {
	market := normalizeBollPumpMarket(s.cfg.Market)
	for _, tf := range []string{timeframe, bollShortHigherTimeframe(timeframe)} {
		if tf == "" {
			continue
		}
		bars, err := s.source.Klines(ctx, market, symbol, tf, bollPumpEMAGateBars)
		if err != nil || !bollPumpBelowEMA100And200(bollPumpClosedBars(bars)) {
			return false
		}
	}
	return true
}

func bollShortSignal(market, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, r int, quoteVolume24h float64) model.BollPumpSignal {
	last := len(bars) - 1
	b := bars[last]
	volumeRatio := 0.0
	if avg := bollPumpAverageVolumeBefore(bars, r, 20); avg > 0 {
		volumeRatio = bars[r].Volume / avg
	}
	rejectMs := bars[r].OpenTimeMs
	return model.BollPumpSignal{
		Market:                normalizeBollPumpMarket(market),
		Symbol:                strings.ToUpper(strings.TrimSpace(symbol)),
		Timeframe:             timeframe,
		SignalLevel:           string(BollPumpLevelShort),
		Price:                 b.Close,
		VolumeRatio:           volumeRatio,
		BollBandwidth:         ind[last].Bandwidth,
		SignalTimeMs:          b.CloseTimeMs,
		CandleStartMs:         b.OpenTimeMs,
		PullbackCandleStartMs: &rejectMs,
		QuoteVolume24h:        quoteVolume24h,
		Reason: fmt.Sprintf("touched upper %s then broke low %s",
			bollSqueezeNum(ind[r].Upper), bollSqueezeNum(bars[r].Low)),
		Details: model.JSONB(fmt.Sprintf(`{"rejectHigh":%g,"rejectLow":%g,"upper":%g}`, bars[r].High, bars[r].Low, ind[r].Upper)),
	}
}

// formatBollShortNotify Telegram 正文。
func formatBollShortNotify(sig model.BollPumpSignal, upper, rejectLow float64) string {
	return fmt.Sprintf("BOLL 做空 · %s %s\n₮%s  碰上轨 %s 后跌破 %s",
		strings.TrimSuffix(sig.Symbol, "USDT"), sig.Timeframe,
		bollSqueezeNum(sig.Price), bollSqueezeNum(upper), bollSqueezeNum(rejectLow))
}

// scanShort 最新一根收盘 K 线刚确认碰上轨回落、EMA 也满足时，写一条 SHORT 信号；打开做空推送时写通知。
func (s *BollPumpScanner) scanShort(ctx context.Context, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64) bool {
	r, ok := bollShortTrigger(bars, ind)
	if !ok || !s.shortEMAGate(ctx, symbol, timeframe) {
		return false
	}
	sig := bollShortSignal(s.cfg.Market, symbol, timeframe, bars, ind, r, quoteVolume24h)
	if s.store == nil {
		return true
	}
	if exists, err := BollPumpSignalExists(ctx, s.store, sig); err != nil || exists {
		return false
	}
	if _, err := SaveBollPumpSignal(ctx, s.store, sig, false); err != nil {
		return false
	}
	if s.cfg.ShortTelegramEnabled {
		_, _ = insertAnomalyEvents(ctx, s.store, []map[string]interface{}{
			yidongEvent(sig.Market, sig.Symbol, BollShortEventType, sig.Timeframe, "", sig.SignalTimeMs,
				formatBollShortNotify(sig, ind[r].Upper, bars[r].Low), map[string]interface{}{"price": sig.Price, "upper": ind[r].Upper, "rejectLow": bars[r].Low}),
		})
	}
	return true
}
