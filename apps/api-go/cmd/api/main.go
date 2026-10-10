package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/handler"
	"coinmark/api-go/internal/hub"
	"coinmark/api-go/internal/marketstate"
	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
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

	if cfg.PprofAddr != "" {
		go servePprof(cfg.PprofAddr)
	}

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

	// 内存市场状态
	var marketState *marketstate.State
	if cfg.MarketStateEnabled && chClient != nil {
		marketState = marketstate.New(1440)
		go runMarketState(ctx, cfg, chClient, marketState)
	}

	// Hub runtime
	hubRT := hub.NewRuntime(cfg, sqliteStore, chClient, bnClient)
	hubRT.SetMarketState(marketState)
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

// runMarketState 运行内存市场状态；出错（如 NATS 不可用）时 30 秒后重新加载，期间相关扫描自动回退到 ClickHouse。
func runMarketState(ctx context.Context, cfg *config.Config, ch *chrepo.Client, ms *marketstate.State) {
	msCfg := marketstate.Config{
		NATSURL: cfg.MarketStateNATSURL,
		Stream:  cfg.MarketStateNATSStream,
		Subject: cfg.MarketStateNATSSubject,
		Markets: []string{"swap", "spot"},
	}
	load := func(ctx context.Context, market string, fromMs, toMs int64) ([]model.CHTradeRow, error) {
		return ch.QueryTradeBuckets(ctx, market, "", nil, "1m", fromMs, toMs, "asc", 0)
	}
	for {
		if err := ms.Run(ctx, msCfg, load); err != nil {
			log.Printf("marketstate: %v (30s 后重试)", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// servePprof 单独端口提供 /debug/pprof（不挂在对外的 API 路由上），用于查内存占用。
func servePprof(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	log.Printf("pprof: listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("pprof: %v", err)
	}
}
