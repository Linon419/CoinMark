package service

import (
	"context"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/binance"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

// 潜力区：两个名单，规则来自 2026-07~09 全市场回测（只看加密币小币）。
//   - 观察名单（低位高积累）：3 天合约净流入 ≥ 500 万，离 30 天低点 < 30%。
//     回测 24h 内先涨 5% 的 42%、先跌 5% 的 24%（随机为 33% / 26%），只有 20 个币，样本少。
//   - 别追名单：3 天净流入 < 50 万，4 小时涨 ≥ 10%。回测 4h 后 58% 下跌，24h 后中位跌 4.8%。
//
// 净流入 = 主动买入额 − 主动卖出额（与 CoinArch 口径一致），用已收盘的 1h K 线。
const (
	potentialMinVol24          = 2e6  // 24h 成交额下限
	potentialMinOI             = 2e6  // 持仓太少的不碰
	potentialMaxMcap           = 1e9  // 小币：市值 < 10 亿
	potentialMaxOIUnknownMcap  = 5e7  // 没有市值数据时，持仓 < 5000 万算小币
	potentialWatchMinAcc3d     = 5e6  // 观察名单：3 天积累 ≥ 500 万
	potentialWatchMaxRise      = 0.30 // 观察名单：离 30 天低点 < 30%
	potentialAvoidMaxAcc3d     = 5e5  // 别追名单：3 天积累 < 50 万
	potentialAvoidMinRet4h     = 0.10 // 别追名单：4 小时涨 ≥ 10%
	potentialEMACrossLookbackH = 4    // EMA 标签：最近 4 小时内上穿过
	potentialRecordGapMs       = 24 * 3600 * 1000
	potentialTouchPct          = 0.05
	potentialScanInterval      = 5 * time.Minute
	potentialRefreshInterval   = time.Hour
	potentialOICacheTTL        = 10 * time.Minute

	PotentialListWatch = "watch"
	PotentialListAvoid = "avoid"
)

type PotentialItem struct {
	Symbol        string  `json:"symbol"`
	DecisionMs    int64   `json:"decision_ms"`
	Price         float64 `json:"price"`
	Acc3d         float64 `json:"acc_3d"`
	RiseFromLow30 float64 `json:"rise_from_low30"`
	Ret4h         float64 `json:"ret_4h"`
	Vol24         float64 `json:"vol_24h"`
	OIUSD         float64 `json:"oi_usd"`
	MarketCap     float64 `json:"market_cap"` // 0 = 没有市值数据
	EMACross      bool    `json:"ema_cross"`
}

// potentialHourlyMetrics 用已收盘 1h K 线（升序，至少 72 根）算积累量、4h 涨幅、24h 成交额。
func potentialHourlyMetrics(h1 []BollPumpBar) (PotentialItem, bool) {
	n := len(h1)
	if n < 72 {
		return PotentialItem{}, false
	}
	last := h1[n-1]
	it := PotentialItem{DecisionMs: last.OpenTimeMs + 3600*1000, Price: last.Close}
	for i := n - 72; i < n; i++ {
		it.Acc3d += 2*h1[i].TakerBuyQuote - h1[i].QuoteVolume
	}
	for i := n - 24; i < n; i++ {
		it.Vol24 += h1[i].QuoteVolume
	}
	if prev := h1[n-5].Close; prev > 0 {
		it.Ret4h = last.Close/prev - 1
	}
	return it, true
}

// potentialRiseFromLow30 现价相对 30 天最低价的涨幅；4h K 线覆盖 30 天，再补上最近几根 1h 的低点。
func potentialRiseFromLow30(h4, h1 []BollPumpBar, decisionMs int64, price float64) (float64, bool) {
	from := decisionMs - 30*24*3600*1000
	low := math.Inf(1)
	for _, b := range h4 {
		if b.OpenTimeMs >= from && b.Low > 0 && b.Low < low {
			low = b.Low
		}
	}
	for i := len(h1) - 1; i >= 0 && i >= len(h1)-4; i-- {
		if h1[i].Low > 0 && h1[i].Low < low {
			low = h1[i].Low
		}
	}
	if math.IsInf(low, 1) || len(h4) < 150 { // 不到 25 天的 4h 数据，算不出 30 天低点
		return 0, false
	}
	return price/low - 1, true
}

// potentialEMACrossRecent 最近 lookback 根内，收盘价从下方上穿 EMA100/EMA200 且 EMA100 > EMA200（多头排列）。
func potentialEMACrossRecent(bars []BollPumpBar, lookback int) bool {
	n := len(bars)
	if n < bollSqueezeMinBars || lookback <= 0 {
		return false
	}
	e100 := bollSqueezeEMA(bars, 100)
	e200 := bollSqueezeEMA(bars, 200)
	for i := n - lookback; i < n; i++ {
		top := math.Max(e100[i], e200[i])
		prevTop := math.Max(e100[i-1], e200[i-1])
		if e100[i] > e200[i] && bars[i].Close > top && bars[i-1].Close <= prevTop {
			return true
		}
	}
	return false
}

// potentialIsSmallCoin 市值 < 10 亿；没有市值数据时用持仓 < 5000 万代替。
func potentialIsSmallCoin(mcap, oiUSD float64) bool {
	if mcap > 0 {
		return mcap < potentialMaxMcap
	}
	return oiUSD < potentialMaxOIUnknownMcap
}

// PotentialOutcome 上榜后的表现：4h、24h 收益和 24h 内先涨 5%（1）还是先跌 5%（-1，同一根两边都碰算先跌），都没碰到为 0。
type PotentialOutcome struct {
	Ret4h       *float64
	Ret24h      *float64
	FirstTouch5 *int
}

func potentialOutcome(m15 []BollPumpBar, enteredMs int64, entry float64, nowMs int64) PotentialOutcome {
	var out PotentialOutcome
	if entry <= 0 {
		return out
	}
	const barMs = 15 * 60 * 1000
	closeAt := func(t int64) (float64, bool) {
		px, ok := 0.0, false
		for _, b := range m15 {
			if b.OpenTimeMs >= enteredMs && b.OpenTimeMs+barMs <= t {
				px, ok = b.Close, true
			}
		}
		return px, ok
	}
	if nowMs >= enteredMs+4*3600*1000 {
		if px, ok := closeAt(enteredMs + 4*3600*1000); ok {
			r := px/entry - 1
			out.Ret4h = &r
		}
	}
	end := enteredMs + 24*3600*1000
	if nowMs < end {
		return out
	}
	if px, ok := closeAt(end); ok {
		r := px/entry - 1
		out.Ret24h = &r
		touch := 0
		for _, b := range m15 {
			if b.OpenTimeMs < enteredMs || b.OpenTimeMs >= end {
				continue
			}
			if b.Low <= entry*(1-potentialTouchPct) {
				touch = -1
				break
			}
			if b.High >= entry*(1+potentialTouchPct) {
				touch = 1
				break
			}
		}
		out.FirstTouch5 = &touch
	}
	return out
}

type potentialOI struct {
	usd float64
	at  time.Time
}

// PotentialScanner 每 5 分钟用 BOLL 扫描的 WS K 线缓存扫全部合约，名单放内存，上榜记录写 SQLite。
type PotentialScanner struct {
	source BollPumpSource
	ch     *chrepo.Client
	bn     *binance.Client
	store  *sqlite.Store

	refreshedAt time.Time
	nonCoin     map[string]bool
	mcapSupply  map[string]float64 // 资产 → 流通量（市值随价格实时算）
	mcapFixed   map[string]float64 // 没有流通量时用的市值
	oi          map[string]potentialOI

	mu        sync.RWMutex
	watch     []PotentialItem
	avoid     []PotentialItem
	updatedMs int64
}

func NewPotentialScanner(source BollPumpSource, ch *chrepo.Client, bn *binance.Client, store *sqlite.Store) *PotentialScanner {
	return &PotentialScanner{source: source, ch: ch, bn: bn, store: store, oi: map[string]potentialOI{}}
}

func (s *PotentialScanner) Snapshot() (watch, avoid []PotentialItem, updatedMs int64) {
	if s == nil {
		return nil, nil, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.watch, s.avoid, s.updatedMs
}

func (s *PotentialScanner) Run(ctx context.Context, stopCh <-chan struct{}) {
	ticker := time.NewTicker(potentialScanInterval)
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

func (s *PotentialScanner) refreshReference(ctx context.Context) {
	if time.Since(s.refreshedAt) < potentialRefreshInterval && s.nonCoin != nil {
		return
	}
	nonCoin, err := s.bn.GetFuturesNonCoinSymbols(ctx)
	if err != nil {
		log.Printf("potential: exchangeInfo error: %v", err)
		return
	}
	caps, err := s.ch.QueryMarketCaps(ctx, nil)
	if err != nil {
		log.Printf("potential: market caps error: %v", err)
		return
	}
	supply := make(map[string]float64, len(caps))
	fixed := make(map[string]float64, len(caps))
	for _, c := range caps {
		if c.CirculatingSupply > 0 {
			supply[c.Asset] = c.CirculatingSupply
		} else if c.MarketCapUSD > 0 {
			fixed[c.Asset] = c.MarketCapUSD
		}
	}
	s.nonCoin, s.mcapSupply, s.mcapFixed, s.refreshedAt = nonCoin, supply, fixed, time.Now()
}

func (s *PotentialScanner) marketCap(symbol string, price float64) float64 {
	asset := strings.TrimSuffix(symbol, "USDT")
	if v := s.mcapSupply[asset]; v > 0 {
		return v * price
	}
	return s.mcapFixed[asset]
}

func (s *PotentialScanner) openInterestUSD(ctx context.Context, symbol string, price float64) (float64, bool) {
	if c, ok := s.oi[symbol]; ok && time.Since(c.at) < potentialOICacheTTL {
		return c.usd, true
	}
	raw, err := s.bn.GetFuturesOpenInterest(ctx, symbol)
	if err != nil {
		return 0, false
	}
	qty, _ := strconv.ParseFloat(potentialString(raw["openInterest"]), 64)
	usd := qty * price
	s.oi[symbol] = potentialOI{usd: usd, at: time.Now()}
	return usd, true
}

func potentialString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *PotentialScanner) scan(ctx context.Context) {
	s.refreshReference(ctx)
	if s.nonCoin == nil {
		return
	}
	symbols, err := s.source.Symbols(ctx, "swap", 0)
	if err != nil {
		log.Printf("potential: symbols error: %v", err)
		return
	}
	nowMs := time.Now().UnixMilli()
	var watch, avoid []PotentialItem
	for _, symbol := range symbols {
		if s.nonCoin[symbol] {
			continue
		}
		h1, err := s.source.Klines(ctx, "swap", symbol, "1h", 499)
		if err != nil {
			continue
		}
		h1 = bollPumpClosedBarsBefore(h1, nowMs)
		it, ok := potentialHourlyMetrics(h1)
		if !ok || it.Vol24 < potentialMinVol24 {
			continue
		}
		it.Symbol = symbol
		it.MarketCap = s.marketCap(symbol, it.Price)
		if it.MarketCap >= potentialMaxMcap {
			continue
		}
		isWatch, isAvoid := false, false
		if it.Acc3d >= potentialWatchMinAcc3d {
			h4, err := s.source.Klines(ctx, "swap", symbol, "4h", 499)
			if err != nil {
				continue
			}
			rise, ok := potentialRiseFromLow30(bollPumpClosedBarsBefore(h4, nowMs), h1, it.DecisionMs, it.Price)
			it.RiseFromLow30 = rise
			isWatch = ok && rise < potentialWatchMaxRise
		}
		isAvoid = it.Acc3d < potentialAvoidMaxAcc3d && it.Ret4h >= potentialAvoidMinRet4h
		if !isWatch && !isAvoid {
			continue
		}
		oi, ok := s.openInterestUSD(ctx, symbol, it.Price)
		if !ok || oi < potentialMinOI || !potentialIsSmallCoin(it.MarketCap, oi) {
			continue
		}
		it.OIUSD = oi
		it.EMACross = potentialEMACrossRecent(h1, potentialEMACrossLookbackH)
		if !it.EMACross {
			if m15, err := s.source.Klines(ctx, "swap", symbol, "15m", 499); err == nil {
				it.EMACross = potentialEMACrossRecent(bollPumpClosedBarsBefore(m15, nowMs), potentialEMACrossLookbackH*4)
			}
		}
		if isWatch {
			watch = append(watch, it)
		}
		if isAvoid {
			avoid = append(avoid, it)
		}
	}
	sort.Slice(watch, func(i, j int) bool { return watch[i].Acc3d > watch[j].Acc3d })
	sort.Slice(avoid, func(i, j int) bool { return avoid[i].Ret4h > avoid[j].Ret4h })
	s.mu.Lock()
	s.watch, s.avoid, s.updatedMs = watch, avoid, nowMs
	s.mu.Unlock()

	if s.store == nil {
		return
	}
	if err := s.record(ctx, PotentialListWatch, watch); err != nil {
		log.Printf("potential: record watch error: %v", err)
	}
	if err := s.record(ctx, PotentialListAvoid, avoid); err != nil {
		log.Printf("potential: record avoid error: %v", err)
	}
	if err := s.fillOutcomes(ctx, nowMs); err != nil {
		log.Printf("potential: fill outcomes error: %v", err)
	}
}

// record 同一个币同一个名单 24 小时内只记一次（与回测去重一致）。
func (s *PotentialScanner) record(ctx context.Context, list string, items []PotentialItem) error {
	if len(items) == 0 {
		return nil
	}
	return s.store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, it := range items {
			var n int
			if err := tx.Get(&n, `SELECT COUNT(*) FROM potential_picks WHERE list = ? AND symbol = ? AND entered_ms > ?`,
				list, it.Symbol, it.DecisionMs-potentialRecordGapMs); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO potential_picks
(list, symbol, entered_ms, entry_price, acc_3d, rise_from_low30, ret_4h_before, ema_cross) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				list, it.Symbol, it.DecisionMs, it.Price, it.Acc3d, it.RiseFromLow30, it.Ret4h, it.EMACross); err != nil {
				return err
			}
		}
		return nil
	})
}

