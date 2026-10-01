package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

const (
	yidongMinuteMs = int64(60 * 1000)
	yidongDayMs    = int64(24 * 60 * 60 * 1000)
)

type yidongBar struct {
	Ts int64
	O  float64
	H  float64
	L  float64
	C  float64
	QV float64
}

const (
	// 7日/30日新高新低需要今天之前 29 天的日线（与 yidongMaxDailyBefore 的 6/29 天窗口对应）
	yidongDailyLookbackDays = 29
	// 收盘不足 5 分钟的日线可能还有分钟桶没写完/在修复，下一轮重新拉取
	yidongDailyFinalDelayMs = 5 * yidongMinuteMs
)

type yidongDailyFetch func(ctx context.Context, market string, symbols []string, startMs, endMs int64) ([]model.CHTradeRow, error)

// yidongDailyCache 缓存已收盘的日线。收盘的日线不会再变，
// 避免每轮扫描都用 1m 数据把 30 天日线重算一遍（该查询曾占 ClickHouse 扫描量的大头）。
type yidongDailyCache struct {
	mu      sync.Mutex
	bars    map[string]map[int64]yidongBar // market|symbol -> 日线开盘时间 -> 日线
	finalTo map[string]int64               // market|symbol -> 已定稿的最后一天（含）
}

func newYidongDailyCache() *yidongDailyCache {
	return &yidongDailyCache{bars: map[string]map[int64]yidongBar{}, finalTo: map[string]int64{}}
}

var yidongDaily = newYidongDailyCache()

// get 返回 symbols 在 dayStart 之前 yidongDailyLookbackDays 天的日线（按时间升序），只拉缺失或未定稿的日子。
func (c *yidongDailyCache) get(ctx context.Context, market string, symbols []string, dayStart, nowMs int64, fetch yidongDailyFetch) (map[string][]yidongBar, error) {
	from := dayStart - yidongDailyLookbackDays*yidongDayMs
	lastDay := dayStart - yidongDayMs
	finalLimit := lastDay
	if nowMs < dayStart+yidongDailyFinalDelayMs {
		finalLimit = lastDay - yidongDayMs
	}

	groups := map[int64][]string{}
	c.mu.Lock()
	for _, s := range symbols {
		start := from
		if f, ok := c.finalTo[market+"|"+s]; ok && f >= from {
			start = f + yidongDayMs
		}
		if start <= lastDay {
			groups[start] = append(groups[start], s)
		}
	}
	c.mu.Unlock()

	for start, syms := range groups {
		rows, err := fetch(ctx, market, syms, start, dayStart-1)
		if err != nil {
			return nil, err
		}
		fetched := yidongBuildBarMap(rows)
		c.mu.Lock()
		for _, s := range syms {
			key := market + "|" + s
			m := c.bars[key]
			if m == nil {
				m = map[int64]yidongBar{}
				c.bars[key] = m
			}
			for _, b := range fetched[s] {
				m[b.Ts] = b
			}
			if f, ok := c.finalTo[key]; !ok || finalLimit > f {
				c.finalTo[key] = finalLimit
			}
		}
		c.mu.Unlock()
	}

	out := make(map[string][]yidongBar, len(symbols))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range symbols {
		m := c.bars[market+"|"+s]
		bars := make([]yidongBar, 0, len(m))
		for ts, b := range m {
			if ts < from {
				delete(m, ts)
				continue
			}
			if ts < dayStart {
				bars = append(bars, b)
			}
		}
		sort.Slice(bars, func(i, j int) bool { return bars[i].Ts < bars[j].Ts })
		out[s] = bars
	}
	return out, nil
}

// yidongDailyCoverage 判断日线历史是否覆盖今天之前的 7 天/30 天窗口。dayBars 需按时间升序。
func yidongDailyCoverage(dayBars []yidongBar, dayStart int64) (has7d, has30d bool) {
	if len(dayBars) == 0 {
		return false, false
	}
	earliest := dayBars[0].Ts
	return earliest <= dayStart-6*yidongDayMs, earliest <= dayStart-29*yidongDayMs
}

