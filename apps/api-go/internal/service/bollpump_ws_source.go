package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bollPumpFuturesMarketStreamURL = "wss://fstream.binance.com/market/stream?streams="
	bollPumpWSReadTimeout          = 12 * time.Minute
	bollPumpWSMinReconnectDelay    = 2 * time.Second
	bollPumpWSMaxReconnectDelay    = 30 * time.Second
	bollPumpOneMinuteMs            = int64(60 * 1000)
)

type BollPumpLiveKlineSourceConfig struct {
	Market          string
	SymbolLimit     int
	Intervals       []string
	BootstrapLimit  int
	BootstrapRPS    int
	StreamChunkSize int
}

type BollPumpLiveKlineSource struct {
	base  BollPumpSource
	cfg   BollPumpLiveKlineSourceConfig
	cache *bollPumpKlineCache

	startOnce sync.Once
	symbolMu  sync.RWMutex
	symbols   []string

	refreshMu   sync.Mutex
	lastRefresh map[string]time.Time // 缓存过期时 REST 补拉的时间，每个币每个周期最多 5 分钟一次
}

// bollPumpRefreshBackoff 缓存过期时 REST 补拉的最短间隔。原来没有间隔：WS 断线或聚合缺根时，
// 每个扫描器每次读 K 线都会请求币安，几个扫描器叠加容易被限流封 IP。
const bollPumpRefreshBackoff = 5 * time.Minute

func NewBollPumpLiveKlineSource(base BollPumpSource, cfg BollPumpLiveKlineSourceConfig) *BollPumpLiveKlineSource {
	cfg.Market = normalizeBollPumpMarket(cfg.Market)
	if cfg.Market == "" {
		cfg.Market = "swap"
	}
	if cfg.SymbolLimit <= 0 {
		cfg.SymbolLimit = 1000
	}
	if cfg.BootstrapLimit < 80 {
		cfg.BootstrapLimit = 240
	}
	if cfg.BootstrapRPS <= 0 {
		cfg.BootstrapRPS = 8
	}
	if cfg.StreamChunkSize <= 0 || cfg.StreamChunkSize > 1024 {
		cfg.StreamChunkSize = 200
	}
	cfg.Intervals = bollPumpLiveIntervals(cfg.Intervals)
	return &BollPumpLiveKlineSource{
		base:  base,
		cfg:   cfg,
		cache: newBollPumpKlineCache(),
	}
}

func NewBollPumpLiveKlineSourceConfig(cfg BollPumpConfig, bootstrapLimit, bootstrapRPS, chunkSize int) BollPumpLiveKlineSourceConfig {
	cfg = NormalizeBollPumpConfig(cfg)
	return BollPumpLiveKlineSourceConfig{
		Market:          cfg.Market,
		SymbolLimit:     cfg.SymbolLimit,
		Intervals:       bollPumpLiveIntervals(append(append([]string{}, cfg.Timeframes...), cfg.MinimumTrendTimeframe, "4h")),
		BootstrapLimit:  bootstrapLimit,
		BootstrapRPS:    bootstrapRPS,
		StreamChunkSize: chunkSize,
	}
}

func (s *BollPumpLiveKlineSource) Start(ctx context.Context, stopCh <-chan struct{}) {
	if s == nil || s.base == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.run(ctx, stopCh)
	})
}

func (s *BollPumpLiveKlineSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	if cached := s.cachedSymbols(limit); len(cached) > 0 {
		return cached, nil
	}
	symbols, err := s.base.Symbols(ctx, market, limit)
	if err != nil {
		return nil, err
	}
	s.setSymbols(symbols)
	return s.cachedSymbols(limit), nil
}

