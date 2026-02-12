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
	Manager  *Manager
	Pub      *Publisher
	Stream   *AnomalyStream
	cfg      *config.Config
	store    *sqlite.Store
	ch       *chrepo.Client
	bn       *binance.Client
	stopCh   chan struct{}
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
		go rt.climaxLoop(ctx)
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

	// periodic cleanup
	go rt.cleanupLoop(ctx)
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
				200,   // symbolLimit
				60,    // lookbackMinutes
				30,    // avgWindow
				5.0,   // climaxFactor
				10,    // reversalWindowMinutes
				0.30,  // sellCascadeThreshold
				0.70,  // buyCascadeThreshold
				3e6,   // minCascadeNotional
				0.15,  // obImbalanceThreshold
				120,   // cooldownMinutes
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

func (rt *Runtime) doCleanup(ctx context.Context) {
	// heatmap: 24h
	cutoff24h := time.Now().UnixMilli() - 24*60*60*1000
	rt.store.DB.ExecContext(ctx, "DELETE FROM orderbook_heatmap_1m WHERE bucket_start_ms < ?", cutoff24h)

	// anomaly events: 30d
	cutoff30d := time.Now().UnixMilli() - 30*24*60*60*1000
	rt.store.DB.ExecContext(ctx, "DELETE FROM anomaly_events WHERE event_time_ms < ?", cutoff30d)

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