func ScanMarketYidongMinute(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, bn *binance.Client, market string, topN int) (int, error) {
	m := strings.ToLower(strings.TrimSpace(market))
	if m != "spot" && m != "swap" {
		m = "swap"
	}
	if topN <= 0 {
		topN = 200
	}

	nowMs := time.Now().UnixMilli()
	lastClosedStart := (nowMs/yidongMinuteMs)*yidongMinuteMs - yidongMinuteMs
	if lastClosedStart <= 0 {
		return 0, nil
	}
	dayStart := (lastClosedStart / yidongDayMs) * yidongDayMs
	start2h := lastClosedStart - 120*yidongMinuteMs
	start1m := dayStart
	if start2h < start1m {
		start1m = start2h
	}

	symbols, err := yidongTopSymbols(ctx, bn, m, topN)
	if err != nil || len(symbols) == 0 {
		return 0, err
	}

	minRows, err := ch.QueryTradeBuckets(ctx, m, "", symbols, "1m", start1m, lastClosedStart, "asc", 0)
	if err != nil {
		return 0, err
	}
	dayMap, err := yidongDaily.get(ctx, m, symbols, dayStart, nowMs, func(ctx context.Context, market string, syms []string, startMs, endMs int64) ([]model.CHTradeRow, error) {
		return ch.QueryTradeBuckets(ctx, market, "", syms, "1d", startMs, endMs, "asc", 0)
	})
	if err != nil {
		return 0, err
	}

	minMap := yidongBuildBarMap(minRows)
	events := make([]map[string]interface{}, 0, 256)

	for _, sym := range symbols {
		if binance.IsExcludedSymbol(sym) {
			continue
		}
		bars := minMap[sym]
		if len(bars) < 2 {
			continue
		}
		lastIdx := yidongLatestIdxAtOrBefore(bars, lastClosedStart)
		if lastIdx < 1 {
			continue
		}
		latest := bars[lastIdx]
		if latest.Ts < lastClosedStart-3*yidongMinuteMs {
			continue
		}
		activeBars := bars[:lastIdx+1]
		eventTimeMs := latest.Ts + yidongMinuteMs
		has7dCoverage, has30dCoverage := yidongDailyCoverage(dayMap[sym], dayStart)

		dayBars := yidongFilterFrom(activeBars, dayStart)
		if len(dayBars) < 2 {
			continue
		}

		dayOpen := dayBars[0].O
		dayHigh := yidongMaxHigh(dayBars)
		dayLow := yidongMinLow(dayBars)
		dayHighPrev := yidongMaxHigh(dayBars[:len(dayBars)-1])
		dayLowPrev := yidongMinLow(dayBars[:len(dayBars)-1])

		prior6High := yidongMaxDailyBefore(dayMap[sym], dayStart, 6, true)
		prior6Low := yidongMaxDailyBefore(dayMap[sym], dayStart, 6, false)
		prior29High := yidongMaxDailyBefore(dayMap[sym], dayStart, 29, true)
		prior29Low := yidongMaxDailyBefore(dayMap[sym], dayStart, 29, false)

		highBase7 := yidongMax2(dayHighPrev, prior6High)
		highBase30 := yidongMax2(dayHighPrev, prior29High)
		lowBase7 := yidongMin2(dayLowPrev, prior6Low)
		lowBase30 := yidongMin2(dayLowPrev, prior29Low)

		if dayHighPrev > 0 && latest.H > dayHighPrev*(1+1e-8) {
			events = append(events, yidongEvent(m, sym, "new_high_1d", "1m", "1d", eventTimeMs,
				fmt.Sprintf("%s 今日新高", sym),
				map[string]interface{}{"latestHigh": latest.H, "prevHigh": dayHighPrev}))
		}
		if has7dCoverage && highBase7 > 0 && latest.H > highBase7*(1+1e-8) {
			events = append(events, yidongEvent(m, sym, "new_high_7d", "1m", "7d", eventTimeMs,
				fmt.Sprintf("%s 7日新高", sym),
				map[string]interface{}{"latestHigh": latest.H, "prevHigh": highBase7}))
		}
		if has30dCoverage && highBase30 > 0 && latest.H > highBase30*(1+1e-8) {
			events = append(events, yidongEvent(m, sym, "new_high_30d", "1m", "30d", eventTimeMs,
				fmt.Sprintf("%s 30日新高", sym),
				map[string]interface{}{"latestHigh": latest.H, "prevHigh": highBase30}))
		}

		if dayLowPrev > 0 && latest.L < dayLowPrev*(1-1e-8) {
			events = append(events, yidongEvent(m, sym, "new_low_1d", "1m", "1d", eventTimeMs,
				fmt.Sprintf("%s 今日新低", sym),
				map[string]interface{}{"latestLow": latest.L, "prevLow": dayLowPrev}))
		}
		if has7dCoverage && lowBase7 > 0 && latest.L < lowBase7*(1-1e-8) {
			events = append(events, yidongEvent(m, sym, "new_low_7d", "1m", "7d", eventTimeMs,
				fmt.Sprintf("%s 7日新低", sym),
				map[string]interface{}{"latestLow": latest.L, "prevLow": lowBase7}))
		}
		if has30dCoverage && lowBase30 > 0 && latest.L < lowBase30*(1-1e-8) {
			events = append(events, yidongEvent(m, sym, "new_low_30d", "1m", "30d", eventTimeMs,
				fmt.Sprintf("%s 30日新低", sym),
				map[string]interface{}{"latestLow": latest.L, "prevLow": lowBase30}))
		}

		if open5m, ok := yidongCloseAtOrBefore(activeBars, latest.Ts-5*yidongMinuteMs); ok && open5m > 0 {
			ret5m := (latest.C/open5m - 1) * 100
			if et, label, ok2 := yidongClassifyMove(ret5m, "5m", 3, 7, 11, "price"); ok2 {
				events = append(events, yidongEvent(m, sym, et, "5m", "", eventTimeMs,
					fmt.Sprintf("%s %s", sym, label),
					map[string]interface{}{"retPct": ret5m, "open": open5m, "close": latest.C}))
			}
		}
		if open2h, ok := yidongCloseAtOrBefore(activeBars, latest.Ts-120*yidongMinuteMs); ok && open2h > 0 {
			ret2h := (latest.C/open2h - 1) * 100
			if et, label, ok2 := yidongClassifyMove(ret2h, "2h", 3, 7, 11, "price"); ok2 {
				events = append(events, yidongEvent(m, sym, et, "2h", "", eventTimeMs,
					fmt.Sprintf("%s %s", sym, label),
					map[string]interface{}{"retPct": ret2h, "open": open2h, "close": latest.C}))
			}
		}

		if dayOpen > 0 && dayHigh > 0 {
			upPct := (dayHigh - dayOpen) / dayOpen * 100
			pullbackPct := (latest.C - dayHigh) / dayHigh * 100
			if upPct >= 8 && pullbackPct <= -5 {
				events = append(events, yidongEvent(m, sym, "intraday_peak_reversal", "1m", "1d", eventTimeMs,
					fmt.Sprintf("%s 冲高回落", sym),
					map[string]interface{}{"dayOpen": dayOpen, "dayHigh": dayHigh, "close": latest.C, "upPct": upPct, "pullbackPct": pullbackPct}))
			}
		}
		if dayOpen > 0 && dayLow > 0 {
			downPct := (dayLow - dayOpen) / dayOpen * 100
			reboundPct := (latest.C - dayLow) / dayLow * 100
			if downPct <= -8 && reboundPct >= 5 {
				events = append(events, yidongEvent(m, sym, "intraday_bottom_rebound", "1m", "1d", eventTimeMs,
					fmt.Sprintf("%s 探底回升", sym),
					map[string]interface{}{"dayOpen": dayOpen, "dayLow": dayLow, "close": latest.C, "downPct": downPct, "reboundPct": reboundPct}))
			}
		}
	}

	if len(events) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, events)
}

