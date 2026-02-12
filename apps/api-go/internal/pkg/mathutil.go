package pkg

import "math"

func FloorBucketStartMs(tsMs int64, bucketMs int64) int64 {
	return (tsMs / bucketMs) * bucketMs
}

func BucketStart1m(tsMs int64) int64 {
	return FloorBucketStartMs(tsMs, 60_000)
}

// LinReg performs ordinary least-squares linear regression.
// Returns slope, intercept, r-squared.
func LinReg(x, y []float64) (slope, intercept, r2 float64) {
	n := len(x)
	if n < 2 || len(y) != n {
		return 0, 0, 0
	}
	var sx, sy, sxx, sxy, syy float64
	fn := float64(n)
	for i := 0; i < n; i++ {
		sx += x[i]
		sy += y[i]
		sxx += x[i] * x[i]
		sxy += x[i] * y[i]
		syy += y[i] * y[i]
	}
	denom := fn*sxx - sx*sx
	if denom == 0 {
		return 0, sy / fn, 0
	}
	slope = (fn*sxy - sx*sy) / denom
	intercept = (sy - slope*sx) / fn

	ssTot := syy - (sy*sy)/fn
	ssRes := 0.0
	for i := 0; i < n; i++ {
		pred := slope*x[i] + intercept
		diff := y[i] - pred
		ssRes += diff * diff
	}
	if ssTot == 0 {
		r2 = 1
	} else {
		r2 = 1 - ssRes/ssTot
	}
	return slope, intercept, r2
}

func MaxFloat(a, b float64) float64 {
	return math.Max(a, b)
}

func MinFloat(a, b float64) float64 {
	return math.Min(a, b)
}

func MaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func MinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func Clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func IsFinite(v float64) bool {
	return !math.IsInf(v, 0) && !math.IsNaN(v)
}
