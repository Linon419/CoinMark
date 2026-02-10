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
	MicropriceShiftBPS decimal.Decimal
	L1DepthNotional    decimal.Decimal
	DepthImbalanceL20  decimal.Decimal
	WallPressureL20    decimal.Decimal
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

	bidNotionalL20 := decimal.Zero
	askNotionalL20 := decimal.Zero
	maxBidNotionalL20 := decimal.Zero
	maxAskNotionalL20 := decimal.Zero

	for i := 0; i < len(bids) && i < 20; i++ {
		notional := bids[i].Price.Mul(bids[i].Qty)
		bidNotionalL20 = bidNotionalL20.Add(notional)
		if notional.GreaterThan(maxBidNotionalL20) {
			maxBidNotionalL20 = notional
		}
	}
	for i := 0; i < len(asks) && i < 20; i++ {
		notional := asks[i].Price.Mul(asks[i].Qty)
		askNotionalL20 = askNotionalL20.Add(notional)
		if notional.GreaterThan(maxAskNotionalL20) {
			maxAskNotionalL20 = notional
		}
	}

	depthDenomL20 := bidNotionalL20.Add(askNotionalL20)
	depthImbalanceL20 := decimal.Zero
	if depthDenomL20.GreaterThan(decimal.Zero) {
		depthImbalanceL20 = bidNotionalL20.Sub(askNotionalL20).Div(depthDenomL20)
	}

	microDenom := bid1.Qty.Add(ask1.Qty)
	micropriceShiftBPS := decimal.Zero
	if microDenom.GreaterThan(decimal.Zero) {
		microprice := ask1.Price.Mul(bid1.Qty).Add(bid1.Price.Mul(ask1.Qty)).Div(microDenom)
		micropriceShiftBPS = microprice.Sub(mid).Div(mid).Mul(decimal.NewFromInt(10000))
	}

	wallDenomL20 := maxBidNotionalL20.Add(maxAskNotionalL20)
	wallPressureL20 := decimal.Zero
	if wallDenomL20.GreaterThan(decimal.Zero) {
		wallPressureL20 = maxBidNotionalL20.Sub(maxAskNotionalL20).Div(wallDenomL20)
	}

	l1DepthNotional := bid1.Price.Mul(bid1.Qty).Add(ask1.Price.Mul(ask1.Qty))
	return &DepthFeatures{
		SpreadBPS:          spreadBPS,
		MicropriceShiftBPS: micropriceShiftBPS,
		L1DepthNotional:    l1DepthNotional,
		DepthImbalanceL20:  depthImbalanceL20,
		WallPressureL20:    wallPressureL20,
	}, true
}