func ScanMarketYidongVolume(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, bn *binance.Client, market string, topN int) (int, error) {
	m := strings.ToLower(strings.TrimSpace(market))
	if m != "spot" && m != "swap" {
		m = "swap"
	}
	if topN <= 0 {
		topN = 200
	}
	nowMs := time.Now().UnixMilli()
	lastClosedStart := (nowMs/yidongMinuteMs)*yidongMinuteMs - yidongMinuteMs
	if lastClosedStart <= 0 {
		return 0, nil
	}
	start1m := lastClosedStart - (24*15+15)*yidongMinuteMs

	symbols, err := yidongTopSymbols(ctx, bn, m, topN)
	if err != nil || len(symbols) == 0 {
		return 0, err
	}
	minRows, err := ch.QueryTradeBuckets(ctx, m, "", symbols, "1m", start1m, lastClosedStart, "asc", 0)
	if err != nil {
		return 0, err
	}
	minMap := yidongBuildBarMap(minRows)
	events := make([]map[string]interface{}, 0, 128)

	for _, sym := range symbols {
		if binance.IsExcludedSymbol(sym) {
			continue
		}
		bars := minMap[sym]
		if len(bars) < 40 {
			continue
		}
		lastIdx := yidongLatestIdxAtOrBefore(bars, lastClosedStart)
		if lastIdx < 15 {
			continue
		}
		latest := bars[lastIdx]
		if latest.Ts < lastClosedStart-3*yidongMinuteMs {
			continue
		}
		activeBars := bars[:lastIdx+1]
		eventTimeMs := latest.Ts + yidongMinuteMs

		curStart := latest.Ts - 14*yidongMinuteMs
		curVol := yidongSumVol(activeBars, curStart, latest.Ts)
		if curVol <= 0 {
			continue
		}

		prevWindowSum := 0.0
		validWindows := 0
		for w := 1; w <= 24; w++ {
			end := latest.Ts - int64(w*15)*yidongMinuteMs
			start := end - 14*yidongMinuteMs
			if end < activeBars[0].Ts {
				break
			}
			prevWindowSum += yidongSumVol(activeBars, start, end)
			validWindows++
		}
		if validWindows < 24 {
			continue
		}
		avgVol := prevWindowSum / float64(validWindows)
		if avgVol <= 0 || curVol < avgVol*50 {
			continue
		}
		open15m, ok := yidongCloseAtOrBefore(activeBars, latest.Ts-15*yidongMinuteMs)
		if !ok || open15m <= 0 {
			continue
		}
		ret15m := (latest.C/open15m - 1) * 100
		if et, label, ok2 := yidongClassifyMove(ret15m, "15m", 7, 11, 15, "volume"); ok2 {
			events = append(events, yidongEvent(m, sym, et, "15m", "24x15m", eventTimeMs,
				fmt.Sprintf("%s 放量%s", sym, strings.TrimPrefix(label, "15分钟")),
				map[string]interface{}{"retPct": ret15m, "open": open15m, "close": latest.C, "curVol": curVol, "avgVol": avgVol, "volumeFactor": curVol / avgVol}))
		}
	}

	if len(events) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, events)
}

