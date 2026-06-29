package service

import (
	"math"
	"testing"
)

func TestBollPumpIndicatorsUseClosedBars(t *testing.T) {
	bars := make([]BollPumpBar, 0, 25)
	for i := 0; i < 25; i++ {
		closePrice := 100 + float64(i)
		bars = append(bars, BollPumpBar{
			OpenTimeMs:  int64(i) * 60000,
			CloseTimeMs: int64(i+1)*60000 - 1,
			Open:        closePrice - 0.5,
			High:        closePrice + 1,
			Low:         closePrice - 1,
			Close:       closePrice,
			Volume:      100,
			QuoteVolume: 1000,
			Closed:      true,
		})
	}

	out := ComputeBollPumpIndicators(bars, 20, 2, 14)
	last := out[len(out)-1]

	if math.Abs(last.Middle-114.5) > 1e-9 {
		t.Fatalf("middle = %.10f, want 114.5", last.Middle)
	}
	if math.Abs(last.Upper-126.0325625947) > 1e-6 {
		t.Fatalf("upper = %.10f, want 126.0325625947", last.Upper)
	}
	if math.Abs(last.Lower-102.9674374053) > 1e-6 {
		t.Fatalf("lower = %.10f, want 102.9674374053", last.Lower)
	}
	if math.Abs(last.Bandwidth-0.2014430148) > 1e-6 {
		t.Fatalf("bandwidth = %.10f, want 0.2014430148", last.Bandwidth)
	}
	if math.Abs(last.ATR14-2.0) > 1e-9 {
		t.Fatalf("atr = %.10f, want 2.0", last.ATR14)
	}
}

func TestBollPumpPercentile(t *testing.T) {
	got := bollPumpPercentile([]float64{5, 1, 3, 2, 4}, 0.30)
	if math.Abs(got-2.2) > 1e-9 {
		t.Fatalf("percentile = %.10f, want 2.2", got)
	}
}