func (s *BollPumpLiveKlineSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	if s == nil || s.cache == nil {
		return nil, fmt.Errorf("boll pump websocket source is nil")
	}
	bars := s.cache.Klines(market, symbol, timeframe, limit)
	minBars := bollPumpKlineMinBars(limit)
	if len(bars) < minBars {
		return nil, &bollPumpKlineCacheWarmingError{
			Symbol:    strings.ToUpper(symbol),
			Timeframe: timeframe,
			Have:      len(bars),
			Want:      minBars,
		}
	}
	if bollPumpKlineCacheFresh(bars, timeframe, time.Now().UnixMilli()) {
		return bars, nil
	}
	if !s.claimRefresh(bollPumpKlineCacheKey(market, symbol, timeframe)) {
		return nil, &bollPumpKlineCacheWarmingError{Symbol: strings.ToUpper(symbol), Timeframe: timeframe, Have: len(bars), Want: minBars}
	}
	return s.refreshCachedKlines(ctx, market, symbol, timeframe, limit, minBars, len(bars))
}

// 1m、3m 不需要 499 根：BOLL 泵盘扫描取 200 根，1m 聚合 4h 需要 244 根，留 260 根。
// 5m 保留 499 根（补 24h 信号表现要 40 小时的 5m），15m 以上保留 499 根（EMA200）。
const bollPumpShortIntervalLimit = 260

// cacheLimit 每个周期缓存保留的根数。
func (s *BollPumpLiveKlineSource) cacheLimit(timeframe string) int {
	if (timeframe == "1m" || timeframe == "3m") && s.cfg.BootstrapLimit > bollPumpShortIntervalLimit {
		return bollPumpShortIntervalLimit
	}
	return s.cfg.BootstrapLimit
}

// claimRefresh 距上次补拉不到 bollPumpRefreshBackoff 时返回 false（失败的补拉也算一次）。
func (s *BollPumpLiveKlineSource) claimRefresh(key string) bool {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.lastRefresh == nil {
		s.lastRefresh = map[string]time.Time{}
	}
	if time.Since(s.lastRefresh[key]) < bollPumpRefreshBackoff {
		return false
	}
	s.lastRefresh[key] = time.Now()
	return true
}

func (s *BollPumpLiveKlineSource) refreshCachedKlines(ctx context.Context, market, symbol, timeframe string, limit, minBars, cachedBars int) ([]BollPumpBar, error) {
	if s == nil || s.base == nil || s.cache == nil {
		return nil, &bollPumpKlineCacheWarmingError{
			Symbol:    strings.ToUpper(symbol),
			Timeframe: timeframe,
			Have:      cachedBars,
			Want:      minBars,
		}
	}
	fetchLimit := limit
	if fetchLimit < minBars {
		fetchLimit = minBars
	}
	if fetchLimit <= 0 {
		fetchLimit = s.cacheLimit(timeframe)
	}
	freshBars, err := s.base.Klines(ctx, market, symbol, timeframe, fetchLimit)
	if err != nil {
		return nil, &bollPumpKlineCacheWarmingError{
			Symbol:    strings.ToUpper(symbol),
			Timeframe: timeframe,
			Have:      cachedBars,
			Want:      minBars,
		}
	}
	cacheLimit := s.cacheLimit(timeframe)
	if cacheLimit < fetchLimit {
		cacheLimit = fetchLimit
	}
	s.cache.Seed(market, symbol, timeframe, bollPumpMarkClosedByTime(freshBars), cacheLimit)
	bars := s.cache.Klines(market, symbol, timeframe, limit)
	if len(bars) < minBars || !bollPumpKlineCacheFresh(bars, timeframe, time.Now().UnixMilli()) {
		return nil, &bollPumpKlineCacheWarmingError{
			Symbol:    strings.ToUpper(symbol),
			Timeframe: timeframe,
			Have:      len(bars),
			Want:      minBars,
		}
	}
	return bars, nil
}

func (s *BollPumpLiveKlineSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	return s.base.QuoteVolume24h(ctx, market, symbol)
}

func (s *BollPumpLiveKlineSource) run(ctx context.Context, stopCh <-chan struct{}) {
	if s.cfg.Market != "swap" {
		log.Printf("boll_pump_ws: market=%s skipped", s.cfg.Market)
		return
	}
	symbols := s.loadSymbols(ctx, stopCh)
	if len(symbols) == 0 {
		return
	}
	s.setSymbols(symbols)
	streams := s.streamNames(symbols)
	chunks := chunkStrings(streams, s.cfg.StreamChunkSize)
	log.Printf("boll_pump_ws: starting symbols=%d stream_interval=1m aggregate_intervals=%d streams=%d connections=%d bootstrap_limit=%d bootstrap_rps=%d",
		len(symbols), len(s.cfg.Intervals), len(streams), len(chunks), s.cfg.BootstrapLimit, s.cfg.BootstrapRPS)
	for i, chunk := range chunks {
		go s.runStreamLoop(ctx, stopCh, i+1, chunk)
	}
	go s.bootstrapLoop(ctx, stopCh, symbols)
}

