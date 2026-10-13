package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"coinmark/api-go/internal/model"
)

// BOLL 做空：碰上轨回落。不走做多的状态机，每根 K 线收盘时看一次：
//   - 压力 K：最近 3 根里，最高价碰到上轨、收盘收回上轨下方；
//   - 触发：之后没有收盘站上上轨，也没有 K 线跌破过压力 K 的最低价，最新一根第一次跌破它、收在上轨下方；
//   - EMA：触发周期和上一级周期最新一根都收在 EMA100、EMA200 之下（做空没有状态机，主导周期用上一级周期）。
//
// 分数：基础 60，按下面几项加分（参照做多的加分项反过来，没回测过；持仓增长每条要请求一次币安，做空信号多，没加）。
const (
	BollShortEventType      = "boll_short"
	bollShortRejectLookback = 3

	bollShortBaseScore     = 60.0
	bollShortVolumeBonus   = 10.0 // 压力 K 成交量 ≥ 前 20 根平均的 2 倍
	bollShortWickBonus     = 10.0 // 压力 K 上影线 ≥ 整根的一半
	bollShortSegmentsBonus = 15.0 // 同一段下跌里第 2 次及以后碰上轨回落
	bollShortTrendBonus    = 10.0 // 15m 中轨下降（做多“15m 趋势”反过来）
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

// bollShortSegments 当前这次是同一段下跌里第几次碰上轨回落：往前 lookback 根内数之前的触发，遇到收盘站上上轨就停。
func bollShortSegments(bars []BollPumpBar, ind []BollPumpIndicator, lookback int) int {
	last := len(bars) - 1
	segments := 1
	for k := last - 1; k >= 0 && k >= last-lookback; k-- {
		if !ind[k].ValidBoll || bars[k].Close >= ind[k].Upper {
			break
		}
		if _, ok := bollShortTrigger(bars[:k+1], ind[:k+1]); ok {
			segments++
		}
	}
	return segments
}

// bollShortCandleScore 只看当前周期 K 线的分数：基础分 + 压力 K 放量 + 长上影 + 连续第几段。
func bollShortCandleScore(bars []BollPumpBar, ind []BollPumpIndicator, r, segments int) (float64, []string) {
	score := bollShortBaseScore
	var reasons []string
	if avg := bollPumpAverageVolumeBefore(bars, r, 20); avg > 0 && bars[r].Volume >= 2*avg {
		score += bollShortVolumeBonus
		reasons = append(reasons, fmt.Sprintf("reject volume %.1fx", bars[r].Volume/avg))
	}
	b := bars[r]
	if rng := b.High - b.Low; rng > 0 && (b.High-math.Max(b.Open, b.Close))/rng >= 0.5 {
		score += bollShortWickBonus
		reasons = append(reasons, "long upper wick")
	}
	if segments >= 2 {
		score += bollShortSegmentsBonus
		reasons = append(reasons, fmt.Sprintf("segment %d", segments))
	}
	return score, reasons
}

// bollPumpInvertBars 价格取倒数（高低互换），用来把“中轨上升”“突破阻力”的判断反过来用于做空。
func bollPumpInvertBars(bars []BollPumpBar) []BollPumpBar {
	out := make([]BollPumpBar, 0, len(bars))
	for _, b := range bars {
		if b.Open <= 0 || b.High <= 0 || b.Low <= 0 || b.Close <= 0 {
			continue
		}
		b.Open, b.High, b.Low, b.Close = 1/b.Open, 1/b.Low, 1/b.High, 1/b.Close
		out = append(out, b)
	}
	return out
}

// shortTrendDown 15m 中轨下降：做多的 15m 趋势判断用在倒数价格上。
func (s *BollPumpScanner) shortTrendDown(ctx context.Context, symbol, timeframe string, bars []BollPumpBar) bool {
	tf := NormalizeBollPumpConfig(s.cfg).MinimumTrendTimeframe
	trendBars := bars
	if timeframe != tf {
		loaded, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, tf, s.cfg.MinimumTrendCheckCandles+s.cfg.BollPeriod+10)
		if err != nil {
			return false
		}
		trendBars = bollPumpClosedBars(loaded)
	}
	return bollPumpMinimumTrendGate(bollPumpInvertBars(trendBars), s.cfg).Pass
}

