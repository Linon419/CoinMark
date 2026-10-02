package service

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// ZECUSDT 4h，最后一根 2026-10-01 16:00 UTC：上涨趋势中缩口并跌到下轨（用户给的截图）。
func loadZECSqueezeBars(t *testing.T) []BollPumpBar {
	t.Helper()
	raw, err := os.ReadFile("testdata/boll_squeeze_zec_4h.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		OpenTimeMs []int64   `json:"open_time_ms"`
		Close      []float64 `json:"close"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	bars := make([]BollPumpBar, len(fx.Close))
	for i, c := range fx.Close {
		bars[i] = BollPumpBar{OpenTimeMs: fx.OpenTimeMs[i], Open: c, High: c, Low: c, Close: c, Closed: true}
	}
	return bars
}

func TestEvaluateBollSqueezeHitsZECPullbackToLowerBand(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	hit, ok := EvaluateBollSqueeze(bars)
	if !ok {
		t.Fatal("expected hit on ZEC 4h 2026-10-01 16:00")
	}
	// TradingView 截图：EMA200 1268.11、下轨 1327.61（截图时已多走了几根）
	if math.Abs(hit.EMA200-1267.05) > 1 || math.Abs(hit.Lower-1337.67) > 1 {
		t.Fatalf("ema200=%.2f lower=%.2f", hit.EMA200, hit.Lower)
	}
	if hit.PercentB > 0 || hit.PercentB < -0.2 {
		t.Fatalf("percentB=%.3f", hit.PercentB)
	}
}

func TestEvaluateBollSqueezeSkipsWhenPriceNotNearLowerBand(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	// 10-01 00:00 收盘在 %B 0.37，还没回到下轨附近
	if _, ok := EvaluateBollSqueeze(bars[:len(bars)-4]); ok {
		t.Fatal("expected no hit before price reached lower band")
	}
}

func TestEvaluateBollSqueezeAllowsPercentBUpTo035(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	// 10-01 08:00 收盘 %B 0.31：缩口中回落到下轨上方不远，也算
	hit, ok := EvaluateBollSqueeze(bars[:len(bars)-2])
	if !ok || hit.PercentB < 0.25 || hit.PercentB > 0.35 {
		t.Fatalf("ok=%v percentB=%.3f", ok, hit.PercentB)
	}
}

func TestEvaluateBollSqueezeSkipsDowntrend(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	// 价格镜像翻转：形态相同但趋势向下
	for i := range bars {
		c := 3000 - bars[i].Close
		bars[i].Open, bars[i].High, bars[i].Low, bars[i].Close = c, c, c, c
	}
	if _, ok := EvaluateBollSqueeze(bars); ok {
		t.Fatal("expected no hit in downtrend")
	}
}

func TestEvaluateBollSqueezeNeedsEnoughHistoryForEMA200(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	if _, ok := EvaluateBollSqueeze(bars[len(bars)-240:]); ok {
		t.Fatal("expected no hit with only 240 bars")
	}
}