func (s *BollPumpLiveKlineSource) loadSymbols(ctx context.Context, stopCh <-chan struct{}) []string {
	delay := bollPumpWSMinReconnectDelay
	for {
		if bollPumpStopped(ctx, stopCh) {
			return nil
		}
		symbols, err := s.base.Symbols(ctx, s.cfg.Market, s.cfg.SymbolLimit)
		if err == nil && len(symbols) > 0 {
			return symbols
		}
		if err != nil {
			log.Printf("boll_pump_ws: symbol load error: %v", err)
		}
		if !bollPumpSleep(ctx, stopCh, delay) {
			return nil
		}
		if delay < bollPumpWSMaxReconnectDelay {
			delay *= 2
		}
	}
}

func (s *BollPumpLiveKlineSource) setSymbols(symbols []string) {
	out := make([]string, 0, len(symbols))
	seen := map[string]bool{}
	for _, symbol := range symbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if seen[symbol] || !bollPumpTradableUSDTSymbol(symbol) {
			continue
		}
		seen[symbol] = true
		out = append(out, symbol)
	}
	s.symbolMu.Lock()
	s.symbols = out
	s.symbolMu.Unlock()
}

func (s *BollPumpLiveKlineSource) cachedSymbols(limit int) []string {
	s.symbolMu.RLock()
	defer s.symbolMu.RUnlock()
	if len(s.symbols) == 0 {
		return nil
	}
	out := append([]string{}, s.symbols...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *BollPumpLiveKlineSource) streamNames(symbols []string) []string {
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if !bollPumpTradableUSDTSymbol(symbol) {
			continue
		}
		lower := strings.ToLower(symbol)
		streams = append(streams, lower+"@kline_1m")
	}
	return streams
}

func (s *BollPumpLiveKlineSource) runStreamLoop(ctx context.Context, stopCh <-chan struct{}, index int, streams []string) {
	delay := bollPumpWSMinReconnectDelay
	for {
		if bollPumpStopped(ctx, stopCh) {
			return
		}
		err := s.readStream(ctx, stopCh, index, streams)
		if bollPumpStopped(ctx, stopCh) {
			return
		}
		if err != nil {
			log.Printf("boll_pump_ws: connection=%d streams=%d error=%v", index, len(streams), err)
		}
		if !bollPumpSleep(ctx, stopCh, delay) {
			return
		}
		if delay < bollPumpWSMaxReconnectDelay {
			delay *= 2
		}
	}
}

func (s *BollPumpLiveKlineSource) readStream(ctx context.Context, stopCh <-chan struct{}, index int, streams []string) error {
	endpoint := bollPumpFuturesMarketStreamURL + strings.Join(streams, "/")
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial status %d: %w", resp.StatusCode, err)
		}
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(bollPumpWSReadTimeout))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(bollPumpWSReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	conn.SetPongHandler(func(appData string) error {
		return conn.SetReadDeadline(time.Now().Add(bollPumpWSReadTimeout))
	})

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopCh:
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	log.Printf("boll_pump_ws: connected connection=%d streams=%d", index, len(streams))
	// 每 10 分钟记一次本连接收到的消息数和处理掉的收盘 K 线数。收盘数为 0 说明解析出了问题
	// （曾经因为 JSON 字段大小写冲突，所有收盘事件都被丢掉、缓存全靠 REST 补拉，导致被币安封 IP）。
	var stMsgs, stCloses int
	stAt := time.Now()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		stMsgs++
		if s.handleWSMessage(msg) > 0 {
			stCloses++
		}
		if time.Since(stAt) >= 10*time.Minute {
			if stCloses == 0 && stMsgs > 0 {
				log.Printf("boll_pump_ws: WARNING connection=%d received %d messages but no closed klines in 10m", index, stMsgs)
			} else {
				log.Printf("boll_pump_ws: connection=%d msgs=%d closed_klines=%d (10m)", index, stMsgs, stCloses)
			}
			stMsgs, stCloses, stAt = 0, 0, time.Now()
		}
	}
}

