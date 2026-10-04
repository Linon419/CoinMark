package service

import (
	"context"
	"math"
	"testing"

	"coinmark/api-go/internal/model"
)

// signalMs 是信号 K 线收盘时间（xx:59.999），之后的 5m K 线从 t0 开始。
func bollPumpPerfFixture() (bars []BollPumpBar, signalMs int64) {
	t0 := int64(1000) * 3600 * 1000
	bars = potentialTestBars(300, bollPumpPerfBarMs, t0, func(i int) float64 { return 1 })
	bars[3].High = 1.10   // 1h 内最高 +10%
	bars[5].Low = 0.95    // 1h 内最低 -5%
	bars[11].Close = 1.02 // 1h 收盘（第 12 根）
	bars[47].Close = 0.97 // 4h 收盘（第 48 根）
	bars[287].Close = 1.2 // 24h 收盘（第 288 根）
	return bars, t0 - 1
}

func TestBollPumpComputePerformance(t *testing.T) {
	bars, signalMs := bollPumpPerfFixture()
	perf, ok := bollPumpComputePerformance(bars, signalMs, 1, signalMs+5*3600*1000)
	if !ok {
		t.Fatal("expected performance")
	}
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }
	if !near(perf.Perf1hCloseReturn, 0.02) || !near(perf.Perf1hMaxGain, 0.10) || !near(perf.Perf1hMaxDrawdown, -0.05) {
		t.Fatalf("1h = %v %v %v", perf.Perf1hCloseReturn, perf.Perf1hMaxGain, perf.Perf1hMaxDrawdown)
	}
	if !near(perf.Perf4hCloseReturn, -0.03) {
		t.Fatalf("4h close = %v", perf.Perf4hCloseReturn)
	}
	if perf.Perf24hCloseReturn != nil {
		t.Fatal("24h not reached yet, should stay nil")
	}
	// K 线缺太多时不算
	if _, ok := bollPumpComputePerformance(bars[:6], signalMs, 1, signalMs+5*3600*1000); ok {
		t.Fatal("expected no performance with missing bars")
	}
}

func TestBollPumpFillPerformanceWritesOnceAndSummarizes(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()
	bars, signalMs := bollPumpPerfFixture()
	if _, err := SaveBollPumpSignal(ctx, store, model.BollPumpSignal{
		Market: "swap", Symbol: "XYZUSDT", Timeframe: "15m", SignalLevel: string(BollPumpLevelWatch),
		Price: 1, SignalTimeMs: signalMs, CandleStartMs: signalMs - 15*60*1000 + 1, Details: model.JSONB(`{}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	scanner := NewBollPumpScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"5m": bars}}, store, DefaultBollPumpConfig())

	// 5 小时后：补上 1h、4h
	if n, err := scanner.fillPerformance(ctx, signalMs+5*3600*1000); err != nil || n != 1 {
		t.Fatalf("first fill: n=%d err=%v", n, err)
	}
	// 再过 20 小时：补 24h，1h/4h 保持不变
	if n, err := scanner.fillPerformance(ctx, signalMs+25*3600*1000); err != nil || n != 1 {
		t.Fatalf("second fill: n=%d err=%v", n, err)
	}
	if n, _ := scanner.fillPerformance(ctx, signalMs+26*3600*1000); n != 0 {
		t.Fatalf("third fill: n=%d, want 0 (nothing pending)", n)
	}
	sig, err := GetBollPumpSignal(ctx, store, 1)
	if err != nil || sig == nil || sig.Perf1hCloseReturn == nil || math.Abs(*sig.Perf1hCloseReturn-0.02) > 1e-9 || sig.Perf24hCloseReturn == nil || math.Abs(*sig.Perf24hCloseReturn-0.2) > 1e-9 {
		t.Fatalf("signal perf = %+v err=%v", sig, err)
	}
	perf, err := bollPumpPerformanceByLevel(ctx, store, "swap", 0)
	if err != nil {
		t.Fatal(err)
	}
	if w := perf[string(BollPumpLevelWatch)]; w.Count4h != 1 || w.Up4hRatio != 0 || w.Count24h != 1 || w.Up24hRatio != 1 {
		t.Fatalf("perf by level = %+v", w)
	}
}
