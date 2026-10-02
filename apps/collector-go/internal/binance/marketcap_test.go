package binance

import (
	"reflect"
	"testing"
)

func TestRankMarketCapRows(t *testing.T) {
	rows := []map[string]any{
		{"b": "NVDAB", "pm": "USDT", "c": "180", "cs": "1e10", "tags": []any{"bStocks"}}, // 美股代币：排除
		{"b": "ETH", "pm": "USDT", "c": "3000", "cs": "120000000"},
		{"b": "BTC", "pm": "USDC", "c": "84000", "cs": "19000000", "qv": "1"},
		{"b": "BTC", "pm": "USDT", "c": "84000", "cs": "19000000", "qv": "1", "tags": []any{"Payments"}},
		{"b": "DOGE", "pm": "BTC", "c": "0.1", "cs": "1e11"}, // 非 USDT/USDC 计价：跳过
		{"b": "BAD", "pm": "USDT", "c": "0", "cs": "1"},      // 无效价格：跳过
	}
	got := rankMarketCapRows(rows)
	if want := []string{"BTCUSDT", "ETHUSDT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
