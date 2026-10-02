package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/marketstate"
	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

// CoinArch 格式的市场异动（规格：docs/plans/2026-10-01-coinarch-anomaly-spec.md）。
// 事件类型统一以 arch_ 开头，title 即完整的推送文案。
const (
	ArchEventPriceShock  = "arch_price_shock"
	ArchEventFundingRate = "arch_funding_rate"
	ArchEventVolumeSpike = "arch_volume_spike"

	archMarket           = "swap"
	archShockCooldownMs  = 5 * yidongMinuteMs
	archFundingCooldown  = 4 * 60 * yidongMinuteMs
	archVolumeCooldownMs = 65 * yidongMinuteMs
	archFundingThreshold = 0.005 // |资金费率| ≥ 0.5%
	archVolumeRatio      = 0.8   // 60 分钟内成交 ≥ 昨日全天的 80%
	archIntradayMinPct   = 5.0   // 日内回调/反弹附加的最小幅度
	archFirstMinDays     = 7     // (N天首次) 至少间隔 7 天才显示
	archBottomMaxPos     = 0.1   // 60 日区间位置 ≤10% 视为“底部”（低置信度近似）
)

type archShock struct {
	Up      bool
	Minutes int
	Pct     float64
}

// archShockThreshold 短时波动阈值（涨跌对称）：1~5 分钟 3%，6~15 分钟 5%，16~45 分钟 10%。
func archShockThreshold(minutes int) float64 {
	switch {
	case minutes <= 5:
		return 3
	case minutes <= 15:
		return 5
	default:
		return 10
	}
}

func archShockTier(minutes int) int {
	switch {
	case minutes <= 5:
		return 0
	case minutes <= 15:
		return 1
	default:
		return 2
	}
}

// archDetectPriceShock 找出满足阈值的最短窗口（1~45 分钟，含当前分钟）。
// 涨以窗口内最低价为起点，跌以窗口内最高价为起点。bars 需按时间升序，最后一根为当前分钟。
func archDetectPriceShock(bars []marketstate.Bar, price float64) (archShock, bool) {
	if len(bars) == 0 || price <= 0 {
		return archShock{}, false
	}
	cur := bars[len(bars)-1].StartMs
	low, high := math.Inf(1), 0.0
	i := len(bars) - 1
	for w := 1; w <= 45; w++ {
		start := cur - int64(w-1)*yidongMinuteMs
		for i >= 0 && bars[i].StartMs >= start {
			low = math.Min(low, bars[i].Low)
			high = math.Max(high, bars[i].High)
			i--
		}
		th := archShockThreshold(w)
		up, down := 0.0, 0.0
		if low > 0 && !math.IsInf(low, 1) {
			up = (price/low - 1) * 100
		}
		if high > 0 {
			down = (price/high - 1) * 100
		}
		if up >= th || -down >= th {
			if up >= -down {
				return archShock{Up: true, Minutes: w, Pct: up}, true
			}
			return archShock{Up: false, Minutes: w, Pct: down}, true
		}
	}
	return archShock{}, false
}

// archVolumeWindow 首次触发时取成交达到昨日 80% 的最短窗口；持续期间（full）报告完整 60 分钟。
func archVolumeWindow(bars []marketstate.Bar, nowMinute int64, yday float64, full bool) (int, float64, bool) {
	if yday <= 0 {
		return 0, 0, false
	}
	need := yday * archVolumeRatio
	vol := 0.0
	i := len(bars) - 1
	for w := 1; w <= 60; w++ {
		start := nowMinute - int64(w-1)*yidongMinuteMs
		for i >= 0 && bars[i].StartMs >= start {
			vol += bars[i].QuoteNotional
			i--
		}
		if !full && vol >= need {
			return w, vol, true
		}
	}
	if full && vol >= need {
		return 60, vol, true
	}
	return 0, 0, false
}