// handleWSMessage 处理一条 WS 消息；是收盘 K 线时返回它的收盘时间（毫秒），否则返回 0。
func (s *BollPumpLiveKlineSource) handleWSMessage(msg []byte) int64 {
	var combined struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &combined); err == nil && len(combined.Data) > 0 {
		msg = combined.Data
	}
	var ev bollPumpWSKlineEvent
	if err := json.Unmarshal(msg, &ev); err != nil || ev.EventType != "kline" || !ev.Kline.Closed {
		return 0
	}
	bar, ok := bollPumpBarFromWSKline(ev)
	if !ok {
		return 0
	}
	s.cache.Upsert(s.cfg.Market, ev.Symbol, ev.Kline.Interval, bar, s.cacheLimit(ev.Kline.Interval))
	if ev.Kline.Interval == "1m" {
		s.aggregateFromOneMinute(ev.Symbol, bar)
	}
	return bar.CloseTimeMs
}

func (s *BollPumpLiveKlineSource) aggregateFromOneMinute(symbol string, bar BollPumpBar) {
	if s == nil || s.cache == nil || !bar.Closed {
		return
	}
	for _, tf := range s.cfg.Intervals {
		if tf == "1m" {
			continue
		}
		tfMs := bollPumpWSIntervalMs(tf)
		if tfMs <= bollPumpOneMinuteMs {
			continue
		}
		bucketStart := (bar.OpenTimeMs / tfMs) * tfMs
		bucketEnd := bucketStart + tfMs - 1
		if bar.CloseTimeMs < bucketEnd {
			continue
		}
		parts := s.cache.Klines(s.cfg.Market, symbol, "1m", int(tfMs/bollPumpOneMinuteMs)+4)
		agg, ok := aggregateBollPumpOneMinuteBars(parts, bucketStart, tfMs)
		if !ok {
			continue
		}
		s.cache.Upsert(s.cfg.Market, symbol, tf, agg, s.cacheLimit(tf))
	}
}

func (s *BollPumpLiveKlineSource) bootstrapLoop(ctx context.Context, stopCh <-chan struct{}, symbols []string) {
	rps := s.cfg.BootstrapRPS
	if rps <= 0 {
		rps = 8
	}
	interval := time.Second / time.Duration(rps)
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	total := len(symbols) * len(s.cfg.Intervals)
	done := 0
	errors := 0
	type job struct{ symbol, tf string }
	var failed []job
	for _, symbol := range symbols {
		for _, tf := range s.cfg.Intervals {
			if bollPumpStopped(ctx, stopCh) {
				return
			}
			if s.cache.HasAtLeast(s.cfg.Market, symbol, tf, s.cacheLimit(tf)/2) {
				done++
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
			}
			bars, err := s.base.Klines(ctx, s.cfg.Market, symbol, tf, s.cacheLimit(tf))
			if err != nil {
				errors++
				failed = append(failed, job{symbol, tf})
				if errors <= 10 || errors%100 == 0 {
					log.Printf("boll_pump_ws: bootstrap %s %s error=%v", symbol, tf, err)
				}
				continue
			}
			s.cache.Seed(s.cfg.Market, symbol, tf, bollPumpMarkClosedByTime(bars), s.cacheLimit(tf))
			done++
			if done%250 == 0 || done == total {
				log.Printf("boll_pump_ws: bootstrap progress=%d/%d errors=%d", done, total, errors)
			}
		}
	}
	log.Printf("boll_pump_ws: bootstrap complete jobs=%d errors=%d", done, errors)

	// 失败的（例如预热时正好被币安限流）每 5 分钟重试一轮，最多 12 轮；WS 已经攒够的跳过。
	for round := 1; len(failed) > 0 && round <= 12; round++ {
		if !bollPumpSleep(ctx, stopCh, 5*time.Minute) {
			return
		}
		var still []job
		for _, j := range failed {
			if s.cache.HasAtLeast(s.cfg.Market, j.symbol, j.tf, s.cacheLimit(j.tf)/2) {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
			}
			bars, err := s.base.Klines(ctx, s.cfg.Market, j.symbol, j.tf, s.cacheLimit(j.tf))
			if err != nil {
				still = append(still, j)
				continue
			}
			s.cache.Seed(s.cfg.Market, j.symbol, j.tf, bollPumpMarkClosedByTime(bars), s.cacheLimit(j.tf))
		}
		log.Printf("boll_pump_ws: bootstrap retry round=%d retried=%d still_failed=%d", round, len(failed), len(still))
		failed = still
	}
}

