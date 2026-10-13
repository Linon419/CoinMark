package service

import (
	"context"
	"testing"

	"coinmark/api-go/internal/model"
)

// bollShortFixture 一路下跌的 K 线，末尾接一根“碰上轨、收回上轨下方”的压力 K，再接一根跌破它最低价的 K 线。
func bollShortFixture(n int, stepMs int64) []BollPumpBar {
	bars := bollPumpLineBars(n, 150, 100, stepMs)
	for i := range bars {
		bars[i].High = bars[i].Close + 0.3
		bars[i].Low = bars[i].Close - 0.3
	}
	add := func(open, high, low, close float64) {
		i := len(bars)
		bars = append(bars, BollPumpBar{OpenTimeMs: int64(i) * stepMs, CloseTimeMs: int64(i+1)*stepMs - 1,
			Open: open, High: high, Low: low, Close: close, Volume: 300, QuoteVolume: 300 * close, Closed: true})
	}
	add(100, 100, 99.5, 99.8) // 压力 K：最高价稍后按上轨设置
	ind := ComputeBollPumpIndicators(bars, 20, 2, 14)
	r := len(bars) - 1
	bars[r].High = ind[r].Upper * 1.002
	add(99.7, 99.9, 99.2, 99.3) // 跌破压力 K 最低价 99.5
	return bars
}

func bollShortInd(bars []BollPumpBar) []BollPumpIndicator {
	return ComputeBollPumpIndicators(bars, 20, 2, 14)
}

func TestBollShortTrigger(t *testing.T) {
	bars := bollShortFixture(60, 60_000)
	if r, ok := bollShortTrigger(bars, bollShortInd(bars)); !ok || r != len(bars)-2 {
		t.Fatalf("碰上轨后下一根跌破最低价：应触发，r=%d ok=%v", r, ok)
	}

	noBreak := append([]BollPumpBar(nil), bars...)
	noBreak[len(noBreak)-1].Low = 99.6 // 没跌破 99.5
	if _, ok := bollShortTrigger(noBreak, bollShortInd(noBreak)); ok {
		t.Fatal("没跌破压力 K 最低价，不应触发")
	}

	noTouch := bollShortFixture(60, 60_000)
	noTouch[len(noTouch)-2].High = 100.1 // 没碰到上轨
	if _, ok := bollShortTrigger(noTouch, bollShortInd(noTouch)); ok {
		t.Fatal("压力 K 没碰到上轨，不应触发")
	}

	// 压力 K 之后已经有一根跌破过，最新这根再跌不算新信号
	later := bollShortFixture(60, 60_000)
	last := later[len(later)-1]
	last.OpenTimeMs += 60_000
	last.CloseTimeMs += 60_000
	last.Low, last.Close = 99.0, 99.1
	later = append(later, last)
	if _, ok := bollShortTrigger(later, bollShortInd(later)); ok {
		t.Fatal("只在第一次跌破时触发一次")
	}
}

func TestBollPumpBelowEMA100And200(t *testing.T) {
	if !bollPumpBelowEMA100And200(bollPumpLineBars(250, 150, 100, 60_000)) {
		t.Fatal("一路下跌，收盘价应在 EMA100/200 之下")
	}
	if bollPumpBelowEMA100And200(bollPumpLineBars(250, 100, 150, 60_000)) {
		t.Fatal("一路上涨，不应通过")
	}
	if bollPumpBelowEMA100And200(bollPumpLineBars(150, 150, 100, 60_000)) {
		t.Fatal("不足 200 根，不应通过")
	}
}

func TestBollShortHigherTimeframe(t *testing.T) {
	for in, want := range map[string]string{"1m": "3m", "3m": "5m", "5m": "15m", "15m": "30m", "30m": "1h", "1h": ""} {
		if got := bollShortHigherTimeframe(in); got != want {
			t.Fatalf("%s 的上一级 = %q, want %q", in, got, want)
		}
	}
}

