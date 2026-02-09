package runtime

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"coinmark/ingest-go/internal/binance"
	"coinmark/ingest-go/internal/config"
	"coinmark/ingest-go/internal/ingest"
	"coinmark/ingest-go/internal/store"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

type Service struct {
	cfg      *config.Config
	store    *store.Store
	binance  *binance.Client
	stats    *Stats
	tradeAgg *ingest.TradeAggregator
	obAgg    *ingest.OrderbookAggregator
}

func NewService(cfg *config.Config, st *store.Store, bc *binance.Client, stats *Stats, tradeAgg *ingest.TradeAggregator, obAgg *ingest.OrderbookAggregator) *Service {
	return &Service{
		cfg:      cfg,
		store:    st,
		binance:  bc,
		stats:    stats,
		tradeAgg: tradeAgg,
		obAgg:    obAgg,
	}
}

func (s *Service) FlushTradeLoop(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.FlushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			rows := s.tradeAgg.Drain()
			if len(rows) == 0 {
				continue
			}
			if _, err := s.store.UpsertTradeBuckets(ctx, rows, s.cfg.IngestDBBatchSize); err != nil {
				log.Printf("flush trade failed: %v", err)
				continue
			}
			s.stats.TradeFlushRows.Add(int64(len(rows)))
			s.stats.TradeFlushBatches.Add(1)
		}
	}
}

func (s *Service) FlushOrderbookLoop(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.FlushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			rows := s.obAgg.DrainClosed(ingest.UtcNowMS())
			if len(rows) == 0 {
				continue
			}
			if _, err := s.store.UpsertOrderbookBuckets(ctx, rows, s.cfg.IngestDBBatchSize); err != nil {
				log.Printf("flush orderbook failed: %v", err)
				continue
			}
			s.stats.OrderbookFlushRows.Add(int64(len(rows)))
			s.stats.OrderbookFlushBatches.Add(1)
		}
	}
}

func (s *Service) RuntimeReportLoop(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.RuntimeReportInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			snap := s.stats.SnapshotAndReset()
			log.Printf(
				"IngestRuntime trade_msg=%d depth_msg=%d trade_buckets=%d orderbook_buckets=%d trade_flush_rows=%d trade_flush_batches=%d orderbook_flush_rows=%d orderbook_flush_batches=%d",
				snap["nats_trade_msg"],
				snap["nats_depth_msg"],
				s.tradeAgg.PendingBuckets(),
				s.obAgg.PendingBuckets(),
				snap["trade_flush_rows"],
				snap["trade_flush_batches"],
				snap["orderbook_flush_rows"],
				snap["orderbook_flush_batches"],
			)
		}
	}
}