type bollPumpWSKlineEvent struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Kline     struct {
		OpenTime      int64  `json:"t"`
		CloseTime     int64  `json:"T"`
		Symbol        string `json:"s"`
		Interval      string `json:"i"`
		Open          string `json:"o"`
		Close         string `json:"c"`
		High          string `json:"h"`
		Low           string `json:"l"`
		Volume        string `json:"v"`
		QuoteVolume   string `json:"q"`
		TakerBuyQuote string `json:"Q"`
		Closed        bool   `json:"x"`
		// encoding/json 匹配字段不区分大小写：币安推送里的 "L"（末笔成交 ID，数字）会落到 Low（"l"）上导致整条解析失败，
		// "V" 会覆盖 Volume（"v"）。显式声明这些字段，让它们各归各位。
		LastTradeID    int64  `json:"L"`
		TakerBuyVolume string `json:"V"`
	} `json:"k"`
}

func bollPumpBarFromWSKline(ev bollPumpWSKlineEvent) (BollPumpBar, bool) {
	open, ok := parseBollPumpWSFloat(ev.Kline.Open)
	if !ok {
		return BollPumpBar{}, false
	}
	high, ok := parseBollPumpWSFloat(ev.Kline.High)
	if !ok {
		return BollPumpBar{}, false
	}
	low, ok := parseBollPumpWSFloat(ev.Kline.Low)
	if !ok {
		return BollPumpBar{}, false
	}
	closePrice, ok := parseBollPumpWSFloat(ev.Kline.Close)
	if !ok {
		return BollPumpBar{}, false
	}
	volume, _ := parseBollPumpWSFloat(ev.Kline.Volume)
	quoteVolume, _ := parseBollPumpWSFloat(ev.Kline.QuoteVolume)
	takerBuyQuote, _ := parseBollPumpWSFloat(ev.Kline.TakerBuyQuote)
	return BollPumpBar{
		OpenTimeMs:    ev.Kline.OpenTime,
		CloseTimeMs:   ev.Kline.CloseTime,
		Open:          open,
		High:          high,
		Low:           low,
		Close:         closePrice,
		Volume:        volume,
		QuoteVolume:   quoteVolume,
		TakerBuyQuote: takerBuyQuote,
		Closed:        ev.Kline.Closed,
	}, true
}

func parseBollPumpWSFloat(v string) (float64, bool) {
	out, err := strconv.ParseFloat(v, 64)
	return out, err == nil
}

func bollPumpMarkClosedByTime(bars []BollPumpBar) []BollPumpBar {
	nowMs := time.Now().UnixMilli()
	out := append([]BollPumpBar{}, bars...)
	for i := range out {
		out[i].Closed = out[i].CloseTimeMs > 0 && out[i].CloseTimeMs <= nowMs
	}
	return out
}

func bollPumpKlineMinBars(limit int) int {
	if limit > 0 && limit <= 80 {
		return limit
	}
	return 80
}

