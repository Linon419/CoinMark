package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

type BollPumpSource interface {
	Symbols(ctx context.Context, market string, limit int) ([]string, error)
	Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error)
	QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error)
}

type BollPumpScanner struct {
	source      BollPumpSource
	store       *sqlite.Store
	cfg         BollPumpConfig
	symbolLimit int
}

type BollPumpScanResult struct {
	Timeframe      string
	SymbolsScanned int
	SignalsFound   int
	Errors         int
	StartedAtMs    int64
	FinishedAtMs   int64
}

func NewBollPumpScanner(source BollPumpSource, store *sqlite.Store, cfg BollPumpConfig) *BollPumpScanner {
	cfg = NormalizeBollPumpConfig(cfg)
	return &BollPumpScanner{source: source, store: store, cfg: cfg, symbolLimit: cfg.SymbolLimit}
}

func (s *BollPumpScanner) ScanTimeframe(ctx context.Context, timeframe string) BollPumpScanResult {
	result := BollPumpScanResult{Timeframe: timeframe, StartedAtMs: time.Now().UnixMilli()}
	defer func() { result.FinishedAtMs = time.Now().UnixMilli() }()
	if s == nil || s.source == nil {
		result.Errors++
		return result
	}
	s.refreshConfig(ctx)
	if !s.cfg.Enabled {
		return result
	}
	symbols, err := s.source.Symbols(ctx, normalizeBollPumpMarket(s.cfg.Market), s.symbolLimit)
	if err != nil {
		result.Errors++
		return result
	}
	symbols = s.mergeActiveStateSymbols(ctx, timeframe, symbols)
	limit := s.klineLimit()
	for _, symbol := range symbols {
		if !bollPumpTradableUSDTSymbol(symbol) {
			continue
		}
		select {
		case <-ctx.Done():
			result.Errors++
			return result
		default:
		}
		bars, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, timeframe, limit)
		if err != nil {
			if bollPumpIsKlineCacheWarming(err) {
				continue
			}
			result.Errors++
			continue
		}
		if len(bars) == 0 {
			continue
		}
		bars = bollPumpClosedBars(bars)
		if len(bars) == 0 {
			continue
		}
		quoteVolume, _ := s.source.QuoteVolume24h(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol)
		ind := ComputeBollPumpIndicators(bars, s.cfg.BollPeriod, s.cfg.BollStdDev, s.cfg.ATRPeriod)
		state := s.loadRuntimeState(ctx, symbol, timeframe)
		latest := bars[len(bars)-1]
		latestInd := ind[len(ind)-1]
		if bollPumpActiveLowerBandBreakdown(state, latest, latestInd) {
			bollPumpInvalidateRuntimeState(&state)
			s.persistState(ctx, state)
			result.SymbolsScanned++
			continue
		}
		var trendGate bollPumpMinimumTrendGateResult
		trendChecked := false
		checkTrend := func() bollPumpMinimumTrendGateResult {
			if !trendChecked {
				trendGate = s.minimumTrendGate(ctx, symbol, timeframe, bars)
				trendChecked = true
			}
			return trendGate
		}
		if bollPumpStateNeedsMinimumTrend(state) {
			gate := checkTrend()
			if gate.Unavailable {
				result.SymbolsScanned++
				continue
			}
			if !gate.Pass {
				s.invalidateStateForMinimumTrend(ctx, &state)
				s.persistState(ctx, state)
				result.SymbolsScanned++
				continue
			}
		}
		latestOpen := bars[len(bars)-1].OpenTimeMs
		var resistanceBreakout bollPumpResistanceBreakoutResult
		resistanceChecked := false
		stopReplay := false
		for i := 0; i < len(bars); i++ {
			out := AdvanceBollPumpState(&state, bars[:i+1], ind[:i+1], quoteVolume, s.cfg)
			for _, sig := range out.Signals {
				if s.store != nil && sig.CandleStartMs != latestOpen {
					continue
				}
				gate := checkTrend()
				if gate.Unavailable {
					stopReplay = true
					continue
				}
				if !gate.Pass {
					s.invalidateStateForMinimumTrend(ctx, &state)
					stopReplay = true
					continue
				}
				if !resistanceChecked {
					resistanceBreakout = s.fourHourResistanceBreakout(ctx, symbol)
					resistanceChecked = true
				}
				sig = applyBollPumpMinimumTrendGate(sig, gate)
				sig = applyBollPumpResistanceBreakout(sig, resistanceBreakout)
				if resistanceBreakout.Triggered {
					state.CurrentScore = sig.Score
					if sig.SignalLevel == string(BollPumpLevelWatch) {
						state.WatchScore = sig.Score
					}
				}
				result.SignalsFound++
				s.persistSignal(ctx, sig)
			}
			if stopReplay {
				break
			}
		}
		if !stopReplay && bollPumpStateNeedsMinimumTrend(state) {
			gate := checkTrend()
			if !gate.Unavailable && !gate.Pass {
				s.invalidateStateForMinimumTrend(ctx, &state)
			}
		}
		s.persistState(ctx, state)
		result.SymbolsScanned++
	}
	return result
}