// archIntradayExtra 下跌提醒且当日上涨时附加“日内回调”（距日内高点），上涨提醒且当日下跌时附加“日内反弹”（距日内低点），幅度需 ≥5%。
func archIntradayExtra(up bool, dayChgPct, price, dayHigh, dayLow float64) (string, bool) {
	if !up && dayChgPct > 0 && dayHigh > 0 {
		if pb := (price/dayHigh - 1) * 100; pb <= -archIntradayMinPct {
			return fmt.Sprintf(" , 日内回调 %.2f%%", pb), true
		}
	}
	if up && dayChgPct < 0 && dayLow > 0 {
		if rb := (price/dayLow - 1) * 100; rb >= archIntradayMinPct {
			return fmt.Sprintf(" , 日内反弹 %.2f%%", rb), true
		}
	}
	return "", false
}

func archHead(symbol string, price, dayChgPct float64) string {
	base := strings.TrimSuffix(strings.ToUpper(symbol), "USDT")
	return fmt.Sprintf("%s - ₮%s (%.2f%%)", base, strconv.FormatFloat(price, 'f', -1, 64), dayChgPct)
}

func archShockBody(s archShock) string {
	verb := "涨"
	if !s.Up {
		verb = "跌"
	}
	return fmt.Sprintf(" , %d分钟%s %.2f%%", s.Minutes, verb, s.Pct)
}

func archFundingBody(rate float64) string {
	return fmt.Sprintf(" , 资金费异常: %+.4f%%", rate*100)
}

// archVolumeBody 末尾保留一个空格：原文在“量能x”与后缀之间是两个空格。
func archVolumeBody(position string, minutes int, vol, yday, netFlow, ratio float64) string {
	dir := "入"
	if netFlow < 0 {
		dir = "出"
	}
	return fmt.Sprintf(" (%s) , %d分钟成交%.2f万 = 昨日%.0f%% , 今日净流%s%.2f万 , 量能%.2fx ",
		position, minutes, vol/1e4, vol/yday*100, dir, netFlow/1e4, ratio)
}

func archSuffix(firstDays, count int) string {
	s := ""
	if firstDays > 0 {
		s += fmt.Sprintf(" (%d天首次)", firstDays)
	}
	return s + fmt.Sprintf(" [%d]", count)
}

// archDailyCounter 每个币当天的推送次数，UTC 0 点清零。
type archDailyCounter struct {
	mu     sync.Mutex
	day    int64
	counts map[string]int
}

func newArchDailyCounter() *archDailyCounter {
	return &archDailyCounter{counts: map[string]int{}}
}

func (c *archDailyCounter) seed(dayStart int64, counts map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.day, c.counts = dayStart, map[string]int{}
	for k, v := range counts {
		c.counts[k] = v
	}
}

func (c *archDailyCounter) next(symbol string, dayStart int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dayStart != c.day {
		c.day, c.counts = dayStart, map[string]int{}
	}
	c.counts[symbol]++
	return c.counts[symbol]
}

// ArchAlertEngine 定时从内存市场状态检测 CoinArch 格式的异动，写入 anomaly_events。
type ArchAlertEngine struct {
	ms    *marketstate.State
	ch    *chrepo.Client
	store *sqlite.Store

	counter    *archDailyCounter
	lastAlert  map[string]int64 // 冷却：key -> 上次推送时间
	intradayAt map[string]float64
}

func NewArchAlertEngine(ms *marketstate.State, ch *chrepo.Client, store *sqlite.Store) *ArchAlertEngine {
	return &ArchAlertEngine{ms: ms, ch: ch, store: store, counter: newArchDailyCounter(),
		lastAlert: map[string]int64{}, intradayAt: map[string]float64{}}
}

// Run 每 10 秒检测短时波动，每 60 秒检测资费异常和交易放量。
func (e *ArchAlertEngine) Run(ctx context.Context, stopCh <-chan struct{}) {
	e.seedCounter(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastSlow int64
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now().UnixMilli()
		if !e.ms.Ready(now) {
			continue
		}
		events := e.scanPriceShock(now)
		if now-lastSlow >= 60_000 {
			lastSlow = now
			events = append(events, e.scanFunding(ctx, now)...)
			events = append(events, e.scanVolume(ctx, now)...)
		}
		if len(events) == 0 {
			continue
		}
		if _, err := insertAnomalyEvents(ctx, e.store, events); err != nil {
			log.Printf("arch alerts: insert error: %v", err)
		}
	}
}