// shortFourHourBreakdown 跌破 4h 支撑：做多的 4h 突破阻力用在倒数价格上。
func (s *BollPumpScanner) shortFourHourBreakdown(ctx context.Context, symbol string) bollPumpResistanceBreakoutResult {
	if s.cfg.Resistance4HBreakoutBonus <= 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	bars, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, "4h", bollPumpEMAGateBars)
	if err != nil {
		return bollPumpResistanceBreakoutResult{}
	}
	return bollPumpFourHourResistanceBreakout(bollPumpInvertBars(bollPumpClosedBarsBefore(bars, time.Now().UnixMilli())), s.cfg)
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
	segment := ""
	if sig.BounceCount >= 2 {
		segment = fmt.Sprintf("（连续第 %d 段）", sig.BounceCount)
	}
	return fmt.Sprintf("BOLL 做空 · %s %s · %.0f 分\n₮%s  碰上轨 %s 后跌破 %s%s",
		strings.TrimSuffix(sig.Symbol, "USDT"), sig.Timeframe, sig.PriorityScore,
		bollSqueezeNum(sig.Price), bollSqueezeNum(upper), bollSqueezeNum(rejectLow), segment)
}

// scoreShort 打分：当前周期 K 线的几项，加 15m 趋势向下、跌破 4h 支撑。
func (s *BollPumpScanner) scoreShort(ctx context.Context, sig *model.BollPumpSignal, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, r int) {
	segments := bollShortSegments(bars, ind, s.cfg.StageExpiryCandles)
	score, reasons := bollShortCandleScore(bars, ind, r, segments)
	if s.shortTrendDown(ctx, symbol, timeframe, bars) {
		score += bollShortTrendBonus
		reasons = append(reasons, "15m middle falling")
	}
	if b := s.shortFourHourBreakdown(ctx, symbol); b.Triggered {
		score += b.Bonus
		reasons = append(reasons, "4h support breakdown")
	}
	sig.BounceCount = segments
	sig.Score, sig.PriorityScore = score, score
	if len(reasons) > 0 {
		sig.Reason += ", " + strings.Join(reasons, ", ")
	}
}

// scanShort 最新一根收盘 K 线刚确认碰上轨回落、EMA 也满足时，写一条 SHORT 信号；打开做空推送时写通知。
func (s *BollPumpScanner) scanShort(ctx context.Context, symbol, timeframe string, bars []BollPumpBar, ind []BollPumpIndicator, quoteVolume24h float64) bool {
	r, ok := bollShortTrigger(bars, ind)
	if !ok || !s.shortEMAGate(ctx, symbol, timeframe) {
		return false
	}
	sig := bollShortSignal(s.cfg.Market, symbol, timeframe, bars, ind, r, quoteVolume24h)
	s.scoreShort(ctx, &sig, symbol, timeframe, bars, ind, r)
	if s.store == nil {
		return true
	}
	if exists, err := BollPumpSignalExists(ctx, s.store, sig); err != nil || exists {
		return false
	}
	if _, err := SaveBollPumpSignal(ctx, s.store, sig, false); err != nil {
		return false
	}
	if s.cfg.ShortTelegramEnabled && sig.PriorityScore >= s.cfg.ShortTelegramThreshold {
		_, _ = insertAnomalyEvents(ctx, s.store, []map[string]interface{}{
			yidongEvent(sig.Market, sig.Symbol, BollShortEventType, sig.Timeframe, "", sig.SignalTimeMs,
				formatBollShortNotify(sig, ind[r].Upper, bars[r].Low), map[string]interface{}{"price": sig.Price, "upper": ind[r].Upper, "rejectLow": bars[r].Low}),
		})
	}
	return true
}
