package ingest

import (
	"sync"

	"github.com/shopspring/decimal"
)

type OrderbookBucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

type OrderbookBucketDelta struct {
	SpreadBPSSum          decimal.Decimal
	DepthImbalanceL5Sum   decimal.Decimal
	MicropriceShiftBPSSum decimal.Decimal
	WallPressureL5Sum     decimal.Decimal
	DepthImbalanceL20Sum  decimal.Decimal
	WallPressureL20Sum    decimal.Decimal
	SampleCount           int64

	TakerBuyNotional  decimal.Decimal
	TakerSellNotional decimal.Decimal

	DepletionEvents     int64
	ReplenishmentEvents int64

	L1DepthBaseline *decimal.Decimal
	DepletionActive bool
}

func NewOrderbookBucketDelta() *OrderbookBucketDelta {
	return &OrderbookBucketDelta{
		SpreadBPSSum:          decimal.Zero,
		DepthImbalanceL5Sum:   decimal.Zero,
		MicropriceShiftBPSSum: decimal.Zero,
		WallPressureL5Sum:     decimal.Zero,
		DepthImbalanceL20Sum:  decimal.Zero,
		WallPressureL20Sum:    decimal.Zero,
		TakerBuyNotional:      decimal.Zero,
		TakerSellNotional:     decimal.Zero,
	}
}

func (d *OrderbookBucketDelta) AddOrderbookSample(spreadBPS, depthImbalanceL5, micropriceShiftBPS, wallPressureL5, l1DepthNotional, depthImbalanceL20, wallPressureL20 decimal.Decimal) {
	d.SpreadBPSSum = d.SpreadBPSSum.Add(spreadBPS)
	d.DepthImbalanceL5Sum = d.DepthImbalanceL5Sum.Add(depthImbalanceL5)
	d.MicropriceShiftBPSSum = d.MicropriceShiftBPSSum.Add(micropriceShiftBPS)
	d.WallPressureL5Sum = d.WallPressureL5Sum.Add(wallPressureL5)
	d.DepthImbalanceL20Sum = d.DepthImbalanceL20Sum.Add(depthImbalanceL20)
	d.WallPressureL20Sum = d.WallPressureL20Sum.Add(wallPressureL20)
	d.SampleCount++

	if d.L1DepthBaseline == nil {
		v := l1DepthNotional
		d.L1DepthBaseline = &v
		return
	}
	baseline := *d.L1DepthBaseline
	if baseline.LessThanOrEqual(decimal.Zero) {
		v := l1DepthNotional
		d.L1DepthBaseline = &v
		return
	}

	dropThreshold := baseline.Mul(decimal.RequireFromString("0.60"))
	recoverThreshold := baseline.Mul(decimal.RequireFromString("0.85"))

	if !d.DepletionActive && l1DepthNotional.LessThan(dropThreshold) {
		d.DepletionEvents++
		d.DepletionActive = true
	} else if d.DepletionActive && l1DepthNotional.GreaterThanOrEqual(recoverThreshold) {
		d.ReplenishmentEvents++
		d.DepletionActive = false
	}

	newBaseline := baseline.Mul(decimal.RequireFromString("0.98")).Add(l1DepthNotional.Mul(decimal.RequireFromString("0.02")))
	d.L1DepthBaseline = &newBaseline
}

func (d *OrderbookBucketDelta) AddTrade(takerBuyNotional, takerSellNotional decimal.Decimal) {
	d.TakerBuyNotional = d.TakerBuyNotional.Add(takerBuyNotional)
	d.TakerSellNotional = d.TakerSellNotional.Add(takerSellNotional)
}

type OrderbookDrainItem struct {
	Key   OrderbookBucketKey
	Delta *OrderbookBucketDelta
}

type OrderbookAggregator struct {
	bucket       string
	bucketSizeMS int64
	mu           sync.Mutex
	deltas       map[OrderbookBucketKey]*OrderbookBucketDelta
}

func NewOrderbookAggregator(bucket string) *OrderbookAggregator {
	size, _ := BucketMS(bucket)
	return &OrderbookAggregator{
		bucket:       bucket,
		bucketSizeMS: size,
		deltas:       make(map[OrderbookBucketKey]*OrderbookBucketDelta),
	}
}

func (a *OrderbookAggregator) ensure(key OrderbookBucketKey) *OrderbookBucketDelta {
	d, ok := a.deltas[key]
	if ok {
		return d
	}
	d = NewOrderbookBucketDelta()
	a.deltas[key] = d
	return d
}

func (a *OrderbookAggregator) AddOrderbookSample(market, symbol string, tsMS int64, spreadBPS, depthImbalanceL5, micropriceShiftBPS, wallPressureL5, l1DepthNotional, depthImbalanceL20, wallPressureL20 decimal.Decimal) {
	startMS, err := FloorBucketStartMS(tsMS, a.bucket)
	if err != nil {
		return
	}
	k := OrderbookBucketKey{Market: market, Symbol: symbol, Bucket: a.bucket, BucketStartMS: startMS}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensure(k).AddOrderbookSample(spreadBPS, depthImbalanceL5, micropriceShiftBPS, wallPressureL5, l1DepthNotional, depthImbalanceL20, wallPressureL20)
}

func (a *OrderbookAggregator) AddTrade(market, symbol string, tsMS int64, takerBuyNotional, takerSellNotional decimal.Decimal) {
	startMS, err := FloorBucketStartMS(tsMS, a.bucket)
	if err != nil {
		return
	}
	k := OrderbookBucketKey{Market: market, Symbol: symbol, Bucket: a.bucket, BucketStartMS: startMS}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensure(k).AddTrade(takerBuyNotional, takerSellNotional)
}

func (a *OrderbookAggregator) DrainClosed(nowMS int64) []OrderbookDrainItem {
	cutoff, err := FloorBucketStartMS(nowMS, a.bucket)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]OrderbookDrainItem, 0)
	for key, delta := range a.deltas {
		if key.BucketStartMS < cutoff {
			out = append(out, OrderbookDrainItem{Key: key, Delta: delta})
			delete(a.deltas, key)
		}
	}
	return out
}

func (a *OrderbookAggregator) PendingBuckets() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.deltas)
}