func (s *Service) FundingLoop(ctx context.Context) error {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.refreshFunding(ctx); err != nil {
			log.Printf("Funding refresh failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshFunding(ctx context.Context) error {
	rows, err := s.binance.GetFuturesPremiumIndexAll(ctx)
	if err != nil {
		return err
	}
	nowMS := time.Now().UTC().UnixMilli()
	values := make([]store.FundingSnapshotRow, 0, len(rows))
	for _, row := range rows {
		if row.Symbol == "" || row.LastFundingRate == "" || row.MarkPrice == "" {
			continue
		}
		lfr, err := decimal.NewFromString(row.LastFundingRate)
		if err != nil {
			continue
		}
		mp, err := decimal.NewFromString(row.MarkPrice)
		if err != nil {
			continue
		}
		values = append(values, store.FundingSnapshotRow{Symbol: row.Symbol, LastFundingRate: lfr, MarkPrice: mp, EventTimeMS: nowMS})
	}
	return s.store.UpsertFundingSnapshots(ctx, values)
}

func (s *Service) MarketCapLoop(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		if err := s.refreshMarketCaps(ctx); err != nil {
			log.Printf("Marketcap refresh failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshMarketCaps(ctx context.Context) error {
	items, err := s.binance.GetBinanceBapiProducts(ctx)
	if err != nil {
		return err
	}
	nowMS := time.Now().UTC().UnixMilli()
	type candidate struct {
		row store.MarketCapRow
		pm  string
		qv  decimal.Decimal
	}
	best := map[string]candidate{}
	for _, it := range items {
		base, _ := it["b"].(string)
		pm, _ := it["pm"].(string)
		if base == "" || (pm != "USDT" && pm != "USDC") {
			continue
		}
		price := toDecimal(it["c"])
		supply := toDecimal(it["cs"])
		qv := toDecimal(it["qv"])
		if !price.GreaterThan(decimal.Zero) || !supply.GreaterThan(decimal.Zero) {
			continue
		}
		cand := candidate{
			row: store.MarketCapRow{
				Asset:             base,
				PriceUSD:          price,
				CirculatingSupply: supply,
				MarketCapUSD:      price.Mul(supply),
				Source:            "binance_bapi_get_products",
				EventTimeMS:       nowMS,
			},
			pm: pm,
			qv: qv,
		}
		prev, ok := best[base]
		if !ok {
			best[base] = cand
			continue
		}
		if betterCandidate(cand, prev) {
			best[base] = cand
		}
	}
	values := make([]store.MarketCapRow, 0, len(best))
	for _, c := range best {
		values = append(values, c.row)
	}
	return s.store.UpsertMarketCaps(ctx, values)
}

func betterCandidate(a, b struct {
	row store.MarketCapRow
	pm  string
	qv  decimal.Decimal
}) bool {
	rank := func(pm string) int {
		if pm == "USDT" {
			return 0
		}
		return 1
	}
	ra, rb := rank(a.pm), rank(b.pm)
	if ra != rb {
		return ra < rb
	}
	return a.qv.GreaterThan(b.qv)
}

func toDecimal(v interface{}) decimal.Decimal {
	switch t := v.(type) {
	case string:
		d, _ := decimal.NewFromString(t)
		return d
	case float64:
		d, _ := decimal.NewFromString(strconv.FormatFloat(t, 'f', -1, 64))
		return d
	case int64:
		return decimal.NewFromInt(t)
	case int:
		return decimal.NewFromInt(int64(t))
	default:
		return decimal.Zero
	}
}

func (s *Service) OILoop(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.OIRefreshInterval())
	defer ticker.Stop()
	for {
		if err := s.refreshOI(ctx); err != nil {
			log.Printf("Open interest refresh failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshOI(ctx context.Context) error {
	symbols, err := s.binance.TopSymbolsByVolume(ctx, "swap", s.cfg.OIRefreshTopN)
	if err != nil {
		return err
	}
	premiumRows, err := s.binance.GetFuturesPremiumIndexAll(ctx)
	if err != nil {
		return err
	}
	markMap := make(map[string]decimal.Decimal, len(premiumRows))
	for _, row := range premiumRows {
		mp, err := decimal.NewFromString(row.MarkPrice)
		if err != nil {
			continue
		}
		markMap[row.Symbol] = mp
	}

	nowMS := time.Now().UTC().UnixMilli()
	values := make([]store.OISnapshotRow, 0, len(symbols))
	mu := sync.Mutex{}
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, 25)
	for _, sym := range symbols {
		sym := sym
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			oi, err := s.binance.GetFuturesOpenInterest(gctx, sym)
			if err != nil {
				return nil
			}
			mp, ok := markMap[sym]
			if !ok || !mp.GreaterThan(decimal.Zero) {
				return nil
			}
			row := store.OISnapshotRow{
				Symbol:        sym,
				OpenInterest:  oi,
				MarkPrice:     mp,
				OINotionalUSD: oi.Mul(mp),
				EventTimeMS:   nowMS,
			}
			mu.Lock()
			values = append(values, row)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return s.store.UpsertOISnapshots(ctx, values)
}

func intervalMS(interval string) int64 {
	switch interval {
	case "1m":
		return 60 * 1000
	case "15m":
		return 15 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	default:
		return 60 * 1000
	}
}

func (s *Service) BackfillOnce(ctx context.Context) error {
	if !s.cfg.BackfillEnable {
		return nil
	}
	log.Printf("Backfill start TopN=%d concurrency=%d", s.cfg.BackfillTopN, s.cfg.BackfillConcurrency)

	run := func(market string) {
		syms, err := s.binance.TopSymbolsByVolume(ctx, market, s.cfg.BackfillTopN)
		if err != nil {
			log.Printf("backfill top symbols %s failed: %v", market, err)
			return
		}
		intervals := []struct {
			name  string
			limit int
		}{
			{"1m", s.cfg.Backfill1mLimit},
		}

		for _, interval := range intervals {
			if interval.limit <= 0 {
				continue
			}
			rows := make([]ingest.TradeDrainItem, 0)
			for _, sym := range syms {
				klines, err := s.binance.GetKlines(ctx, market, sym, interval.name, interval.limit)
				if err != nil {
					continue
				}
				lastClosedStart := (ingest.UtcNowMS()/intervalMS(interval.name))*intervalMS(interval.name) - intervalMS(interval.name)
				for _, row := range klines {
					if len(row) < 11 {
						continue
					}
					openTime, ok := toInt64(row[0])
					if !ok || openTime > lastClosedStart {
						continue
					}
					closeTime, _ := toInt64(row[6])
					o := toDecimal(row[1])
					h := toDecimal(row[2])
					l := toDecimal(row[3])
					c := toDecimal(row[4])
					quoteNotional := toDecimal(row[7])
					trades, _ := toInt64(row[8])
					takerBuyQuote := toDecimal(row[10])
					takerSellQuote := quoteNotional.Sub(takerBuyQuote)
					if takerSellQuote.IsNegative() {
						takerSellQuote = decimal.Zero
					}
					openTimeCopy := openTime
					closeTimeCopy := closeTime
					oCopy := o
					cCopy := c
					hCopy := h
					lCopy := l
					rows = append(rows, ingest.TradeDrainItem{
						Key: ingest.BucketKey{Market: market, Symbol: sym, Bucket: interval.name, BucketStartMS: openTime},
						Delta: &ingest.BucketDelta{
							TakerBuyNotional:  takerBuyQuote,
							TakerSellNotional: takerSellQuote,
							QuoteNotional:     quoteNotional,
							TradeCount:        trades,
							FirstTradeMS:      &openTimeCopy,
							LastTradeMS:       &closeTimeCopy,
							OpenPrice:         &oCopy,
							ClosePrice:        &cCopy,
							HighPrice:         &hCopy,
							LowPrice:          &lCopy,
						},
					})
				}
			}
			if _, err := s.store.UpsertTradeBuckets(ctx, rows, s.cfg.IngestDBBatchSize); err != nil {
				log.Printf("backfill upsert failed market=%s interval=%s err=%v", market, interval.name, err)
			}
		}
	}

	if s.cfg.IngestEnableSpot {
		run("spot")
	}
	if s.cfg.IngestEnableSwap {
		run("swap")
	}
	log.Printf("Backfill completed")
	return nil
}

func toInt64(v interface{}) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case string:
		d, err := decimal.NewFromString(t)
		if err != nil {
			return 0, false
		}
		return d.IntPart(), true
	default:
		return 0, false
	}
}
