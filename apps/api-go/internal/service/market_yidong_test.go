package service

import (
	"context"
	"testing"

	"coinmark/api-go/internal/marketstate"
	"coinmark/api-go/internal/model"
)

const testDayStart = int64(1790812800000) // 2026-10-01 00:00 UTC

func yidongDayBars(fromDaysAgo int) []yidongBar {
	var bars []yidongBar
	for i := fromDaysAgo; i >= 1; i-- {
		bars = append(bars, yidongBar{Ts: testDayStart - int64(i)*yidongDayMs, O: 1, H: 2, L: 0.5, C: 1})
	}
	return bars
}

func TestYidongDailyCoverage(t *testing.T) {
	cases := []struct {
		name          string
		bars          []yidongBar
		want7, want30 bool
	}{
		{"无日线", nil, false, false},
		{"只有今天的分钟线", []yidongBar{{Ts: testDayStart}, {Ts: testDayStart + yidongMinuteMs}}, false, false},
		{"只有前 5 天", yidongDayBars(5), false, false},
		{"刚好前 6 天", yidongDayBars(6), true, false},
		{"前 28 天", yidongDayBars(28), true, false},
		{"前 29 天", yidongDayBars(29), true, true},
	}
	for _, tc := range cases {
		got7, got30 := yidongDailyCoverage(tc.bars, testDayStart)
		if got7 != tc.want7 || got30 != tc.want30 {
			t.Fatalf("%s: got (%v,%v), want (%v,%v)", tc.name, got7, got30, tc.want7, tc.want30)
		}
	}
}

type fetchCall struct {
	symbols    []string
	start, end int64
}

// fakeDailyFetch 为每个请求的币、每个请求的日子返回一根日线（由 1m 行聚合而来，这里直接给日线行）。
func fakeDailyFetch(calls *[]fetchCall) yidongDailyFetch {
	return func(_ context.Context, _ string, symbols []string, startMs, endMs int64) ([]model.CHTradeRow, error) {
		*calls = append(*calls, fetchCall{symbols: append([]string(nil), symbols...), start: startMs, end: endMs})
		var rows []model.CHTradeRow
		for _, s := range symbols {
			for d := startMs; d <= endMs; d += yidongDayMs {
				p := float64(d % 1000)
				rows = append(rows, model.CHTradeRow{Symbol: s, BucketStartMs: d, OpenPrice: &p, HighPrice: &p, LowPrice: &p, ClosePrice: &p})
			}
		}
		return rows, nil
	}
}

