package ingest

import (
	"sync"

	"github.com/shopspring/decimal"
)

type BucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

type BucketDelta struct {
	TakerBuyNotional  decimal.Decimal
	TakerSellNotional decimal.Decimal
	QuoteNotional     decimal.Decimal
	TradeCount        int64
	FirstTradeMS      *int64
	LastTradeMS       *int64
	OpenPrice         *decimal.Decimal
	ClosePrice        *decimal.Decimal
	HighPrice         *decimal.Decimal
	LowPrice          *decimal.Decimal
}

func NewBucketDelta() *BucketDelta {
	return &BucketDelta{
		TakerBuyNotional:  decimal.Zero,
		TakerSellNotional: decimal.Zero,
		QuoteNotional:     decimal.Zero,
	}
}

func (d *BucketDelta) Add(tsMS int64, price, buy, sell, total decimal.Decimal, count int64) {
	d.TakerBuyNotional = d.TakerBuyNotional.Add(buy)
	d.TakerSellNotional = d.TakerSellNotional.Add(sell)
	d.QuoteNotional = d.QuoteNotional.Add(total)
	d.TradeCount += count

	if d.FirstTradeMS == nil || tsMS < *d.FirstTradeMS {
		v := tsMS
		d.FirstTradeMS = &v
		p := price
		d.OpenPrice = &p
	}
	if d.LastTradeMS == nil || tsMS > *d.LastTradeMS {
		v := tsMS
		d.LastTradeMS = &v
		p := price
		d.ClosePrice = &p
	}
	if d.HighPrice == nil || price.GreaterThan(*d.HighPrice) {
		p := price
		d.HighPrice = &p
	}
	if d.LowPrice == nil || price.LessThan(*d.LowPrice) {
		p := price
		d.LowPrice = &p
	}
}

type TradeAggregator struct {
	buckets []string
	mu      sync.Mutex
	deltas  map[BucketKey]*BucketDelta
}

func NewTradeAggregator(buckets []string) *TradeAggregator {
	return &TradeAggregator{
		buckets: buckets,
		deltas:  make(map[BucketKey]*BucketDelta),
	}
}

func (a *TradeAggregator) AddTrade(market, symbol string, tsMS int64, price, takerBuyNotional, takerSellNotional, quoteNotional decimal.Decimal) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, bucket := range a.buckets {
		startMS, err := FloorBucketStartMS(tsMS, bucket)
		if err != nil {
			continue
		}
		k := BucketKey{Market: market, Symbol: symbol, Bucket: bucket, BucketStartMS: startMS}
		d, ok := a.deltas[k]
		if !ok {
			d = NewBucketDelta()
			a.deltas[k] = d
		}
		d.Add(tsMS, price, takerBuyNotional, takerSellNotional, quoteNotional, 1)
	}
}

type TradeDrainItem struct {
	Key   BucketKey
	Delta *BucketDelta
}

func (a *TradeAggregator) Drain() []TradeDrainItem {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.deltas) == 0 {
		return nil
	}
	out := make([]TradeDrainItem, 0, len(a.deltas))
	for k, d := range a.deltas {
		out = append(out, TradeDrainItem{Key: k, Delta: d})
	}
	a.deltas = make(map[BucketKey]*BucketDelta)
	return out
}

func (a *TradeAggregator) PendingBuckets() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.deltas)
}