func (s *BollPumpScanner) ScanKeyK4H(ctx context.Context) BollPumpScanResult {
	result := BollPumpScanResult{Timeframe: "4h", StartedAtMs: time.Now().UnixMilli()}
	defer func() { result.FinishedAtMs = time.Now().UnixMilli() }()
	if s == nil || s.source == nil {
		result.Errors++
		return result
	}
	s.refreshConfig(ctx)
	if !s.cfg.Enabled || !s.cfg.KeyK4HEnabled {
		return result
	}
	symbols, err := s.source.Symbols(ctx, normalizeBollPumpMarket(s.cfg.Market), s.symbolLimit)
	if err != nil {
		result.Errors++
		return result
	}
	limit := s.keyK4HKlineLimit()
	nowMs := time.Now().UnixMilli()
	for _, symbol := range symbols {
		if !bollPumpTradableUSDTSymbol(symbol) {
			continue
		}
		select {
		case <-ctx.Done():
			result.Errors++
			return result
		default:
		}
		bars, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, "4h", limit)
		if err != nil {
			if bollPumpIsKlineCacheWarming(err) {
				continue
			}
			result.Errors++
			continue
		}
		if len(bars) == 0 {
			continue
		}
		bars = bollPumpClosedBarsBefore(bars, nowMs)
		if len(bars) == 0 {
			continue
		}
		quoteVolume, _ := s.source.QuoteVolume24h(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol)
		ind := ComputeBollPumpIndicators(bars, s.cfg.BollPeriod, s.cfg.BollStdDev, s.cfg.ATRPeriod)
		out := EvaluateBollPumpKeyK4H(normalizeBollPumpMarket(s.cfg.Market), symbol, bars, ind, quoteVolume, s.cfg)
		if out.Triggered {
			if exists, err := s.signalExists(ctx, out.Signal); err != nil {
				result.Errors++
				result.SymbolsScanned++
				continue
			} else if exists {
				result.SymbolsScanned++
				continue
			}
			result.SignalsFound++
			s.persistSignal(ctx, out.Signal)
		}
		result.SymbolsScanned++
	}
	return result
}

func bollPumpStateNeedsMinimumTrend(state BollPumpRuntimeState) bool {
	return bollPumpStatusIsActive(state.Status) && (state.CurrentScore > 0 || state.WatchScore > 0)
}

