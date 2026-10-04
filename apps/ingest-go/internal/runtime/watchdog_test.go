package runtime

import (
	"reflect"
	"testing"

	"coinmark/ingest-go/internal/store"
)

func TestCollectWatchdogIssuesSkipsConfirmedEmptyMinutes(t *testing.T) {
	const m = int64(60_000)
	px := 1.0
	good := store.TradeBucketHealthRow{TradeCount: 3, QuoteNotional: 10, OpenPrice: &px, ClosePrice: &px, HighPrice: &px, LowPrice: &px}
	byTs := map[int64]store.TradeBucketHealthRow{
		0 * m: good,
		1 * m: {TradeCount: 0}, // 异常行
		// 2m 缺失；3m 缺失但 REST 已确认没有成交
		4 * m: good,
	}
	issues, missing, abnormal := collectWatchdogIssues(byTs, 0, 4*m, map[int64]struct{}{3 * m: {}})
	if !reflect.DeepEqual(issues, []int64{1 * m, 2 * m}) || missing != 1 || abnormal != 1 {
		t.Fatalf("issues=%v missing=%d abnormal=%d", issues, missing, abnormal)
	}
}

func TestEmptyKlineMinutes(t *testing.T) {
	k := func(open int64, quote string, trades int64) []interface{} {
		return []interface{}{float64(open), "1", "1", "1", "1", "0", float64(open + 59_999), quote, float64(trades), "0", "0", "0"}
	}
	klines := [][]interface{}{
		k(0, "100", 5),
		k(60_000, "0", 0),  // 没有成交
		k(120_000, "0", 0), // 没有成交，但超出范围
	}
	if got := emptyKlineMinutes(klines, 0, 60_000); !reflect.DeepEqual(got, []int64{60_000}) {
		t.Fatalf("empty minutes = %v", got)
	}
}
