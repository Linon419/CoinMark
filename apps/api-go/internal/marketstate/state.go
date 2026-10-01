// Package marketstate 在内存里增量维护全市场的分钟级成交状态（每个 market×symbol 一个 1m 环形缓冲），
// 让实时功能不必每轮去 ClickHouse 全量查询。数据来源：启动时从 ClickHouse 加载切点之前的 1m 桶，
// 之后消费 NATS 原始成交；早于切点的成交直接丢弃，保证每笔只计一次。
package marketstate

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	minuteMs = int64(60_000)
	dayMs    = int64(24 * 60 * 60 * 1000)
	// StaleAfterMs 超过这么久没收到任何成交消息，认为状态不可用，调用方应回退到 ClickHouse
	StaleAfterMs = int64(60_000)
)

// Bar 一分钟（或聚合后）的成交统计。
type Bar struct {
	StartMs       int64
	Open          float64
	High          float64
	Low           float64
	Close         float64
	BuyNotional   float64 // 主动买入额
	SellNotional  float64 // 主动卖出额
	QuoteNotional float64 // 成交额
	Trades        int64
}

// Trade 一笔原始成交。
type Trade struct {
	Market       string
	Symbol       string
	TimeMs       int64
	Price        float64
	Qty          float64
	IsBuyerMaker bool
}

type seriesKey struct{ market, symbol string }

type series struct {
	ring []Bar // 下标 = (StartMs/minuteMs) % len(ring)；StartMs 不等于期望值的槽位视为空
}

type State struct {
	mu         sync.RWMutex
	ringSize   int
	series     map[seriesKey]*series
	cutMs      int64
	loaded     bool
	lastRecvMs int64
}

// New 创建状态，ringMinutes 为保留的分钟数（24h = 1440）。
func New(ringMinutes int) *State {
	if ringMinutes < 1 {
		ringMinutes = 1440
	}
	return &State{ringSize: ringMinutes, series: map[seriesKey]*series{}}
}

func (s *State) slot(k seriesKey, startMs int64) *Bar {
	sr := s.series[k]
	if sr == nil {
		sr = &series{ring: make([]Bar, s.ringSize)}
		s.series[k] = sr
	}
	return &sr.ring[(startMs/minuteMs)%int64(s.ringSize)]
}

// LoadBar 写入启动时从 ClickHouse 加载的一根 1m 桶。
func (s *State) LoadBar(market, symbol string, b Bar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.slot(seriesKey{market, symbol}, b.StartMs) = b
}

// SetCut 设置切点：早于它的成交已经包含在加载的数据里。
func (s *State) SetCut(cutMs int64) {
	s.mu.Lock()
	s.cutMs = cutMs
	s.mu.Unlock()
}

func (s *State) MarkLoaded() {
	s.mu.Lock()
	s.loaded = true
	s.mu.Unlock()
}

// Touch 记录最近一次收到消息的时间（用于判断是否断流）。
func (s *State) Touch(nowMs int64) {
	s.mu.Lock()
	if nowMs > s.lastRecvMs {
		s.lastRecvMs = nowMs
	}
	s.mu.Unlock()
}

// Ready 加载完成且最近 StaleAfterMs 内收到过消息。
func (s *State) Ready(nowMs int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded && nowMs-s.lastRecvMs <= StaleAfterMs
}

// Apply 把一笔成交计入所在分钟。返回是否计入。
func (s *State) Apply(t Trade) bool {
	if t.Price <= 0 || t.Qty <= 0 || t.TimeMs <= 0 {
		return false
	}
	start := (t.TimeMs / minuteMs) * minuteMs
	notional := t.Price * t.Qty

	s.mu.Lock()
	defer s.mu.Unlock()
	if t.TimeMs < s.cutMs {
		return false
	}
	b := s.slot(seriesKey{t.Market, t.Symbol}, start)
	if b.StartMs > start {
		return false // 比环窗口还旧的乱序成交，不能覆盖新数据
	}
	if b.StartMs != start {
		*b = Bar{StartMs: start, Open: t.Price, High: t.Price, Low: t.Price}
	}
	if t.Price > b.High {
		b.High = t.Price
	}
	if t.Price < b.Low {
		b.Low = t.Price
	}
	b.Close = t.Price
	b.QuoteNotional += notional
	if t.IsBuyerMaker {
		b.SellNotional += notional
	} else {
		b.BuyNotional += notional
	}
	b.Trades++
	return true
}

// Minutes 返回 [fromMs, toMs] 内有成交的分钟线（含尚未收盘的当前分钟），按时间升序。
func (s *State) Minutes(market, symbol string, fromMs, toMs int64) []Bar {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sr := s.series[seriesKey{market, symbol}]
	if sr == nil {
		return nil
	}
	from := (fromMs / minuteMs) * minuteMs
	var out []Bar
	for _, b := range sr.ring {
		if b.Trades > 0 && b.StartMs >= from && b.StartMs <= toMs {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out
}

// Day 返回从 dayStartMs 起到当前的累计（UTC 日内）。
func (s *State) Day(market, symbol string, dayStartMs int64) (Bar, bool) {
	return Aggregate(s.Minutes(market, symbol, dayStartMs, dayStartMs+dayMs-1))
}

// Symbols 返回某个市场里有数据的币。
func (s *State) Symbols(market string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for k := range s.series {
		if k.market == market {
			out = append(out, k.symbol)
		}
	}
	return out
}

// Aggregate 把按时间升序的分钟线合成一根。
func Aggregate(bars []Bar) (Bar, bool) {
	if len(bars) == 0 {
		return Bar{}, false
	}
	out := Bar{StartMs: bars[0].StartMs, Open: bars[0].Open, High: bars[0].High, Low: bars[0].Low}
	for _, b := range bars {
		if b.High > out.High {
			out.High = b.High
		}
		if b.Low < out.Low {
			out.Low = b.Low
		}
		out.Close = b.Close
		out.BuyNotional += b.BuyNotional
		out.SellNotional += b.SellNotional
		out.QuoteNotional += b.QuoteNotional
		out.Trades += b.Trades
	}
	return out, true
}

// tradePayload 与 collector 发布、ingest 消费的格式一致（apps/ingest-go/internal/nats）。
type tradePayload struct {
	Market       string `json:"market"`
	Symbol       string `json:"symbol"`
	TradeTimeMS  int64  `json:"trade_time_ms"`
	EventTimeMS  int64  `json:"event_time_ms"`
	Price        any    `json:"price"`
	Qty          any    `json:"qty"`
	IsBuyerMaker bool   `json:"is_buyer_maker"`
}

func parseTrade(data []byte) (Trade, bool) {
	var p tradePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return Trade{}, false
	}
	t := Trade{
		Market:       strings.ToLower(strings.TrimSpace(p.Market)),
		Symbol:       strings.ToUpper(strings.TrimSpace(p.Symbol)),
		TimeMs:       p.TradeTimeMS,
		Price:        toFloat(p.Price),
		Qty:          toFloat(p.Qty),
		IsBuyerMaker: p.IsBuyerMaker,
	}
	if t.TimeMs <= 0 {
		t.TimeMs = p.EventTimeMS
	}
	if t.Market == "" || t.Symbol == "" || t.TimeMs <= 0 || t.Price <= 0 || t.Qty <= 0 {
		return Trade{}, false
	}
	return t, true
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}