func (s *BollPumpScanner) mergeActiveStateSymbols(ctx context.Context, timeframe string, base []string) []string {
	out := make([]string, 0, len(base))
	seen := map[string]bool{}
	add := func(symbol string) {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if seen[symbol] || !bollPumpTradableUSDTSymbol(symbol) {
			return
		}
		seen[symbol] = true
		out = append(out, symbol)
	}
	for _, symbol := range base {
		add(symbol)
	}
	if s == nil || s.store == nil {
		return out
	}
	states, err := ListBollPumpStates(ctx, s.store, BollPumpStateFilter{
		Market:    s.cfg.Market,
		Timeframe: timeframe,
		Limit:     1000,
	})
	if err != nil {
		return out
	}
	for _, st := range states {
		if bollPumpStatusIsActive(st.Status) {
			add(st.Symbol)
		}
	}
	return out
}

func (s *BollPumpScanner) minimumTrendGate(ctx context.Context, symbol, timeframe string, bars []BollPumpBar) bollPumpMinimumTrendGateResult {
	if s == nil || s.source == nil {
		return bollPumpMinimumTrendGateResult{Reason: "15m trend source unavailable"}
	}
	tf := NormalizeBollPumpConfig(s.cfg).MinimumTrendTimeframe
	trendBars := bars
	if timeframe != tf {
		limit := s.cfg.MinimumTrendCheckCandles + s.cfg.BollPeriod + 10
		if limit < 60 {
			limit = 60
		}
		loaded, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, tf, limit)
		if err != nil {
			if bollPumpIsKlineCacheWarming(err) {
				return bollPumpMinimumTrendGateResult{Reason: fmt.Sprintf("%s trend warming", tf), Unavailable: true}
			}
			return bollPumpMinimumTrendGateResult{Reason: fmt.Sprintf("%s trend unavailable", tf)}
		}
		if len(loaded) == 0 {
			return bollPumpMinimumTrendGateResult{Reason: fmt.Sprintf("%s trend unavailable", tf)}
		}
		trendBars = bollPumpClosedBars(loaded)
	}
	return bollPumpMinimumTrendGate(trendBars, s.cfg)
}

func (s *BollPumpScanner) invalidateStateForMinimumTrend(ctx context.Context, state *BollPumpRuntimeState) {
	if state == nil || state.Status == "" || state.Status == string(BollPumpStatusIdle) {
		return
	}
	bollPumpInvalidateRuntimeState(state)
	s.persistState(ctx, *state)
}

func applyBollPumpMinimumTrendGate(sig model.BollPumpSignal, gate bollPumpMinimumTrendGateResult) model.BollPumpSignal {
	if !gate.Pass || strings.TrimSpace(gate.Reason) == "" {
		return sig
	}
	if strings.TrimSpace(sig.Reason) == "" {
		sig.Reason = gate.Reason
	} else if !strings.Contains(sig.Reason, gate.Reason) {
		sig.Reason += ", " + gate.Reason
	}
	return sig
}

func (s *BollPumpScanner) fourHourResistanceBreakout(ctx context.Context, symbol string) bollPumpResistanceBreakoutResult {
	if s == nil || s.source == nil || s.cfg.Resistance4HBreakoutBonus <= 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	limit := s.cfg.Resistance4HLookback + s.cfg.ATRPeriod + 2*s.cfg.Resistance4HSwingSpan + 10
	if limit < 100 {
		limit = 100
	}
	bars, err := s.source.Klines(ctx, normalizeBollPumpMarket(s.cfg.Market), symbol, "4h", limit)
	if err != nil || len(bars) == 0 {
		return bollPumpResistanceBreakoutResult{}
	}
	nowMs := time.Now().UnixMilli()
	closed := make([]BollPumpBar, 0, len(bars))
	for _, b := range bars {
		if b.Closed && (b.CloseTimeMs == 0 || b.CloseTimeMs <= nowMs) {
			closed = append(closed, b)
		}
	}
	return bollPumpFourHourResistanceBreakout(closed, s.cfg)
}