func TestBollPumpScannerShortSignal(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1m"}
	cfg.Resistance4HBreakoutBonus = 0
	trigger := bollShortFixture(250, 60_000)

	run := func(higher []BollPumpBar, tgOn bool) ([]model.BollPumpSignal, int) {
		store := openBollPumpTestStore(t)
		defer store.Close()
		c := cfg
		c.ShortTelegramEnabled = tgOn
		if _, err := SaveBollPumpConfig(ctx, store, c); err != nil {
			t.Fatal(err)
		}
		source := &fakeBollPumpSource{
			bars:  map[string][]BollPumpBar{"1m": trigger, "3m": higher},
			quote: map[string]float64{"XYZUSDT": 3_000_000},
		}
		NewBollPumpScanner(source, store, c).ScanTimeframe(ctx, "1m")
		rows, err := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Side: "short", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var events int
		if err := store.GetContext(ctx, &events, `SELECT COUNT(*) FROM anomaly_events WHERE event_type = ?`, BollShortEventType); err != nil {
			t.Fatal(err)
		}
		return rows, events
	}

	rows, events := run(bollPumpLineBars(250, 150, 100, 180_000), false)
	if len(rows) != 1 || rows[0].SignalLevel != string(BollPumpLevelShort) || rows[0].Timeframe != "1m" {
		t.Fatalf("上一级 3m 也在 EMA 之下：应有 1 条做空信号，得到 %#v", rows)
	}
	if events != 0 {
		t.Fatalf("做空推送默认关，不应写通知，得到 %d", events)
	}
	if _, events := run(bollPumpLineBars(250, 150, 100, 180_000), true); events != 1 {
		t.Fatalf("打开做空推送后应写 1 条通知，得到 %d", events)
	}
	if rows, _ := run(bollPumpLineBars(250, 100, 150, 180_000), false); len(rows) != 0 {
		t.Fatalf("上一级 3m 在 EMA 之上：不应有做空信号，得到 %d", len(rows))
	}

	// 做多列表不混入做空信号
	store := openBollPumpTestStore(t)
	defer store.Close()
	NewBollPumpScanner(&fakeBollPumpSource{
		bars:  map[string][]BollPumpBar{"1m": trigger, "3m": bollPumpLineBars(250, 150, 100, 180_000)},
		quote: map[string]float64{"XYZUSDT": 3_000_000},
	}, store, cfg).ScanTimeframe(ctx, "1m")
	if longRows, _ := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Side: "long", Limit: 10}); len(longRows) != 0 {
		t.Fatalf("side=long 不应包含做空信号: %d", len(longRows))
	}
}

func TestBollShortFollowsBollPumpTGSwitch(t *testing.T) {
	p := DefaultTGNotifyPrefs(1)
	p.BollPumpEnabled = true
	if !IsTGNotifyEventEnabled(BollShortEventType, p) {
		t.Fatal("BOLL 泵盘通知打开时，做空也应推送")
	}
	p.BollPumpEnabled = false
	if IsTGNotifyEventEnabled(BollShortEventType, p) {
		t.Fatal("BOLL 泵盘通知关闭时，做空不应推送")
	}
}

func TestBollPumpStatsBySide(t *testing.T) {
	ctx := context.Background()
	store := openBollPumpTestStore(t)
	defer store.Close()
	for _, lvl := range []BollPumpSignalLevel{BollPumpLevelWatch, BollPumpLevelShort} {
		if _, err := SaveBollPumpSignal(ctx, store, model.BollPumpSignal{Market: "swap", Symbol: "XYZUSDT", Timeframe: "15m", SignalLevel: string(lvl),
			Price: 1, SignalTimeMs: 1000, CandleStartMs: 1000, Reason: "x", Details: model.JSONB(`{}`)}, false); err != nil {
			t.Fatal(err)
		}
	}
	for side, want := range map[string]int64{"": 2, "long": 1, "short": 1} {
		stats, err := BollPumpStats(ctx, store, "swap", 1, side)
		if err != nil {
			t.Fatal(err)
		}
		if got := stats["countsByTimeframe"].(map[string]int64)["15m"]; got != want {
			t.Fatalf("side=%q 15m 数量 = %d, want %d", side, got, want)
		}
	}
}

// bollShortAddEpisode 在下跌 K 线后面接一段“碰上轨（压力 K）→ 跌破”，再接 gap 根继续下跌的普通 K 线。
func bollShortAddEpisode(bars []BollPumpBar, stepMs int64, gap int) []BollPumpBar {
	add := func(open, high, low, close, vol float64) {
		i := len(bars)
		bars = append(bars, BollPumpBar{OpenTimeMs: int64(i) * stepMs, CloseTimeMs: int64(i+1)*stepMs - 1,
			Open: open, High: high, Low: low, Close: close, Volume: vol, QuoteVolume: vol * close, Closed: true})
	}
	p := bars[len(bars)-1].Close
	add(p, p, p-0.5, p-0.2, 300) // 压力 K
	r := len(bars) - 1
	ind := ComputeBollPumpIndicators(bars, 20, 2, 14)
	bars[r].High = ind[r].Upper * 1.002
	add(p-0.3, p-0.1, p-0.8, p-0.7, 100) // 跌破压力 K 最低价
	for i := 0; i < gap; i++ {
		q := bars[len(bars)-1].Close
		add(q, q+0.05, q-0.25, q-0.2, 100)
	}
	return bars
}

