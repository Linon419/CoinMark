package service

import (
	"math"
	"sort"
)

func ComputeBollPumpIndicators(bars []BollPumpBar, bollPeriod int, stdMult float64, atrPeriod int) []BollPumpIndicator {
	out := make([]BollPumpIndicator, len(bars))
	for i := range bars {
		if bollPeriod > 0 && i+1 >= bollPeriod {
			start := i + 1 - bollPeriod
			sum := 0.0
			for j := start; j <= i; j++ {
				sum += bars[j].Close
			}
			middle := sum / float64(bollPeriod)
			variance := 0.0
			for j := start; j <= i; j++ {
				d := bars[j].Close - middle
				variance += d * d
			}
			std := math.Sqrt(variance / float64(bollPeriod))
			upper := middle + stdMult*std
			lower := middle - stdMult*std
			bw := 0.0
			if middle != 0 {
				bw = (upper - lower) / middle
			}
			out[i].Middle = middle
			out[i].Upper = upper
			out[i].Lower = lower
			out[i].Bandwidth = bw
			out[i].ValidBoll = true
		}
		if atrPeriod > 0 && i+1 >= atrPeriod {
			start := i + 1 - atrPeriod
			sumTR := 0.0
			for j := start; j <= i; j++ {
				sumTR += bollPumpTrueRange(bars, j)
			}
			atr := sumTR / float64(atrPeriod)
			out[i].ATR14 = atr
			if bars[i].Close > 0 {
				out[i].ATRRatio = atr / bars[i].Close
			}
			out[i].ValidATR = true
		}
	}
	return out
}

func bollPumpTrueRange(bars []BollPumpBar, idx int) float64 {
	hl := bars[idx].High - bars[idx].Low
	if idx == 0 {
		return hl
	}
	prevClose := bars[idx-1].Close
	hc := math.Abs(bars[idx].High - prevClose)
	lc := math.Abs(bars[idx].Low - prevClose)
	return math.Max(hl, math.Max(hc, lc))
}

func bollPumpPercentile(values []float64, p float64) float64 {
	clean := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return 0
	}
	sort.Float64s(clean)
	if p <= 0 {
		return clean[0]
	}
	if p >= 1 {
		return clean[len(clean)-1]
	}
	pos := p * float64(len(clean)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return clean[lo]
	}
	frac := pos - float64(lo)
	return clean[lo]*(1-frac) + clean[hi]*frac
}
