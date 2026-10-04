package service

import (
	"context"
	"testing"

	"coinmark/api-go/internal/model"
)

// 空闲状态、新 K 线没有改变状态时，不写数据库（原来每个周期每次扫描都对全部币写一次）。
func TestBollPumpScannerSkipsStateWriteWhenOnlyLastCheckedAdvances(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()
	const m15 = int64(15 * 60 * 1000)
	bars := potentialTestBars(120, m15, 0, func(i int) float64 { return 1 }) // 横盘，不会触发
	if err := SaveBollPumpState(ctx, store, model.BollPumpState{
		Market: "swap", Symbol: "XYZUSDT", Timeframe: "15m", Status: string(BollPumpStatusIdle),
		LastCheckedCandleMs: ptrInt64(bars[len(bars)-2].OpenTimeMs), Details: model.JSONB(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"15m"}
	src := &fakeBollPumpSource{symbols: []string{"XYZUSDT"}, bars: map[string][]BollPumpBar{"15m": bars}}
	scanner := NewBollPumpScanner(src, store, cfg)

	scanner.ScanTimeframe(ctx, "15m")

	st, err := GetBollPumpState(ctx, store, "swap", "XYZUSDT", "15m")
	if err != nil || st == nil {
		t.Fatalf("state = %v err=%v", st, err)
	}
	if got := ptrI64Value(st.LastCheckedCandleMs); got != bars[len(bars)-2].OpenTimeMs {
		t.Fatalf("db last_checked = %d, want unchanged %d (no write)", got, bars[len(bars)-2].OpenTimeMs)
	}
	// 内存里已经检查到最新一根，下一次扫描不会重复处理
	if mem := scanner.loadRuntimeState(ctx, "XYZUSDT", "15m"); mem.LastCheckedCandleMs != bars[len(bars)-1].OpenTimeMs {
		t.Fatalf("memory last_checked = %d, want latest %d", mem.LastCheckedCandleMs, bars[len(bars)-1].OpenTimeMs)
	}
}

// 重启后从数据库读到很久以前检查过的空闲状态时，最多回放最近 30 根，避免用很旧的 K 线重新触发观察。
func TestBollPumpCapIdleReplay(t *testing.T) {
	const m15 = int64(15 * 60 * 1000)
	bars := potentialTestBars(120, m15, 0, func(i int) float64 { return 1 })
	idle := BollPumpRuntimeState{Status: string(BollPumpStatusIdle), LastCheckedCandleMs: bars[5].OpenTimeMs}
	bollPumpCapIdleReplay(&idle, bars)
	if want := bars[len(bars)-1-bollPumpIdleReplayCandles].OpenTimeMs; idle.LastCheckedCandleMs != want {
		t.Fatalf("idle last_checked = %d, want %d", idle.LastCheckedCandleMs, want)
	}
	recent := BollPumpRuntimeState{Status: string(BollPumpStatusIdle), LastCheckedCandleMs: bars[110].OpenTimeMs}
	bollPumpCapIdleReplay(&recent, bars)
	if recent.LastCheckedCandleMs != bars[110].OpenTimeMs {
		t.Fatal("recently checked idle state should replay normally")
	}
	active := BollPumpRuntimeState{Status: string(BollPumpStatusWatch), LastCheckedCandleMs: bars[5].OpenTimeMs}
	bollPumpCapIdleReplay(&active, bars)
	if active.LastCheckedCandleMs != bars[5].OpenTimeMs {
		t.Fatal("active state must replay every candle since last check")
	}
}