func TestBollShortSegments(t *testing.T) {
	bars := bollPumpLineBars(250, 150, 100, 60_000)
	bars = bollShortAddEpisode(bars, 60_000, 8)
	bars = bollShortAddEpisode(bars, 60_000, 0)
	ind := bollShortInd(bars)
	if _, ok := bollShortTrigger(bars, ind); !ok {
		t.Fatal("第二段应触发")
	}
	if got := bollShortSegments(bars, ind, 60); got != 2 {
		t.Fatalf("连续两段碰上轨回落，段数 = %d, want 2", got)
	}

	// 两段之间有一根收盘站上上轨：下跌被打断，只算这一段
	broken := bollPumpLineBars(250, 150, 100, 60_000)
	broken = bollShortAddEpisode(broken, 60_000, 8)
	mid := len(broken) - 4
	ind = bollShortInd(broken)
	broken[mid].Close = ind[mid].Upper * 1.01
	broken[mid].High = broken[mid].Close
	broken = bollShortAddEpisode(broken, 60_000, 0)
	ind = bollShortInd(broken)
	if _, ok := bollShortTrigger(broken, ind); !ok {
		t.Fatal("第二段本身仍应触发")
	}
	if got := bollShortSegments(broken, ind, 60); got != 1 {
		t.Fatalf("中间站上过上轨，段数 = %d, want 1", got)
	}
}

func TestBollShortCandleScore(t *testing.T) {
	bars := bollPumpLineBars(250, 150, 100, 60_000)
	bars = bollShortAddEpisode(bars, 60_000, 0)
	ind := bollShortInd(bars)
	r := len(bars) - 2
	// 压力 K：放量 3 倍、长上影
	bars[r].Volume = 300
	score, _ := bollShortCandleScore(bars, ind, r, 1)
	if score != 60+10+10 {
		t.Fatalf("放量 + 长上影，一段：分数 = %v, want 80", score)
	}
	if score, _ := bollShortCandleScore(bars, ind, r, 2); score != 60+10+10+15 {
		t.Fatalf("再加连续两段：分数 = %v, want 95", score)
	}
	bars[r].Volume = 100
	bars[r].High = bars[r].Open + 0.01 // 上影线很短
	if score, _ := bollShortCandleScore(bars, ind, r, 1); score != 60 {
		t.Fatalf("不放量、没有长上影：分数 = %v, want 60", score)
	}
}

func TestBollShortTelegramUsesScoreThreshold(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultBollPumpConfig()
	cfg.Timeframes = []string{"1m"}
	cfg.Resistance4HBreakoutBonus = 0
	cfg.ShortTelegramEnabled = true
	trigger := bollShortFixture(250, 60_000)
	for _, tc := range []struct {
		threshold float64
		want      int
	}{{60, 1}, {119, 0}} {
		store := openBollPumpTestStore(t)
		c := cfg
		c.ShortTelegramThreshold = tc.threshold
		if _, err := SaveBollPumpConfig(ctx, store, c); err != nil {
			t.Fatal(err)
		}
		NewBollPumpScanner(&fakeBollPumpSource{
			bars:  map[string][]BollPumpBar{"1m": trigger, "3m": bollPumpLineBars(250, 150, 100, 180_000)},
			quote: map[string]float64{"XYZUSDT": 3_000_000},
		}, store, c).ScanTimeframe(ctx, "1m")
		rows, _ := ListBollPumpSignals(ctx, store, BollPumpSignalFilter{Market: "swap", Side: "short", Limit: 10})
		var events int
		_ = store.GetContext(ctx, &events, `SELECT COUNT(*) FROM anomaly_events WHERE event_type = ?`, BollShortEventType)
		store.Close()
		if len(rows) != 1 || rows[0].PriorityScore < 60 {
			t.Fatalf("应有 1 条带分数的做空信号: %#v", rows)
		}
		if events != tc.want {
			t.Fatalf("门槛 %v：通知 %d 条, want %d（分数 %v）", tc.threshold, events, tc.want, rows[0].PriorityScore)
		}
	}
}

func TestBollShortTrendAndFourHourMirrorLongRules(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultBollPumpConfig()
	falling := bollPumpLineBars(120, 150, 100, 900_000)
	rising := bollPumpLineBars(120, 100, 150, 900_000)
	s := NewBollPumpScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"15m": falling}}, nil, cfg)
	if !s.shortTrendDown(ctx, "XYZUSDT", "1m", nil) {
		t.Fatal("15m 一路下跌：应算趋势向下")
	}
	s = NewBollPumpScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"15m": rising}}, nil, cfg)
	if s.shortTrendDown(ctx, "XYZUSDT", "1m", nil) {
		t.Fatal("15m 一路上涨：不应算趋势向下")
	}

	// 做多测试里“突破 4h 阻力”的 K 线取倒数，就是“跌破 4h 支撑”
	breakout := bollPumpFixtureFourHourResistanceBreakout()
	if !bollPumpFourHourResistanceBreakout(breakout, cfg).Triggered {
		t.Fatal("前提：原 K 线应判为突破阻力")
	}
	s = NewBollPumpScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"4h": bollPumpInvertBars(breakout)}}, nil, cfg)
	if b := s.shortFourHourBreakdown(ctx, "XYZUSDT"); !b.Triggered || b.Bonus != cfg.Resistance4HBreakoutBonus {
		t.Fatalf("取倒数后应判为跌破支撑: %+v", b)
	}
}