type archQuote struct {
	price, dayChg, dayHigh, dayLow, netFlow, dayVol float64
	bars                                            []marketstate.Bar
}

func (e *ArchAlertEngine) quote(symbol string, now int64) (archQuote, bool) {
	dayStart := (now / yidongDayMs) * yidongDayMs
	bars := e.ms.Minutes(archMarket, symbol, now-60*yidongMinuteMs, now)
	day, ok := e.ms.Day(archMarket, symbol, dayStart)
	if len(bars) == 0 || !ok || day.Open <= 0 {
		return archQuote{}, false
	}
	// 最后一根须是当前或上一分钟：没有实时成交的币（如仅有启动时加载的历史）不检测，避免把旧行情当成新异动
	if bars[len(bars)-1].StartMs < (now/yidongMinuteMs)*yidongMinuteMs-yidongMinuteMs {
		return archQuote{}, false
	}
	price := bars[len(bars)-1].Close
	return archQuote{
		price: price, dayChg: (price/day.Open - 1) * 100, dayHigh: day.High, dayLow: day.Low,
		netFlow: day.BuyNotional - day.SellNotional, dayVol: day.QuoteNotional, bars: bars,
	}, true
}

func (e *ArchAlertEngine) cooled(key string, now, cooldown int64) bool {
	if last, ok := e.lastAlert[key]; ok && now-last < cooldown {
		return false
	}
	e.lastAlert[key] = now
	return true
}

func (e *ArchAlertEngine) emit(eventType, symbol string, now int64, body string, firstDays int, details map[string]interface{}, q archQuote) map[string]interface{} {
	dayStart := (now / yidongDayMs) * yidongDayMs
	title := archHead(symbol, q.price, q.dayChg) + body + archSuffix(firstDays, e.counter.next(symbol, dayStart))
	details["price"] = q.price
	details["dayChgPct"] = q.dayChg
	return yidongEvent(archMarket, symbol, eventType, "1m", "", now, title, details)
}

func (e *ArchAlertEngine) scanPriceShock(now int64) []map[string]interface{} {
	var out []map[string]interface{}
	for _, sym := range e.ms.Symbols(archMarket) {
		if binance.IsExcludedSymbol(sym) {
			continue
		}
		q, ok := e.quote(sym, now)
		if !ok {
			continue
		}
		s, ok := archDetectPriceShock(q.bars, q.price)
		if !ok || !e.cooled(fmt.Sprintf("shock|%s|%v|%d", sym, s.Up, archShockTier(s.Minutes)), now, archShockCooldownMs) {
			continue
		}
		body := archShockBody(s)
		// 日内回调/反弹每个日内高点（低点）只附加一次
		if extra, ok := archIntradayExtra(s.Up, q.dayChg, q.price, q.dayHigh, q.dayLow); ok {
			ref, key := q.dayHigh, "pullback|"+sym
			if s.Up {
				ref, key = q.dayLow, "rebound|"+sym
			}
			if e.intradayAt[key] != ref {
				e.intradayAt[key] = ref
				body += extra
			}
		}
		out = append(out, e.emit(ArchEventPriceShock, sym, now, body, 0,
			map[string]interface{}{"minutes": s.Minutes, "retPct": s.Pct, "up": s.Up}, q))
	}
	return out
}

func (e *ArchAlertEngine) scanFunding(ctx context.Context, now int64) []map[string]interface{} {
	if e.ch == nil {
		return nil
	}
	rows, err := e.ch.QueryFundingSnapshots(ctx)
	if err != nil {
		log.Printf("arch alerts: funding query error: %v", err)
		return nil
	}
	var out []map[string]interface{}
	for _, r := range rows {
		if math.Abs(r.LastFundingRate) < archFundingThreshold || binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		q, ok := e.quote(r.Symbol, now)
		if !ok || !e.cooled("funding|"+r.Symbol, now, archFundingCooldown) {
			continue
		}
		out = append(out, e.emit(ArchEventFundingRate, r.Symbol, now, archFundingBody(r.LastFundingRate),
			e.firstInDays(ctx, ArchEventFundingRate, r.Symbol, now), map[string]interface{}{"fundingRate": r.LastFundingRate}, q))
	}
	return out
}

