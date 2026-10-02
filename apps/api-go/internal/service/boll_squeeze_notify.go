package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const BollSqueezeEventType = "boll_squeeze"

func bollSqueezeNum(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// formatBollSqueezeNotify Telegram 消息正文（发送时间由 Telegram 按本地时区显示，正文不再写时间）。
func formatBollSqueezeNotify(symbol string, hit BollSqueezeHit) string {
	return fmt.Sprintf("布林回踩 · %s %s\n₮%s  %%B %.2f（下轨 %s）\nEMA100 %s > EMA200 %s · 带宽 %.1f%%（近 20 根最大 %.1f%%）",
		strings.TrimSuffix(symbol, "USDT"), hit.Timeframe,
		bollSqueezeNum(hit.Close), hit.PercentB, bollSqueezeNum(hit.Lower),
		bollSqueezeNum(hit.EMA100), bollSqueezeNum(hit.EMA200),
		hit.Bandwidth*100, hit.BandwidthMax*100)
}

func bollSqueezeSignal(symbol, tf string, bars []BollPumpBar) (pullbackSignal, bool) {
	hit, ok := EvaluateBollSqueeze(bars)
	if !ok {
		return pullbackSignal{}, false
	}
	hit.Timeframe = tf
	return pullbackSignal{
		CandleStartMs: hit.CandleStartMs,
		Title:         formatBollSqueezeNotify(symbol, hit),
		Details: map[string]interface{}{
			"close": hit.Close, "lower": hit.Lower, "percentB": hit.PercentB, "ema100": hit.EMA100, "ema200": hit.EMA200, "bandwidth": hit.Bandwidth,
		},
	}, true
}

func (s *BollSqueezeScanner) notify(ctx context.Context, nowMs int64) (int, error) {
	return notifyPullbacks(ctx, s.store, s.source, s.market, BollSqueezeNotifyName, BollSqueezeEventType, nowMs, bollSqueezeSignal)
}