func bollPumpKlineCacheFresh(bars []BollPumpBar, timeframe string, nowMs int64) bool {
	tfMs := bollPumpWSIntervalMs(timeframe)
	if len(bars) == 0 || tfMs <= 0 || nowMs <= 0 {
		return false
	}
	latestClosedMs := int64(0)
	for i := len(bars) - 1; i >= 0; i-- {
		closeMs := bars[i].CloseTimeMs
		if closeMs <= 0 && bars[i].OpenTimeMs > 0 {
			closeMs = bars[i].OpenTimeMs + tfMs - 1
		}
		if bars[i].Closed && closeMs > 0 && closeMs <= nowMs {
			latestClosedMs = closeMs
			break
		}
	}
	if latestClosedMs == 0 {
		return false
	}
	return nowMs-latestClosedMs <= bollPumpKlineFreshnessThresholdMs(timeframe)
}

func bollPumpKlineFreshnessThresholdMs(timeframe string) int64 {
	tfMs := bollPumpWSIntervalMs(timeframe)
	if tfMs <= 0 {
		return 0
	}
	threshold := tfMs*2 + int64(30*time.Second/time.Millisecond)
	minThreshold := int64(3 * time.Minute / time.Millisecond)
	if threshold < minThreshold {
		return minThreshold
	}
	return threshold
}

func bollPumpLiveIntervals(in []string) []string {
	defaults := []string{"1m", "3m", "5m", "15m", "30m", "1h", "4h"}
	if len(in) == 0 {
		in = defaults
	}
	allowed := map[string]bool{"1m": true, "3m": true, "5m": true, "15m": true, "30m": true, "1h": true, "4h": true}
	seen := map[string]bool{}
	out := make([]string, 0, len(in)+len(defaults))
	for _, tf := range append(append([]string{}, in...), "4h") {
		tf = strings.TrimSpace(tf)
		if allowed[tf] && !seen[tf] {
			seen[tf] = true
			out = append(out, tf)
		}
	}
	return out
}

func bollPumpWSIntervalMs(interval string) int64 {
	interval = strings.TrimSpace(interval)
	if strings.HasSuffix(interval, "m") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(interval, "m"), 10, 64)
		return n * bollPumpOneMinuteMs
	}
	if strings.HasSuffix(interval, "h") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(interval, "h"), 10, 64)
		return n * 60 * bollPumpOneMinuteMs
	}
	return 0
}

func aggregateBollPumpOneMinuteBars(bars []BollPumpBar, bucketStart, tfMs int64) (BollPumpBar, bool) {
	if tfMs <= bollPumpOneMinuteMs {
		return BollPumpBar{}, false
	}
	need := int(tfMs / bollPumpOneMinuteMs)
	window := make([]BollPumpBar, 0, need)
	bucketEnd := bucketStart + tfMs - 1
	for _, bar := range bars {
		if !bar.Closed || bar.OpenTimeMs < bucketStart || bar.OpenTimeMs > bucketEnd {
			continue
		}
		window = append(window, bar)
	}
	sort.Slice(window, func(i, j int) bool { return window[i].OpenTimeMs < window[j].OpenTimeMs })
	if len(window) != need {
		return BollPumpBar{}, false
	}
	for i, bar := range window {
		wantOpen := bucketStart + int64(i)*bollPumpOneMinuteMs
		if bar.OpenTimeMs != wantOpen {
			return BollPumpBar{}, false
		}
	}
	out := BollPumpBar{
		OpenTimeMs:  bucketStart,
		CloseTimeMs: bucketEnd,
		Open:        window[0].Open,
		High:        window[0].High,
		Low:         window[0].Low,
		Close:       window[len(window)-1].Close,
		Closed:      true,
	}
	for _, bar := range window {
		if bar.High > out.High {
			out.High = bar.High
		}
		if bar.Low < out.Low {
			out.Low = bar.Low
		}
		out.Volume += bar.Volume
		out.QuoteVolume += bar.QuoteVolume
		out.TakerBuyQuote += bar.TakerBuyQuote
	}
	return out, true
}

func chunkStrings(in []string, size int) [][]string {
	if size <= 0 {
		size = 200
	}
	out := make([][]string, 0, (len(in)+size-1)/size)
	for start := 0; start < len(in); start += size {
		end := start + size
		if end > len(in) {
			end = len(in)
		}
		out = append(out, in[start:end])
	}
	return out
}

func bollPumpStopped(ctx context.Context, stopCh <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return true
	case <-stopCh:
		return true
	default:
		return false
	}
}

