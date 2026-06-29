package hub

import (
	"context"
	"log"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
)

type Runtime struct {
	Manager          *Manager
	Pub              *Publisher
	Stream           *AnomalyStream
	cfg              *config.Config
	store            *sqlite.Store
	ch               *chrepo.Client
	bn               *binance.Client
	stopCh           chan struct{}
	lastSQLiteVacuum time.Time
}

func NewRuntime(cfg *config.Config, store *sqlite.Store, ch *chrepo.Client, bn *binance.Client) *Runtime {
	mgr := NewManager(cfg.HubMaxConnections, cfg.HubHeartbeatIntervalSec, cfg.HubHeartbeatTimeoutSec)
	pub := NewPublisher(mgr, cfg.HubDedupeWindowSec, cfg.HubBroadcastMaxEventsPerSec)
	stream := NewAnomalyStream(store, pub, cfg.HubAnomalyScanBatchSize)

	return &Runtime{
		Manager: mgr, Pub: pub, Stream: stream,
		cfg: cfg, store: store, ch: ch, bn: bn,
		stopCh: make(chan struct{}),
	}
}

func (rt *Runtime) Start(ctx context.Context) {
	// periodic cleanup
	go rt.cleanupLoop(ctx)

	if !rt.cfg.HubEnabled {
		log.Println("hub: disabled")
		return
	}
	log.Println("hub: starting runtime")

	// heartbeat
	go rt.Manager.RunHeartbeat(rt.stopCh)

	// anomaly stream
	go rt.Stream.RunLoop(ctx, rt.cfg.HubAnomalyScanIntervalSec, rt.stopCh)

	// climax scan loop
	if rt.ch != nil {
		if rt.cfg.ClimaxScanEnabled {
			go rt.climaxLoop(ctx)
		}
		if rt.cfg.MarketYidongMinuteEnabled {
			go rt.marketYidongMinuteLoop(ctx)
		}
		if rt.cfg.MarketYidongVolumeEnabled {
			go rt.marketYidongVolumeLoop(ctx)
		}
		if rt.cfg.AbsorptionScanEnabled && rt.bn != nil {
			go rt.absorptionLoop(ctx)
		}
		if rt.cfg.BollPumpEnabled && rt.bn != nil {
			source := service.NewBinanceBollPumpSource(rt.bn, rt.cfg.BollPumpSymbolLimit)
			scanner := service.NewBollPumpScanner(source, rt.store, service.BollPumpConfigFromRuntime(rt.cfg))
			go scanner.Run(ctx, rt.stopCh)
		}
	}

	// depth fullscan
	if rt.cfg.DepthFullscanEnabled && rt.bn != nil {
		scanner := service.NewDepthScanner(rt.bn, rt.store, rt.cfg)
		go func() {
			select {
			case <-rt.stopCh:
				return
			default:
				scanner.Run(ctx)
			}
		}()
	}
}

func (rt *Runtime) Stop() {
	close(rt.stopCh)
	log.Println("hub: runtime stopped")
}

func (rt *Runtime) climaxLoop(ctx context.Context) {
	interval := rt.cfg.HubClimaxScanIntervalSec
	if interval < 10 {
		interval = 60
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-rt.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := service.ScanClimaxReversal(ctx, rt.ch, rt.store, "both",
				200,  // symbolLimit
				60,   // lookbackMinutes
				30,   // avgWindow
				5.0,  // climaxFactor
				10,   // reversalWindowMinutes
				0.30, // sellCascadeThreshold
				0.70, // buyCascadeThreshold
				3e6,  // minCascadeNotional
				0.15, // obImbalanceThreshold
				120,  // cooldownMinutes
			)
			if err != nil {
				log.Printf("hub: climax scan error: %v", err)
			} else if n, _ := result["insertedEvents"].(int); n > 0 {
				log.Printf("hub: climax scan inserted %d events", n)
			}
		}
	}
}

func (rt *Runtime) cleanupLoop(ctx context.Context) {
	interval := rt.cfg.AbsorptionSnapshotCleanupIntervalSec
	if interval < 60 {
		interval = 900
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-rt.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.doCleanup(ctx)
		}
	}
}

func (rt *Runtime) marketYidongMinuteLoop(ctx context.Context) {
	interval := rt.cfg.MarketYidongMinuteIntervalSec
	if interval < 30 {
		interval = 60
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rt.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			inserted, err := service.ScanMarketYidongMinute(ctx, rt.ch, rt.store, rt.bn, "swap", rt.cfg.AnomalyScanTopN)
			if err != nil {
				log.Printf("hub: market yidong minute scan error: %v", err)
			} else if inserted > 0 {
				log.Printf("hub: market yidong minute inserted %d events", inserted)
			}
		}
	}
}

