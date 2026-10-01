package runtime

import (
	"testing"

	"coinmark/ingest-go/internal/store"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestFillFuturesOnlyMarketCaps(t *testing.T) {
	best := map[string]store.MarketCapRow{
		"PEPE": {Asset: "PEPE", PriceUSD: d("0.00001"), CirculatingSupply: d("420000000000000"), MarketCapUSD: d("4200000000"), Source: "binance_bapi_compliance_symbol_list"},
		"ETH":  {Asset: "ETH", PriceUSD: d("3000"), CirculatingSupply: d("120000000"), MarketCapUSD: d("360000000000"), Source: "binance_bapi_compliance_symbol_list"},
	}
	alpha := []map[string]interface{}{
		{"symbol": "btw", "price": "1.40", "marketCap": "3900000000", "circulatingSupply": "2800000000"},
		{"symbol": "ZETA", "price": "0.20", "marketCap": "1000"},             // 同名但价格差太多，是别的币
		{"symbol": "ZETA", "price": "0.0532", "marketCap": "88000000"},       // 同名多链：取价格最接近的
		{"symbol": "UAI", "price": "0.32", "circulatingSupply": "360000000"}, // 没有 marketCap 时用 价格×流通量
		{"symbol": "ETH", "price": "3000", "marketCap": "1"},                 // 已有市值，不覆盖
		{"symbol": "NOFUT", "price": "1", "marketCap": "5000000"},            // 没有合约，不写
		{"symbol": "BADPX", "price": "2.0", "marketCap": "5000000"},          // 价格偏差 > 5%
	}
	marks := map[string]decimal.Decimal{
		"BTWUSDT":      d("1.397"),
		"ZETAUSDT":     d("0.0530"),
		"UAIUSDT":      d("0.321"),
		"ETHUSDT":      d("3001"),
		"BADPXUSDT":    d("1.5"),
		"1000PEPEUSDT": d("0.01002"),
		"1000CATUSDT":  d("0.5"), // CAT 没有现货市值，别名无从复制
	}

	fillFuturesOnlyMarketCaps(best, alpha, marks, 42)

	if r, ok := best["BTW"]; !ok || !r.MarketCapUSD.Equal(d("3900000000")) || r.Source != "binance_alpha_token_list" {
		t.Fatalf("BTW 应由 Alpha 补齐: %+v", r)
	}
	if r := best["ZETA"]; !r.MarketCapUSD.Equal(d("88000000")) {
		t.Fatalf("ZETA 应取价格最接近的那条: %+v", r)
	}
	if r := best["UAI"]; !r.MarketCapUSD.Equal(d("115200000")) {
		t.Fatalf("UAI 应为 价格×流通量: %+v", r)
	}
	if r := best["ETH"]; !r.MarketCapUSD.Equal(d("360000000000")) {
		t.Fatalf("已有市值不应被覆盖: %+v", r)
	}
	for _, a := range []string{"NOFUT", "BADPX", "1000CAT"} {
		if _, ok := best[a]; ok {
			t.Fatalf("%s 不应写入", a)
		}
	}
	r, ok := best["1000PEPE"]
	if !ok || !r.MarketCapUSD.Equal(d("4200000000")) || !r.PriceUSD.Equal(d("0.01")) || !r.CirculatingSupply.Equal(d("420000000000")) {
		t.Fatalf("1000PEPE 应复制 PEPE 的市值并换算价格/流通量: %+v", r)
	}
	if r.EventTimeMS != 42 || best["BTW"].EventTimeMS != 42 {
		t.Fatalf("补齐的行应使用本次刷新时间")
	}
}

func TestFillFuturesOnlyMarketCapsRejectsPrefixAliasOnPriceMismatch(t *testing.T) {
	best := map[string]store.MarketCapRow{
		"CAT": {Asset: "CAT", PriceUSD: d("0.00003"), CirculatingSupply: d("1"), MarketCapUSD: d("100000000"), Source: "binance_bapi_get_products"},
	}
	// 1000CAT 合约价格 0.5，而 1000×CAT 现货价只有 0.03：不是同一个币
	fillFuturesOnlyMarketCaps(best, nil, map[string]decimal.Decimal{"1000CATUSDT": d("0.5")}, 1)
	if _, ok := best["1000CAT"]; ok {
		t.Fatal("价格对不上时不应生成 1000 前缀别名")
	}
}