func TestYidongDailyCacheFetchesOnlyMissingDays(t *testing.T) {
	c := newYidongDailyCache()
	var calls []fetchCall
	fetch := fakeDailyFetch(&calls)
	ctx := context.Background()
	from := testDayStart - yidongDailyLookbackDays*yidongDayMs

	// 第一次：全部 29 天
	got, err := c.get(ctx, "swap", []string{"AUSDT", "BUSDT"}, testDayStart, testDayStart+10*yidongMinuteMs, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].start != from || calls[0].end != testDayStart-1 || len(calls[0].symbols) != 2 {
		t.Fatalf("首次应一次拉齐 29 天: %+v", calls)
	}
	if len(got["AUSDT"]) != yidongDailyLookbackDays || got["AUSDT"][0].Ts != from {
		t.Fatalf("AUSDT 日线数量/顺序不对: %d", len(got["AUSDT"]))
	}

	// 同一天再扫：不再查询
	if _, err := c.get(ctx, "swap", []string{"AUSDT", "BUSDT"}, testDayStart, testDayStart+20*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("同一天不应重复查询: %+v", calls)
	}

	// 第二天：只补昨天一天；并淘汰超出 29 天的旧日线
	next := testDayStart + yidongDayMs
	got, err = c.get(ctx, "swap", []string{"AUSDT", "BUSDT"}, next, next+10*yidongMinuteMs, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].start != testDayStart || calls[1].end != next-1 {
		t.Fatalf("第二天应只补一天: %+v", calls[1:])
	}
	if len(got["BUSDT"]) != yidongDailyLookbackDays || got["BUSDT"][0].Ts != next-yidongDailyLookbackDays*yidongDayMs {
		t.Fatalf("旧日线应被淘汰，got %d 根，最早 %d", len(got["BUSDT"]), got["BUSDT"][0].Ts)
	}

	// 新进入的币：只给它补全量
	if _, err := c.get(ctx, "swap", []string{"AUSDT", "CUSDT"}, next, next+12*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(calls[2].symbols) != 1 || calls[2].symbols[0] != "CUSDT" || calls[2].start != next-yidongDailyLookbackDays*yidongDayMs {
		t.Fatalf("新币应单独补全量: %+v", calls[2:])
	}

	// 不同市场互不影响
	if _, err := c.get(ctx, "spot", []string{"AUSDT"}, next, next+12*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[3].start != next-yidongDailyLookbackDays*yidongDayMs {
		t.Fatalf("spot 应单独拉取: %+v", calls[3:])
	}
}

func TestYidongDailyCacheRefetchesYesterdayUntilFinal(t *testing.T) {
	c := newYidongDailyCache()
	var calls []fetchCall
	fetch := fakeDailyFetch(&calls)
	ctx := context.Background()
	yesterday := testDayStart - yidongDayMs
	syms := []string{"AUSDT"}

	// 刚过 0 点 1 分钟：昨天可能还没写完，拉到但不定稿
	if _, err := c.get(ctx, "swap", syms, testDayStart, testDayStart+1*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	// 3 分钟：重新拉昨天
	if _, err := c.get(ctx, "swap", syms, testDayStart, testDayStart+3*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].start != yesterday {
		t.Fatalf("昨天未定稿前应重拉: %+v", calls)
	}
	// 6 分钟：再拉一次，此后定稿
	if _, err := c.get(ctx, "swap", syms, testDayStart, testDayStart+6*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get(ctx, "swap", syms, testDayStart, testDayStart+8*yidongMinuteMs, fetch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("昨天定稿后不应再拉: %+v", calls)
	}
}

func TestYidongMinuteMapPrefersReadyMarketState(t *testing.T) {
	ms := marketstate.New(1440)
	ms.Apply(marketstate.Trade{Market: "swap", Symbol: "AUSDT", TimeMs: testDayStart + 1000, Price: 2, Qty: 3})
	ms.Apply(marketstate.Trade{Market: "swap", Symbol: "AUSDT", TimeMs: testDayStart + yidongMinuteMs + 1, Price: 4, Qty: 1})
	ms.Apply(marketstate.Trade{Market: "swap", Symbol: "AUSDT", TimeMs: testDayStart + 2*yidongMinuteMs + 1, Price: 9, Qty: 1}) // 超出查询范围
	ms.MarkLoaded()
	ms.Touch(testDayStart + 3*yidongMinuteMs)

	fallbackCalls := 0
	fallback := func(context.Context, string, []string, int64, int64) ([]model.CHTradeRow, error) {
		fallbackCalls++
		return nil, nil
	}
	got, err := yidongMinuteMap(context.Background(), ms, testDayStart+3*yidongMinuteMs, "swap", []string{"AUSDT", "BUSDT"}, testDayStart, testDayStart+yidongMinuteMs, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 0 {
		t.Fatal("状态可用时不应查 ClickHouse")
	}
	a := got["AUSDT"]
	if len(a) != 2 || a[0].O != 2 || a[0].QV != 6 || a[1].C != 4 {
		t.Fatalf("分钟线不对: %+v", a)
	}
	if len(got["BUSDT"]) != 0 {
		t.Fatalf("没成交的币应为空: %+v", got["BUSDT"])
	}

	// 断流后回退到 ClickHouse
	if _, err := yidongMinuteMap(context.Background(), ms, testDayStart+10*yidongMinuteMs, "swap", []string{"AUSDT"}, testDayStart, testDayStart+yidongMinuteMs, fallback); err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 1 {
		t.Fatal("状态不可用时应回退到 ClickHouse")
	}
	// 未启用（nil）也回退
	if _, err := yidongMinuteMap(context.Background(), nil, testDayStart, "swap", []string{"AUSDT"}, testDayStart, testDayStart, fallback); err != nil || fallbackCalls != 2 {
		t.Fatalf("未启用时应回退: calls=%d err=%v", fallbackCalls, err)
	}
}
