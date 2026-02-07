package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"coinmark/collector-go/internal/collector"
	"coinmark/collector-go/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	c, err := collector.New(cfg)
	if err != nil {
		log.Fatalf("new collector failed: %v", err)
	}
	if err := c.Run(ctx); err != nil {
		log.Fatalf("collector run failed: %v", err)
	}
}