func yidongTopSymbols(ctx context.Context, bn *binance.Client, market string, topN int) ([]string, error) {
	if bn == nil {
		return nil, fmt.Errorf("binance client not configured")
	}
	tickers, err := bn.GetTicker24hAll(ctx, market)
	if err != nil {
		return nil, err
	}
	type row struct {
		symbol string
		qv     float64
	}
	items := make([]row, 0, len(tickers))
	for _, t := range tickers {
		s, _ := t["symbol"].(string)
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || binance.IsExcludedSymbol(s) {
			continue
		}
		items = append(items, row{symbol: s, qv: yidongToFloat(t["quoteVolume"])})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].qv > items[j].qv })
	if len(items) > topN {
		items = items[:topN]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.symbol)
	}
	return out, nil
}

func yidongToFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func yidongBuildBarMap(rows []model.CHTradeRow) map[string][]yidongBar {
	out := map[string][]yidongBar{}
	for _, r := range rows {
		if r.OpenPrice == nil || r.HighPrice == nil || r.LowPrice == nil || r.ClosePrice == nil {
			continue
		}
		s := strings.ToUpper(strings.TrimSpace(r.Symbol))
		out[s] = append(out[s], yidongBar{
			Ts: r.BucketStartMs, O: *r.OpenPrice, H: *r.HighPrice, L: *r.LowPrice, C: *r.ClosePrice, QV: r.QuoteNotional,
		})
	}
	for s := range out {
		sort.Slice(out[s], func(i, j int) bool { return out[s][i].Ts < out[s][j].Ts })
	}
	return out
}

