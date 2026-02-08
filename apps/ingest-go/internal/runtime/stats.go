package runtime

import "sync/atomic"

type Stats struct {
	TradeFlushRows        atomic.Int64
	TradeFlushBatches     atomic.Int64
	OrderbookFlushRows    atomic.Int64
	OrderbookFlushBatches atomic.Int64
	NATSTradeMsg          atomic.Int64
	NATSDepthMsg          atomic.Int64
}

func (s *Stats) SnapshotAndReset() map[string]int64 {
	return map[string]int64{
		"trade_flush_rows":        s.TradeFlushRows.Swap(0),
		"trade_flush_batches":     s.TradeFlushBatches.Swap(0),
		"orderbook_flush_rows":    s.OrderbookFlushRows.Swap(0),
		"orderbook_flush_batches": s.OrderbookFlushBatches.Swap(0),
		"nats_trade_msg":          s.NATSTradeMsg.Swap(0),
		"nats_depth_msg":          s.NATSDepthMsg.Swap(0),
	}
}