type PotentialPick struct {
	ID            int64    `db:"id" json:"id"`
	List          string   `db:"list" json:"list"`
	Symbol        string   `db:"symbol" json:"symbol"`
	EnteredMs     int64    `db:"entered_ms" json:"entered_ms"`
	EntryPrice    float64  `db:"entry_price" json:"entry_price"`
	Acc3d         float64  `db:"acc_3d" json:"acc_3d"`
	RiseFromLow30 float64  `db:"rise_from_low30" json:"rise_from_low30"`
	Ret4hBefore   float64  `db:"ret_4h_before" json:"ret_4h_before"`
	EMACross      bool     `db:"ema_cross" json:"ema_cross"`
	Ret4h         *float64 `db:"ret_4h" json:"ret_4h"`
	Ret24h        *float64 `db:"ret_24h" json:"ret_24h"`
	FirstTouch5   *int     `db:"first_touch_5" json:"first_touch_5"`
}

// fillOutcomes 给 5 天内还没补全的记录补 4h/24h 表现（15m 缓存约 5 天）。
func (s *PotentialScanner) fillOutcomes(ctx context.Context, nowMs int64) error {
	var picks []PotentialPick
	if err := s.store.SelectContext(ctx, &picks, `SELECT * FROM potential_picks
WHERE ret_24h IS NULL AND entered_ms <= ? AND entered_ms > ?`, nowMs-4*3600*1000, nowMs-5*24*3600*1000); err != nil {
		return err
	}
	for _, p := range picks {
		m15, err := s.source.Klines(ctx, "swap", p.Symbol, "15m", 499)
		if err != nil {
			continue
		}
		o := potentialOutcome(bollPumpClosedBarsBefore(m15, nowMs), p.EnteredMs, p.EntryPrice, nowMs)
		if o.Ret4h == nil && o.Ret24h == nil {
			continue
		}
		if _, err := s.store.ExecContext(ctx, `UPDATE potential_picks SET ret_4h = COALESCE(?, ret_4h), ret_24h = ?, first_touch_5 = ? WHERE id = ?`,
			o.Ret4h, o.Ret24h, o.FirstTouch5, p.ID); err != nil {
			return err
		}
	}
	return nil
}

