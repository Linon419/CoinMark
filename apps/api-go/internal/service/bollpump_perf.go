package service

import (
	"context"
	"log"
	"sort"

	"coinmark/api-go/internal/repo/sqlite"
)

// 信号之后 1h/4h/24h 的表现（收盘收益、最大涨幅、最大回撤），用 BOLL K 线缓存里的 5m K 线计算。
// 5m 缓存约 41 小时，所以只补 40 小时内的信号。原来表里有这些字段，但一直没有写入。
const (
	bollPumpPerfBarMs  = int64(5 * 60 * 1000)
	bollPumpPerfMaxAge = int64(40 * 3600 * 1000)
)

var bollPumpPerfHorizonsH = []int64{1, 4, 24}

// bollPumpComputePerformance 信号收盘后每个时长内的 5m K 线：收盘收益 = 窗口最后一根收盘；最大涨幅/回撤 = 窗口内最高/最低。
// 时间没到或 K 线缺太多（少于应有根数 - 2）的时长留空。
func bollPumpComputePerformance(bars []BollPumpBar, signalMs int64, price float64, nowMs int64) (BollPumpPerformance, bool) {
	perf := BollPumpPerformance{UpdatedMs: nowMs}
	if price <= 0 {
		return perf, false
	}
	any := false
	for _, h := range bollPumpPerfHorizonsH {
		end := signalMs + h*3600*1000
		if nowMs < end {
			continue
		}
		n, hi, lo, last := 0, 0.0, 0.0, 0.0
		for _, b := range bars {
			if b.OpenTimeMs < signalMs || b.OpenTimeMs+bollPumpPerfBarMs > end+1 {
				continue
			}
			if n == 0 || b.High > hi {
				hi = b.High
			}
			if n == 0 || b.Low < lo {
				lo = b.Low
			}
			last = b.Close
			n++
		}
		if int64(n) < h*3600*1000/bollPumpPerfBarMs-2 {
			continue
		}
		gain, dd, ret := hi/price-1, lo/price-1, last/price-1
		switch h {
		case 1:
			perf.Perf1hMaxGain, perf.Perf1hMaxDrawdown, perf.Perf1hCloseReturn = &gain, &dd, &ret
		case 4:
			perf.Perf4hMaxGain, perf.Perf4hMaxDrawdown, perf.Perf4hCloseReturn = &gain, &dd, &ret
		case 24:
			perf.Perf24hMaxGain, perf.Perf24hMaxDrawdown, perf.Perf24hCloseReturn = &gain, &dd, &ret
		}
		any = true
	}
	return perf, any
}

type bollPumpPendingPerf struct {
	ID           int64   `db:"id"`
	Symbol       string  `db:"symbol"`
	Price        float64 `db:"price"`
	SignalTimeMs int64   `db:"signal_time_ms"`
}

// fillPerformance 给还缺 1h/4h/24h 表现、且时间已到的信号补上表现；同一个币的 5m K 线只取一次。
func (s *BollPumpScanner) fillPerformance(ctx context.Context, nowMs int64) (int, error) {
	if s.store == nil || s.source == nil {
		return 0, nil
	}
	market := normalizeBollPumpMarket(s.cfg.Market)
	var pending []bollPumpPendingPerf
	if err := s.store.SelectContext(ctx, &pending, `SELECT id, symbol, price, signal_time_ms FROM boll_pump_signals
WHERE market = ? AND signal_time_ms > ? AND (
 (perf_1h_close_return IS NULL AND signal_time_ms <= ?) OR
 (perf_4h_close_return IS NULL AND signal_time_ms <= ?) OR
 (perf_24h_close_return IS NULL AND signal_time_ms <= ?))`,
		market, nowMs-bollPumpPerfMaxAge, nowMs-3600*1000, nowMs-4*3600*1000, nowMs-24*3600*1000); err != nil {
		return 0, err
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Symbol < pending[j].Symbol })
	updated := 0
	var bars []BollPumpBar
	lastSymbol := ""
	for _, p := range pending {
		if p.Symbol != lastSymbol {
			lastSymbol = p.Symbol
			bars = nil
			if b, err := s.source.Klines(ctx, market, p.Symbol, "5m", 499); err == nil {
				bars = bollPumpClosedBarsBefore(b, nowMs)
			}
		}
		perf, ok := bollPumpComputePerformance(bars, p.SignalTimeMs, p.Price, nowMs)
		if !ok {
			continue
		}
		if err := UpdateBollPumpPerformance(ctx, s.store, p.ID, perf); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

type bollPumpPerfRow struct {
	Level string   `db:"level"`
	R4h   *float64 `db:"r4h"`
	R24h  *float64 `db:"r24h"`
}

// BollPumpLevelPerformance 每个级别的实盘表现汇总（只算已有 4h 表现的信号）。
type BollPumpLevelPerformance struct {
	Count4h    int     `json:"count4h"`
	Up4hRatio  float64 `json:"up4hRatio"`
	Median4h   float64 `json:"median4h"`
	Count24h   int     `json:"count24h"`
	Median24h  float64 `json:"median24h"`
	Up24hRatio float64 `json:"up24hRatio"`
}

func bollPumpPerformanceByLevel(ctx context.Context, store *sqlite.Store, market string, sinceMs int64) (map[string]BollPumpLevelPerformance, error) {
	var rows []bollPumpPerfRow
	if err := store.SelectContext(ctx, &rows, `SELECT signal_level AS level, perf_4h_close_return AS r4h, perf_24h_close_return AS r24h
FROM boll_pump_signals WHERE market = ? AND signal_time_ms >= ? AND perf_4h_close_return IS NOT NULL`, market, sinceMs); err != nil {
		return nil, err
	}
	r4 := map[string][]float64{}
	r24 := map[string][]float64{}
	for _, r := range rows {
		r4[r.Level] = append(r4[r.Level], *r.R4h)
		if r.R24h != nil {
			r24[r.Level] = append(r24[r.Level], *r.R24h)
		}
	}
	out := map[string]BollPumpLevelPerformance{}
	for level, v := range r4 {
		p := BollPumpLevelPerformance{Count4h: len(v), Up4hRatio: bollPumpUpRatio(v), Median4h: bollPumpMedian(v)}
		if w := r24[level]; len(w) > 0 {
			p.Count24h, p.Up24hRatio, p.Median24h = len(w), bollPumpUpRatio(w), bollPumpMedian(w)
		}
		out[level] = p
	}
	return out, nil
}

func bollPumpUpRatio(v []float64) float64 {
	up := 0
	for _, x := range v {
		if x > 0 {
			up++
		}
	}
	return float64(up) / float64(len(v))
}

func bollPumpMedian(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// logFillPerformance Run 循环里调用：补表现，出错只记日志。
func (s *BollPumpScanner) logFillPerformance(ctx context.Context, nowMs int64) {
	if n, err := s.fillPerformance(ctx, nowMs); err != nil {
		log.Printf("boll_pump: fill performance error: %v", err)
	} else if n > 0 {
		log.Printf("boll_pump: performance updated=%d", n)
	}
}
