package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/handler"
	"coinmark/api-go/internal/hub"
	"coinmark/api-go/internal/migration"
	chrepo "coinmark/api-go/internal/repo/ch"
	redisrepo "coinmark/api-go/internal/repo/redis"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// SQLite
	sqliteStore, err := sqlite.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	defer sqliteStore.Close()

	if err := migration.Migrate(ctx, sqliteStore); err != nil {
		log.Fatalf("migration: %v", err)
	}
	log.Println("sqlite: migrations applied")

	// ClickHouse
	var chClient *chrepo.Client
	if cfg.ClickHouseURL != "" {
		chClient, err = chrepo.New(cfg.ClickHouseURL, cfg.ClickHouseDB, cfg.ClickHouseUser, cfg.ClickHousePassword)
		if err != nil {
			log.Fatalf("clickhouse: %v", err)
		}
		log.Println("clickhouse: connected")
	}

	// Redis
	redisStore, err := redisrepo.Open(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redisStore.Close()
	log.Println("redis: connected")

	// Binance
	bnClient := binance.NewClient()
	log.Println("binance: client ready")

	// Hub runtime
	hubRT := hub.NewRuntime(cfg, sqliteStore, chClient, bnClient)
	hubRT.Start(ctx)
	defer hubRT.Stop()

	// Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	handler.RegisterRoutes(r, &handler.Deps{
		Cfg:   cfg,
		Store: sqliteStore,
		CH:    chClient,
		BN:    bnClient,
		Hub:   hubRT,
	})

	_ = redisStore

	// Telegram
	tgStopCh := make(chan struct{})
	telegram.Start(ctx, cfg, sqliteStore, chClient, bnClient, redisStore, tgStopCh)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		log.Printf("api: listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("api: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("api: shutting down...")
	close(tgStopCh)

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("api: shutdown error: %v", err)
	}
	log.Println("api: stopped")
}