func yidongFilterFrom(bars []yidongBar, startMs int64) []yidongBar {
	if len(bars) == 0 {
		return nil
	}
	i := 0
	for i < len(bars) && bars[i].Ts < startMs {
		i++
	}
	if i >= len(bars) {
		return nil
	}
	return bars[i:]
}

func yidongLatestIdxAtOrBefore(bars []yidongBar, ts int64) int {
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].Ts <= ts {
			return i
		}
	}
	return -1
}

func yidongCloseAtOrBefore(bars []yidongBar, ts int64) (float64, bool) {
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].Ts <= ts {
			return bars[i].C, true
		}
	}
	return 0, false
}

func yidongMaxHigh(bars []yidongBar) float64 {
	maxV := 0.0
	for _, b := range bars {
		if b.H > maxV {
			maxV = b.H
		}
	}
	return maxV
}

func yidongMinLow(bars []yidongBar) float64 {
	minV := 0.0
	for i, b := range bars {
		if i == 0 || b.L < minV {
			minV = b.L
		}
	}
	return minV
}

func yidongMaxDailyBefore(dayBars []yidongBar, dayStart int64, days int, wantHigh bool) float64 {
	if days <= 0 || len(dayBars) == 0 {
		return 0
	}
	start := dayStart - int64(days)*yidongDayMs
	v := 0.0
	init := false
	for _, b := range dayBars {
		if b.Ts < start || b.Ts >= dayStart {
			continue
		}
		x := b.L
		if wantHigh {
			x = b.H
		}
		if !init {
			v = x
			init = true
			continue
		}
		if wantHigh {
			if x > v {
				v = x
			}
		} else if x < v {
			v = x
		}
	}
	if !init {
		return 0
	}
	return v
}

func yidongSumVol(bars []yidongBar, startTs, endTs int64) float64 {
	if endTs < startTs {
		return 0
	}
	sum := 0.0
	for _, b := range bars {
		if b.Ts < startTs {
			continue
		}
		if b.Ts > endTs {
			break
		}
		sum += math.Max(0, b.QV)
	}
	return sum
}

func yidongClassifyMove(retPct float64, interval string, t1, t2, t3 float64, prefix string) (eventType, label string, ok bool) {
	absRet := math.Abs(retPct)
	if absRet < t1 {
		return "", "", false
	}
	size := "large"
	cnSize := "大"
	if absRet < t2 {
		size = "small"
		cnSize = "小"
	} else if absRet < t3 {
		size = "medium"
		cnSize = "中"
	}
	if retPct >= 0 {
		return fmt.Sprintf("%s_rise_%s_%s", prefix, size, interval), fmt.Sprintf("%s%s涨", yidongIntervalLabel(interval), cnSize), true
	}
	return fmt.Sprintf("%s_fall_%s_%s", prefix, size, interval), fmt.Sprintf("%s%s跌", yidongIntervalLabel(interval), cnSize), true
}

func yidongIntervalLabel(interval string) string {
	switch interval {
	case "5m":
		return "5分钟"
	case "2h":
		return "2小时"
	case "15m":
		return "15分钟"
	default:
		return interval
	}
}

func yidongEvent(market, symbol, eventType, tfSignal, tfLevel string, eventTimeMs int64, title string, details map[string]interface{}) map[string]interface{} {
	b, _ := json.Marshal(details)
	var level interface{}
	if tfLevel != "" {
		level = tfLevel
	}
	return map[string]interface{}{
		"market": market, "symbol": symbol, "event_type": eventType,
		"tf_signal": tfSignal, "tf_level": level,
		"event_time_ms": eventTimeMs, "title": title, "details": string(b),
	}
}

func yidongMax2(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a > b {
		return a
	}
	return b
}

func yidongMin2(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}
