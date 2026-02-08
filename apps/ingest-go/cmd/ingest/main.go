package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"coinmark/ingest-go/internal/binance"
	"coinmark/ingest-go/internal/config"
	"coinmark/ingest-go/internal/ingest"
	natsconsumer "coinmark/ingest-go/internal/nats"
	"coinmark/ingest-go/internal/runtime"
	"coinmark/ingest-go/internal/store"
	"golang.org/x/sync/errgroup"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.New(ctx, cfg.DatabaseURL, cfg.IngestClickHouseURL)
	if err != nil {
		log.Fatalf("connect db failed: %v", err)
	}
	defer st.Close()

	tradeAgg := ingest.NewTradeAggregator([]string{"1m", "15m", "1h", "4h", "1d"})
	obAgg := ingest.NewOrderbookAggregator("1m")
	stats := &runtime.Stats{}

	binanceClient := binance.NewClient(cfg)
	svc := runtime.NewService(cfg, st, binanceClient, stats, tradeAgg, obAgg)
	consumer := natsconsumer.New(cfg, stats)

	log.Printf(
		"IngestSource hard-cut nats url=%s stream=%s trade_subject=%s depth_subject=%s depth_enabled=%v",
		cfg.NATSURL,
		cfg.NATSStreamRaw,
		cfg.NATSSubjectTrade,
		cfg.NATSSubjectDepth,
		cfg.IngestEnableDepth,
	)

	g, gctx := errgroup.WithContext(ctx)

	if cfg.IngestEnableSpot {
		g.Go(func() error { return consumer.RunMarket(gctx, "spot", tradeAgg, obAgg) })
	}
	if cfg.IngestEnableSwap {
		g.Go(func() error { return consumer.RunMarket(gctx, "swap", tradeAgg, obAgg) })
	}

	g.Go(func() error { return svc.FlushTradeLoop(gctx) })
	g.Go(func() error { return svc.FlushOrderbookLoop(gctx) })
	g.Go(func() error { return svc.FundingLoop(gctx) })
	g.Go(func() error { return svc.MarketCapLoop(gctx) })
	g.Go(func() error { return svc.OILoop(gctx) })
	g.Go(func() error { return svc.RuntimeReportLoop(gctx) })
	g.Go(func() error { return svc.BackfillOnce(gctx) })

	if err := g.Wait(); err != nil && gctx.Err() == nil {
		log.Fatalf("ingest-go stopped with error: %v", err)
	}
	log.Printf("ingest-go stopped")
}