func applyBollPumpResistanceBreakout(sig model.BollPumpSignal, breakout bollPumpResistanceBreakoutResult) model.BollPumpSignal {
	if !breakout.Triggered {
		return sig
	}
	sig.Score = bollPumpScoreFloor(sig.Score + breakout.Bonus)
	sig.PriorityScore = bollPumpScoreFloor(sig.PriorityScore + breakout.Bonus)
	if strings.TrimSpace(sig.Reason) == "" {
		sig.Reason = breakout.Reason
	} else {
		sig.Reason += ", " + breakout.Reason
	}
	return sig
}

func (s *BollPumpScanner) Run(ctx context.Context, stopCh <-chan struct{}) {
	if s == nil || s.source == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastRun := map[string]int64{}
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := s.refreshConfig(ctx)
			if !cfg.Enabled {
				continue
			}
			for _, tf := range cfg.Timeframes {
				closed := lastClosedStartForTimeframe(time.Now().UnixMilli(), tf)
				if closed <= 0 || lastRun[tf] == closed {
					continue
				}
				lastRun[tf] = closed
				timeoutSec := cfg.ScanTimeoutSec
				if timeoutSec <= 0 {
					timeoutSec = 45
				}
				scanCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
				result := s.ScanTimeframe(scanCtx, tf)
				cancel()
				if result.Errors > 0 || result.SignalsFound > 0 {
					log.Printf("boll_pump: tf=%s scanned=%d signals=%d errors=%d", tf, result.SymbolsScanned, result.SignalsFound, result.Errors)
				}
			}
			if cfg.KeyK4HEnabled {
				key := "key_k_4h"
				closed := lastClosedStartForTimeframe(time.Now().UnixMilli(), "4h")
				if closed > 0 && lastRun[key] != closed {
					lastRun[key] = closed
					timeoutSec := cfg.ScanTimeoutSec
					if timeoutSec <= 0 {
						timeoutSec = 45
					}
					scanCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
					result := s.ScanKeyK4H(scanCtx)
					cancel()
					if result.Errors > 0 || result.SignalsFound > 0 {
						log.Printf("boll_pump_key_k_4h: scanned=%d signals=%d errors=%d", result.SymbolsScanned, result.SignalsFound, result.Errors)
					}
				}
			}
		}
	}
}

func NewBinanceBollPumpSource(bn *binance.Client, symbolLimit int) BollPumpSource {
	return &binanceBollPumpSource{bn: bn, symbolLimit: symbolLimit, quoteCache: map[string]float64{}}
}

func BollPumpConfigFromRuntime(cfg *config.Config) BollPumpConfig {
	out := DefaultBollPumpConfig()
	if cfg == nil {
		return out
	}
	out.Enabled = cfg.BollPumpEnabled
	out.Market = normalizeBollPumpMarket(cfg.BollPumpMarket)
	out.SymbolLimit = cfg.BollPumpSymbolLimit
	out.ScanTimeoutSec = cfg.BollPumpScanTimeoutSec
	if tfs := splitBollPumpCSV(cfg.BollPumpTimeframes); len(tfs) > 0 {
		out.Timeframes = tfs
	}
	return out
}

func (s *BollPumpScanner) refreshConfig(ctx context.Context) BollPumpConfig {
	if s == nil {
		return DefaultBollPumpConfig()
	}
	cfg := NormalizeBollPumpConfig(s.cfg)
	if s.store != nil {
		if loaded, err := LoadBollPumpConfig(ctx, s.store, cfg); err == nil {
			cfg = loaded
		}
	}
	s.cfg = cfg
	s.symbolLimit = cfg.SymbolLimit
	return cfg
}

func (s *BollPumpScanner) klineLimit() int {
	maxStartup := 0
	for _, v := range s.cfg.StartupWindows {
		if v > maxStartup {
			maxStartup = v
		}
	}
	limit := s.cfg.BackgroundLookback + maxStartup + s.cfg.BollPeriod + s.cfg.StageExpiryCandles + 10
	if limit < 200 {
		return 200
	}
	return limit
}