func bollPumpSleep(ctx context.Context, stopCh <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stopCh:
		return false
	case <-timer.C:
		return true
	}
}

type bollPumpKlineCache struct {
	mu   sync.RWMutex
	bars map[string][]BollPumpBar
}

func newBollPumpKlineCache() *bollPumpKlineCache {
	return &bollPumpKlineCache{bars: map[string][]BollPumpBar{}}
}

func (c *bollPumpKlineCache) Klines(market, symbol, timeframe string, limit int) []BollPumpBar {
	if c == nil {
		return nil
	}
	key := bollPumpKlineCacheKey(market, symbol, timeframe)
	c.mu.RLock()
	bars := append([]BollPumpBar{}, c.bars[key]...)
	c.mu.RUnlock()
	if limit > 0 && len(bars) > limit {
		bars = bars[len(bars)-limit:]
	}
	return bars
}

func (c *bollPumpKlineCache) HasAtLeast(market, symbol, timeframe string, n int) bool {
	if c == nil || n <= 0 {
		return false
	}
	key := bollPumpKlineCacheKey(market, symbol, timeframe)
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.bars[key]) >= n
}

func (c *bollPumpKlineCache) Seed(market, symbol, timeframe string, bars []BollPumpBar, limit int) {
	if c == nil || len(bars) == 0 {
		return
	}
	key := bollPumpKlineCacheKey(market, symbol, timeframe)
	c.mu.Lock()
	c.bars[key] = trimBollPumpBars(mergeBollPumpBars(c.bars[key], bars), limit)
	c.mu.Unlock()
}

func (c *bollPumpKlineCache) Upsert(market, symbol, timeframe string, bar BollPumpBar, limit int) {
	if c == nil || !bar.Closed || bar.OpenTimeMs <= 0 {
		return
	}
	key := bollPumpKlineCacheKey(market, symbol, timeframe)
	c.mu.Lock()
	bars := c.bars[key]
	replaced := false
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].OpenTimeMs == bar.OpenTimeMs {
			bars[i] = bar
			replaced = true
			break
		}
		if bars[i].OpenTimeMs < bar.OpenTimeMs {
			break
		}
	}
	if !replaced {
		bars = append(bars, bar)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTimeMs < bars[j].OpenTimeMs })
	c.bars[key] = trimBollPumpBars(bars, limit)
	c.mu.Unlock()
}

func bollPumpKlineCacheKey(market, symbol, timeframe string) string {
	return normalizeBollPumpMarket(market) + ":" + strings.ToUpper(strings.TrimSpace(symbol)) + ":" + strings.TrimSpace(timeframe)
}

func mergeBollPumpBars(a, b []BollPumpBar) []BollPumpBar {
	byOpen := make(map[int64]BollPumpBar, len(a)+len(b))
	for _, bar := range a {
		if bar.OpenTimeMs > 0 {
			byOpen[bar.OpenTimeMs] = bar
		}
	}
	for _, bar := range b {
		if bar.OpenTimeMs > 0 {
			byOpen[bar.OpenTimeMs] = bar
		}
	}
	keys := make([]int64, 0, len(byOpen))
	for open := range byOpen {
		keys = append(keys, open)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]BollPumpBar, 0, len(keys))
	for _, open := range keys {
		out = append(out, byOpen[open])
	}
	return out
}

func trimBollPumpBars(bars []BollPumpBar, limit int) []BollPumpBar {
	if limit <= 0 {
		limit = 240
	}
	if len(bars) <= limit {
		return bars
	}
	return append([]BollPumpBar{}, bars[len(bars)-limit:]...)
}

type bollPumpKlineCacheWarmingError struct {
	Symbol    string
	Timeframe string
	Have      int
	Want      int
}

func (e *bollPumpKlineCacheWarmingError) Error() string {
	return fmt.Sprintf("boll pump websocket cache warming: %s %s %d/%d", e.Symbol, e.Timeframe, e.Have, e.Want)
}

func bollPumpIsKlineCacheWarming(err error) bool {
	var target *bollPumpKlineCacheWarmingError
	return errors.As(err, &target)
}