type PotentialStats struct {
	List       string  `json:"list"`
	Done       int     `json:"done"`         // 已满 24h 的记录数
	Up4hRatio  float64 `json:"up_4h_ratio"`  // 4h 后上涨比例
	Median24h  float64 `json:"median_24h"`   // 24h 收益中位数
	TouchUp5   float64 `json:"touch_up_5"`   // 先涨 5% 的比例
	TouchDown5 float64 `json:"touch_down_5"` // 先跌 5% 的比例
	SinceMs    int64   `json:"since_ms"`
}

// GetPotentialHistory 最近的上榜记录（新的在前）和两个名单的实盘统计。
func GetPotentialHistory(ctx context.Context, store *sqlite.Store, limit int) ([]PotentialPick, []PotentialStats, error) {
	var picks []PotentialPick
	if err := store.SelectContext(ctx, &picks, `SELECT * FROM potential_picks ORDER BY entered_ms DESC`); err != nil {
		return nil, nil, err
	}
	stats := []PotentialStats{summarizePotentialPicks(PotentialListWatch, picks), summarizePotentialPicks(PotentialListAvoid, picks)}
	if limit > 0 && len(picks) > limit {
		picks = picks[:limit]
	}
	return picks, stats, nil
}

func summarizePotentialPicks(list string, picks []PotentialPick) PotentialStats {
	st := PotentialStats{List: list}
	var rets []float64
	up4, n4, up5, down5 := 0, 0, 0, 0
	for _, p := range picks {
		if p.List != list {
			continue
		}
		if st.SinceMs == 0 || p.EnteredMs < st.SinceMs {
			st.SinceMs = p.EnteredMs
		}
		if p.Ret4h != nil {
			n4++
			if *p.Ret4h > 0 {
				up4++
			}
		}
		if p.Ret24h == nil || p.FirstTouch5 == nil {
			continue
		}
		rets = append(rets, *p.Ret24h)
		switch *p.FirstTouch5 {
		case 1:
			up5++
		case -1:
			down5++
		}
	}
	st.Done = len(rets)
	if n4 > 0 {
		st.Up4hRatio = float64(up4) / float64(n4)
	}
	if st.Done > 0 {
		sort.Float64s(rets)
		st.Median24h = rets[len(rets)/2]
		if len(rets)%2 == 0 {
			st.Median24h = (rets[len(rets)/2-1] + rets[len(rets)/2]) / 2
		}
		st.TouchUp5 = float64(up5) / float64(st.Done)
		st.TouchDown5 = float64(down5) / float64(st.Done)
	}
	return st
}