func (s *BollPumpScanner) keyK4HKlineLimit() int {
	limit := s.cfg.KeyK4HLookback + s.cfg.BollPeriod + 20
	if limit < 80 {
		return 80
	}
	return limit
}

func (s *BollPumpScanner) loadRuntimeState(ctx context.Context, symbol, timeframe string) BollPumpRuntimeState {
	fresh := NewBollPumpRuntimeState(s.cfg.Market, symbol, timeframe)
	if s.store == nil {
		return fresh
	}
	st, err := GetBollPumpState(ctx, s.store, s.cfg.Market, symbol, timeframe)
	if err != nil || st == nil {
		return fresh
	}
	return runtimeStateFromModel(*st)
}

func (s *BollPumpScanner) persistState(ctx context.Context, state BollPumpRuntimeState) {
	if s.store == nil {
		return
	}
	_ = SaveBollPumpState(ctx, s.store, modelFromRuntimeState(state))
}

func (s *BollPumpScanner) persistSignal(ctx context.Context, sig model.BollPumpSignal) {
	if s.store == nil {
		return
	}
	insertAnomaly := sig.PriorityScore >= s.telegramThreshold(sig.SignalLevel)
	_, _ = SaveBollPumpSignal(ctx, s.store, sig, insertAnomaly)
}

func (s *BollPumpScanner) signalExists(ctx context.Context, sig model.BollPumpSignal) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}
	return BollPumpSignalExists(ctx, s.store, sig)
}

func (s *BollPumpScanner) telegramThreshold(level string) float64 {
	switch level {
	case string(BollPumpLevelConfirm2):
		return s.cfg.Confirm2TelegramThreshold
	case string(BollPumpLevelConfirm1):
		return s.cfg.Confirm1TelegramThreshold
	case string(BollPumpLevelKeyK4H):
		return s.cfg.KeyK4HTelegramThreshold
	default:
		return s.cfg.WatchTelegramThreshold
	}
}

type binanceBollPumpSource struct {
	bn          *binance.Client
	symbolLimit int
	quoteCache  map[string]float64
}

func (s *binanceBollPumpSource) Symbols(ctx context.Context, market string, limit int) ([]string, error) {
	if s == nil || s.bn == nil {
		return nil, fmt.Errorf("binance source is nil")
	}
	if limit <= 0 {
		limit = s.symbolLimit
	}
	tickers, err := s.bn.GetTicker24hAll(ctx, market)
	if err != nil {
		return nil, err
	}
	type row struct {
		symbol string
		qv     float64
	}
	rows := make([]row, 0, len(tickers))
	for _, t := range tickers {
		sym, _ := t["symbol"].(string)
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if !bollPumpTradableUSDTSymbol(sym) {
			continue
		}
		qv := bollPumpToFloat(t["quoteVolume"])
		rows = append(rows, row{symbol: sym, qv: qv})
		s.quoteCache[sym] = qv
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].qv > rows[j].qv })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.symbol)
	}
	return out, nil
}

func bollPumpTradableUSDTSymbol(symbol string) bool {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	return strings.HasSuffix(sym, "USDT") && !binance.IsExcludedSymbol(sym)
}

func (s *binanceBollPumpSource) Klines(ctx context.Context, market, symbol, timeframe string, limit int) ([]BollPumpBar, error) {
	raw, err := s.bn.GetKlines(ctx, market, symbol, timeframe, limit)
	if err != nil {
		return nil, err
	}
	out := make([]BollPumpBar, 0, len(raw))
	for _, k := range raw {
		if len(k) < 8 {
			continue
		}
		out = append(out, BollPumpBar{
			OpenTimeMs:  int64(bollPumpToFloat(k[0])),
			Open:        bollPumpToFloat(k[1]),
			High:        bollPumpToFloat(k[2]),
			Low:         bollPumpToFloat(k[3]),
			Close:       bollPumpToFloat(k[4]),
			Volume:      bollPumpToFloat(k[5]),
			CloseTimeMs: int64(bollPumpToFloat(k[6])),
			QuoteVolume: bollPumpToFloat(k[7]),
			Closed:      true,
		})
	}
	return out, nil
}

