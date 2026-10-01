package marketstate

import (
	"math"
	"testing"

	"coinmark/api-go/internal/model"
)

const (
	t0  = int64(1790812800000) // 2026-10-01 00:00 UTC
	min = int64(60_000)
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestApplyBuildsMinuteBars(t *testing.T) {
	s := New(1440)
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 1000, Price: 10, Qty: 2, IsBuyerMaker: false}) // 主动买 20
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 2000, Price: 12, Qty: 1, IsBuyerMaker: true})  // 主动卖 12
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 3000, Price: 9, Qty: 1, IsBuyerMaker: false})  // 主动买 9
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + min + 1, Price: 11, Qty: 1})                   // 下一分钟

	bars := s.Minutes("swap", "AUSDT", t0, t0+min)
	if len(bars) != 2 {
		t.Fatalf("应有 2 根分钟线，got %d", len(bars))
	}
	b := bars[0]
	if b.StartMs != t0 || b.Open != 10 || b.High != 12 || b.Low != 9 || b.Close != 9 || b.Trades != 3 {
		t.Fatalf("OHLC/笔数不对: %+v", b)
	}
	if !near(b.BuyNotional, 29) || !near(b.SellNotional, 12) || !near(b.QuoteNotional, 41) {
		t.Fatalf("主动买卖拆分不对: %+v", b)
	}
	if bars[1].StartMs != t0+min || bars[1].Open != 11 {
		t.Fatalf("第二根不对: %+v", bars[1])
	}
	if got := s.Minutes("spot", "AUSDT", t0, t0+min); len(got) != 0 {
		t.Fatalf("不同市场应隔离: %+v", got)
	}
}

func TestCutIgnoresTradesAlreadyInBootstrap(t *testing.T) {
	s := New(1440)
	s.LoadBar("swap", "AUSDT", Bar{StartMs: t0, Open: 1, High: 2, Low: 1, Close: 2, BuyNotional: 100, QuoteNotional: 100, Trades: 5})
	s.SetCut(t0 + min)
	// 重放的旧消息：这一分钟已经在启动加载的数据里，不能再算一次
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 30_000, Price: 3, Qty: 10})
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + min + 5, Price: 3, Qty: 1})

	bars := s.Minutes("swap", "AUSDT", t0, t0+min)
	if len(bars) != 2 || bars[0].Trades != 5 || !near(bars[0].BuyNotional, 100) {
		t.Fatalf("切点前的成交不应重复计入: %+v", bars)
	}
	if bars[1].Trades != 1 {
		t.Fatalf("切点后的成交应计入: %+v", bars[1])
	}
}

func TestRingWrapDoesNotReturnStaleBars(t *testing.T) {
	s := New(10) // 10 分钟环
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0, Price: 1, Qty: 1})
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 10*min, Price: 2, Qty: 1}) // 覆盖同一个槽位
	if got := s.Minutes("swap", "AUSDT", t0, t0+5*min); len(got) != 0 {
		t.Fatalf("被覆盖的旧分钟不应返回: %+v", got)
	}
	got := s.Minutes("swap", "AUSDT", t0, t0+20*min)
	if len(got) != 1 || got[0].StartMs != t0+10*min {
		t.Fatalf("只应返回新的那根: %+v", got)
	}
	// 早于环窗口的乱序成交直接丢弃，不能覆盖新数据
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0, Price: 99, Qty: 1})
	if got := s.Minutes("swap", "AUSDT", t0, t0+20*min); len(got) != 1 || got[0].Open != 2 {
		t.Fatalf("过旧成交不应覆盖: %+v", got)
	}
}

func TestDayAggregate(t *testing.T) {
	s := New(1440)
	s.LoadBar("swap", "AUSDT", Bar{StartMs: t0 - min, Open: 5, High: 5, Low: 5, Close: 5, BuyNotional: 1000, QuoteNotional: 1000, Trades: 1}) // 昨天
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 10, Price: 10, Qty: 1})
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 5*min, Price: 14, Qty: 1, IsBuyerMaker: true})
	s.Apply(Trade{Market: "swap", Symbol: "AUSDT", TimeMs: t0 + 9*min, Price: 8, Qty: 1})

	d, ok := s.Day("swap", "AUSDT", t0)
	if !ok {
		t.Fatal("应有当日数据")
	}
	if d.Open != 10 || d.High != 14 || d.Low != 8 || d.Close != 8 || d.Trades != 3 {
		t.Fatalf("当日 OHLC 不对: %+v", d)
	}
	if !near(d.BuyNotional-d.SellNotional, 10+8-14) {
		t.Fatalf("当日净流入不对: %+v", d)
	}
	if _, ok := s.Day("swap", "BUSDT", t0); ok {
		t.Fatal("没有成交的币不应有当日数据")
	}
}

func TestReadyRequiresLoadAndFreshMessages(t *testing.T) {
	s := New(1440)
	if s.Ready(t0) {
		t.Fatal("未完成加载不应可用")
	}
	s.MarkLoaded()
	s.Touch(t0)
	if !s.Ready(t0 + 30_000) {
		t.Fatal("30 秒内收到过消息应可用")
	}
	if s.Ready(t0 + StaleAfterMs + 1) {
		t.Fatal("超过阈值没有消息应不可用")
	}
}

func TestParseTradePayload(t *testing.T) {
	tr, ok := parseTrade([]byte(`{"market":"SWAP","symbol":" btcusdt ","trade_time_ms":1790812800123,"event_time_ms":1790812800200,"price":"84000.5","qty":0.01,"is_buyer_maker":true}`))
	if !ok || tr.Market != "swap" || tr.Symbol != "BTCUSDT" || tr.TimeMs != 1790812800123 || tr.Price != 84000.5 || tr.Qty != 0.01 || !tr.IsBuyerMaker {
		t.Fatalf("解析不对: %+v ok=%v", tr, ok)
	}
	if tr, ok := parseTrade([]byte(`{"market":"spot","symbol":"X","event_time_ms":5,"price":1,"qty":"2"}`)); !ok || tr.TimeMs != 5 {
		t.Fatalf("缺 trade_time_ms 时应退回 event_time_ms: %+v ok=%v", tr, ok)
	}
	for _, bad := range []string{`{`, `{"market":"swap","symbol":"X","trade_time_ms":1,"price":"0","qty":1}`, `{"market":"swap","symbol":"","trade_time_ms":1,"price":1,"qty":1}`} {
		if _, ok := parseTrade([]byte(bad)); ok {
			t.Fatalf("应拒绝: %s", bad)
		}
	}
}

func TestLoadRowsSkipsIncompleteBuckets(t *testing.T) {
	s := New(1440)
	p := 1.5
	rows := []model.CHTradeRow{
		{Symbol: "AUSDT", BucketStartMs: t0, OpenPrice: &p, HighPrice: &p, LowPrice: &p, ClosePrice: &p, TakerBuyNotional: 3, QuoteNotional: 3, TradeCount: 2},
		{Symbol: "AUSDT", BucketStartMs: t0 + min, TradeCount: 1}, // 缺价格
	}
	if n := s.loadRows("swap", rows); n != 1 {
		t.Fatalf("应只加载 1 根，got %d", n)
	}
	if got := s.Minutes("swap", "AUSDT", t0, t0+min); len(got) != 1 || got[0].BuyNotional != 3 || got[0].Trades != 2 {
		t.Fatalf("加载结果不对: %+v", got)
	}
}
