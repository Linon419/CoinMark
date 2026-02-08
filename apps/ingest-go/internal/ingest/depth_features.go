package ingest

import (
	"github.com/shopspring/decimal"
)

type DepthLevel struct {
	Price decimal.Decimal
	Qty   decimal.Decimal
}

type DepthFeatures struct {
	SpreadBPS          decimal.Decimal
	DepthImbalanceL5   decimal.Decimal
	MicropriceShiftBPS decimal.Decimal
	WallPressureL5     decimal.Decimal
	L1DepthNotional    decimal.Decimal
}

func BuildDepthFeatures(bids, asks []DepthLevel) (*DepthFeatures, bool) {
	if len(bids) == 0 || len(asks) == 0 {
		return nil, false
	}
	bid1 := bids[0]
	ask1 := asks[0]
	mid := bid1.Price.Add(ask1.Price).Div(decimal.NewFromInt(2))
	if !mid.GreaterThan(decimal.Zero) {
		return nil, false
	}

	spreadBPS := ask1.Price.Sub(bid1.Price).Div(mid).Mul(decimal.NewFromInt(10000))

	bidNotionalL5 := decimal.Zero
	askNotionalL5 := decimal.Zero
	maxBidNotionalL5 := decimal.Zero
	maxAskNotionalL5 := decimal.Zero

	for i := 0; i < len(bids) && i < 5; i++ {
		notional := bids[i].Price.Mul(bids[i].Qty)
		bidNotionalL5 = bidNotionalL5.Add(notional)
		if notional.GreaterThan(maxBidNotionalL5) {
			maxBidNotionalL5 = notional
		}
	}
	for i := 0; i < len(asks) && i < 5; i++ {
		notional := asks[i].Price.Mul(asks[i].Qty)
		askNotionalL5 = askNotionalL5.Add(notional)
		if notional.GreaterThan(maxAskNotionalL5) {
			maxAskNotionalL5 = notional
		}
	}

	depthDenom := bidNotionalL5.Add(askNotionalL5)
	depthImbalanceL5 := decimal.Zero
	if depthDenom.GreaterThan(decimal.Zero) {
		depthImbalanceL5 = bidNotionalL5.Sub(askNotionalL5).Div(depthDenom)
	}

	microDenom := bid1.Qty.Add(ask1.Qty)
	micropriceShiftBPS := decimal.Zero
	if microDenom.GreaterThan(decimal.Zero) {
		microprice := ask1.Price.Mul(bid1.Qty).Add(bid1.Price.Mul(ask1.Qty)).Div(microDenom)
		micropriceShiftBPS = microprice.Sub(mid).Div(mid).Mul(decimal.NewFromInt(10000))
	}

	wallDenom := maxBidNotionalL5.Add(maxAskNotionalL5)
	wallPressureL5 := decimal.Zero
	if wallDenom.GreaterThan(decimal.Zero) {
		wallPressureL5 = maxBidNotionalL5.Sub(maxAskNotionalL5).Div(wallDenom)
	}

	l1DepthNotional := bid1.Price.Mul(bid1.Qty).Add(ask1.Price.Mul(ask1.Qty))
	return &DepthFeatures{
		SpreadBPS:          spreadBPS,
		DepthImbalanceL5:   depthImbalanceL5,
		MicropriceShiftBPS: micropriceShiftBPS,
		WallPressureL5:     wallPressureL5,
		L1DepthNotional:    l1DepthNotional,
	}, true
}