func (s *binanceBollPumpSource) QuoteVolume24h(ctx context.Context, market, symbol string) (float64, error) {
	if v, ok := s.quoteCache[strings.ToUpper(symbol)]; ok {
		return v, nil
	}
	t, err := s.bn.GetTicker24h(ctx, market, symbol)
	if err != nil {
		return 0, err
	}
	return bollPumpToFloat(t["quoteVolume"]), nil
}

func runtimeStateFromModel(st model.BollPumpState) BollPumpRuntimeState {
	out := NewBollPumpRuntimeState(st.Market, st.Symbol, st.Timeframe)
	out.Status = st.Status
	out.WatchScore = st.WatchScore
	out.CurrentScore = st.CurrentScore
	out.BounceCount = st.BounceCount
	out.WatchCandleStartMs = ptrI64Value(st.WatchCandleStartMs)
	out.WatchStartedMs = ptrI64Value(st.WatchStartedMs)
	out.FirstPullbackLow = ptrF64Value(st.FirstPullbackLow)
	out.SecondPullbackLow = ptrF64Value(st.SecondPullbackLow)
	out.PendingPullbackCandleMs = ptrI64Value(st.PendingPullbackCandleMs)
	out.PendingPullbackHigh = ptrF64Value(st.PendingPullbackHigh)
	out.ExpiresAtCandleMs = ptrI64Value(st.ExpiresAtCandleMs)
	out.LastCheckedCandleMs = ptrI64Value(st.LastCheckedCandleMs)
	if st.LastSignalLevel != nil {
		out.LastSignalLevel = *st.LastSignalLevel
	}
	return out
}

func modelFromRuntimeState(st BollPumpRuntimeState) model.BollPumpState {
	return model.BollPumpState{
		Market:                  st.Market,
		Symbol:                  st.Symbol,
		Timeframe:               st.Timeframe,
		Status:                  st.Status,
		WatchStartedMs:          ptrI64(st.WatchStartedMs),
		WatchCandleStartMs:      ptrI64(st.WatchCandleStartMs),
		WatchScore:              st.WatchScore,
		CurrentScore:            st.CurrentScore,
		PriorityScore:           st.CurrentScore,
		BounceCount:             st.BounceCount,
		FirstPullbackLow:        ptrF64(st.FirstPullbackLow),
		SecondPullbackLow:       ptrF64(st.SecondPullbackLow),
		PendingPullbackCandleMs: ptrI64(st.PendingPullbackCandleMs),
		PendingPullbackHigh:     ptrF64(st.PendingPullbackHigh),
		LastCheckedCandleMs:     ptrI64(st.LastCheckedCandleMs),
		LastSignalLevel:         ptrString(st.LastSignalLevel),
		ExpiresAtCandleMs:       ptrI64(st.ExpiresAtCandleMs),
		Details:                 model.JSONB(`{}`),
	}
}

func bollPumpClosedBars(bars []BollPumpBar) []BollPumpBar {
	out := bars[:0]
	for _, b := range bars {
		if b.Closed {
			out = append(out, b)
		}
	}
	return out
}

func bollPumpClosedBarsBefore(bars []BollPumpBar, nowMs int64) []BollPumpBar {
	out := bars[:0]
	for _, b := range bars {
		if b.Closed && (b.CloseTimeMs == 0 || b.CloseTimeMs <= nowMs) {
			out = append(out, b)
		}
	}
	return out
}

func splitBollPumpCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lastClosedStartForTimeframe(nowMs int64, timeframe string) int64 {
	step := bollPumpIntervalMs(timeframe, nil)
	return (nowMs/step)*step - step
}

func bollPumpToFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func ptrI64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func ptrF64(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func ptrString(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}

func ptrI64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func ptrF64Value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