func (rt *Runtime) marketYidongVolumeLoop(ctx context.Context) {
	interval := rt.cfg.MarketYidongVolumeIntervalSec
	if interval < 30 {
		interval = 60
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rt.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			inserted, err := service.ScanMarketYidongVolume(ctx, rt.ch, rt.store, rt.bn, "swap", rt.cfg.AnomalyScanTopN)
			if err != nil {
				log.Printf("hub: market yidong volume scan error: %v", err)
			} else if inserted > 0 {
				log.Printf("hub: market yidong volume inserted %d events", inserted)
			}
		}
	}
}

func (rt *Runtime) absorptionLoop(ctx context.Context) {
	interval := rt.cfg.AnomalyScanIntervalSec
	if interval < 30 {
		interval = 60
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-rt.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.RefreshAbsorptionSignalSnapshots(ctx, rt.ch, rt.bn, rt.store, "swap", rt.cfg.AnomalyScanTopN); err != nil {
				log.Printf("hub: absorption scan error: %v", err)
			}
		}
	}
}

func (rt *Runtime) doCleanup(ctx context.Context) {
	rt.cleanupSQLiteHistoryBuckets(ctx)

	// heatmap: 24h
	cutoff24h := time.Now().UnixMilli() - 24*60*60*1000
	if _, err := rt.store.DB.ExecContext(ctx, "DELETE FROM orderbook_heatmap_1m WHERE bucket_start_ms < ?", cutoff24h); err != nil {
		log.Printf("hub: heatmap cleanup error: %v", err)
	}

	// anomaly events: 30d
	cutoff30d := time.Now().UnixMilli() - 30*24*60*60*1000
	if _, err := rt.store.DB.ExecContext(ctx, "DELETE FROM anomaly_events WHERE event_time_ms < ?", cutoff30d); err != nil {
		log.Printf("hub: anomaly cleanup error: %v", err)
	}

	bollRetentionDays := rt.cfg.BollPumpRetentionDays
	if bollRetentionDays < 1 {
		bollRetentionDays = 30
	}
	if n, err := service.CleanupBollPumpSignals(ctx, rt.store, bollRetentionDays); err != nil {
		log.Printf("hub: boll pump cleanup error: %v", err)
	} else if n > 0 {
		log.Printf("hub: cleaned %d boll pump signals", n)
	}
	if n, err := service.ExpireStaleBollPumpStates(ctx, rt.store, 7); err != nil {
		log.Printf("hub: boll pump state expire error: %v", err)
	} else if n > 0 {
		log.Printf("hub: expired %d stale boll pump states", n)
	}

	// absorption snapshots
	hours := rt.cfg.AbsorptionSnapshotRetentionHours
	if hours < 1 {
		hours = 24
	}
	n, err := service.CleanupAbsorptionSnapshots(ctx, rt.store, hours)
	if err != nil {
		log.Printf("hub: absorption cleanup error: %v", err)
	} else if n > 0 {
		log.Printf("hub: cleaned %d absorption snapshots", n)
	}
}

func (rt *Runtime) cleanupSQLiteHistoryBuckets(ctx context.Context) {
	tradeDays := rt.cfg.TradeBucketRetentionDays
	if tradeDays < 1 {
		tradeDays = 7
	}
	orderbookDays := rt.cfg.OrderbookBucketRetentionDays
	if orderbookDays < 1 {
		orderbookDays = 3
	}

	nowMs := time.Now().UnixMilli()
	tradeCutoff := nowMs - int64(tradeDays)*24*60*60*1000
	orderbookCutoff := nowMs - int64(orderbookDays)*24*60*60*1000

	if res, err := rt.store.DB.ExecContext(ctx, "DELETE FROM trade_buckets WHERE bucket_start_ms < ?", tradeCutoff); err != nil {
		log.Printf("hub: trade bucket cleanup error: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("hub: cleaned %d sqlite trade buckets", n)
	}

	if res, err := rt.store.DB.ExecContext(ctx, "DELETE FROM orderbook_feature_buckets WHERE bucket_start_ms < ?", orderbookCutoff); err != nil {
		log.Printf("hub: orderbook bucket cleanup error: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("hub: cleaned %d sqlite orderbook buckets", n)
	}

	rt.reclaimSQLiteSpace(ctx)
}

func (rt *Runtime) reclaimSQLiteSpace(ctx context.Context) {
	if _, err := rt.store.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("hub: sqlite wal checkpoint error: %v", err)
	}

	interval := rt.cfg.SQLiteVacuumIntervalSec
	if interval < 1 {
		return
	}
	if interval < 3600 {
		interval = 3600
	}
	if !rt.lastSQLiteVacuum.IsZero() && time.Since(rt.lastSQLiteVacuum) < time.Duration(interval)*time.Second {
		return
	}
	if _, err := rt.store.DB.ExecContext(ctx, "VACUUM"); err != nil {
		log.Printf("hub: sqlite vacuum error: %v", err)
		return
	}
	rt.lastSQLiteVacuum = time.Now()
	log.Printf("hub: sqlite vacuum completed")
}