func (e *ArchAlertEngine) scanVolume(ctx context.Context, now int64) []map[string]interface{} {
	symbols := e.ms.Symbols(archMarket)
	dayStart := (now / yidongDayMs) * yidongDayMs
	daily, err := yidongDaily.get(ctx, archMarket, symbols, dayStart, now, func(ctx context.Context, market string, syms []string, startMs, endMs int64) ([]model.CHTradeRow, error) {
		return e.ch.QueryTradeBuckets(ctx, market, "", syms, "1d", startMs, endMs, "asc", 0)
	})
	if err != nil {
		log.Printf("arch alerts: daily query error: %v", err)
		return nil
	}
	nowMinute := (now / yidongMinuteMs) * yidongMinuteMs
	elapsedMin := float64(now-dayStart) / float64(yidongMinuteMs)
	var out []map[string]interface{}
	for _, sym := range symbols {
		days := daily[sym]
		if len(days) == 0 || days[len(days)-1].Ts != dayStart-yidongDayMs || binance.IsExcludedSymbol(sym) {
			continue
		}
		yday := days[len(days)-1].QV
		last, seen := e.lastAlert["volume|"+sym]
		if seen && now-last < archVolumeCooldownMs {
			continue
		}
		full := seen && now-last < 3*60*yidongMinuteMs
		q, ok := e.quote(sym, now)
		if !ok {
			continue
		}
		minutes, vol, ok := archVolumeWindow(q.bars, nowMinute, yday, full)
		if !ok {
			continue
		}
		e.lastAlert["volume|"+sym] = now
		ratio := 0.0
		if elapsedMin > 0 {
			ratio = q.dayVol / elapsedMin * 1440 / yday // 按当前速度推算的今日成交 / 昨日成交
		}
		body := archVolumeBody(archRangePosition(days, q.price), minutes, vol, yday, q.netFlow, ratio)
		out = append(out, e.emit(ArchEventVolumeSpike, sym, now, body, e.firstInDays(ctx, ArchEventVolumeSpike, sym, now),
			map[string]interface{}{"minutes": minutes, "volume": vol, "yesterday": yday, "ratio": ratio}, q))
	}
	return out
}

// archRangePosition 价格位于近期（日线缓存 29 天）区间底部 10% 以内为“底部”，否则为“中继”。
// CoinArch 用 60 日区间，且样本显示并非单纯按位置划分，此处为低置信度近似。
func archRangePosition(days []yidongBar, price float64) string {
	hi, lo := 0.0, math.Inf(1)
	for _, d := range days {
		hi, lo = math.Max(hi, d.H), math.Min(lo, d.L)
	}
	if hi > lo && (price-lo)/(hi-lo) <= archBottomMaxPos {
		return "底部"
	}
	return "中继"
}

// firstInDays 距上一次同类事件 ≥7 天时返回天数，否则 0。
func (e *ArchAlertEngine) firstInDays(ctx context.Context, eventType, symbol string, now int64) int {
	var last int64
	err := e.store.GetContext(ctx, &last, `SELECT COALESCE(MAX(event_time_ms), 0) FROM anomaly_events WHERE market = ? AND symbol = ? AND event_type = ? AND event_time_ms < ?`,
		archMarket, symbol, eventType, now)
	if err != nil || last == 0 {
		return 0
	}
	if days := int((now - last) / yidongDayMs); days >= archFirstMinDays {
		return days
	}
	return 0
}

// seedCounter 重启后从今天已写入的 arch 事件恢复 [N] 计数。
func (e *ArchAlertEngine) seedCounter(ctx context.Context) {
	dayStart := (time.Now().UnixMilli() / yidongDayMs) * yidongDayMs
	var rows []struct {
		Symbol string `db:"symbol"`
		N      int    `db:"n"`
	}
	if err := e.store.SelectContext(ctx, &rows, `SELECT symbol, COUNT(*) AS n FROM anomaly_events WHERE market = ? AND event_type LIKE 'arch_%' AND event_time_ms >= ? GROUP BY symbol`,
		archMarket, dayStart); err != nil {
		log.Printf("arch alerts: seed counter error: %v", err)
		return
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.Symbol] = r.N
	}
	e.counter.seed(dayStart, counts)
}
