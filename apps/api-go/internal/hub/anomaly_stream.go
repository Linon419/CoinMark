package hub

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

const hubNotifyWhaleWallFar = "whale_wall_far"

type AnomalyStream struct {
	store      *sqlite.Store
	pub        *Publisher
	batchSize  int
	lastID     int64
	cooldownMs int64
	recentSent map[string]int64
}

func NewAnomalyStream(store *sqlite.Store, pub *Publisher, batchSize int) *AnomalyStream {
	if batchSize < 1 {
		batchSize = 200
	}
	return &AnomalyStream{
		store:      store,
		pub:        pub,
		batchSize:  batchSize,
		cooldownMs: 10 * 60 * 1000,
		recentSent: make(map[string]int64),
	}
}

func (s *AnomalyStream) RunLoop(ctx context.Context, intervalSec int, stopCh <-chan struct{}) {
	if intervalSec < 1 {
		intervalSec = 2
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

func (s *AnomalyStream) pollOnce(ctx context.Context) {
	var rows []model.AnomalyEvent
	q := `SELECT * FROM anomaly_events WHERE id > ? ORDER BY id ASC LIMIT ?`
	if err := s.store.SelectContext(ctx, &rows, q, s.lastID, s.batchSize); err != nil {
		log.Printf("anomaly_stream: query error: %v", err)
		return
	}
	for _, r := range rows {
		if r.ID > s.lastID {
			s.lastID = r.ID
		}
		if !shouldNotifyEventType(r.EventType) {
			continue
		}
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		if s.hitCooldown(r.EventType, r.Symbol) {
			continue
		}
		evt := anomalyToHubEvent(r)
		s.pub.Publish(evt)
	}
}

func anomalyToHubEvent(r model.AnomalyEvent) HubEvent {
	level := toHubEventLevel(r.EventType)
	evtType := toHubEventType(r.EventType)

	minuteBucket := r.EventTimeMs / 60000
	dedupeKey := fmt.Sprintf("anomaly:%s:%s:%s:%d", r.Market, r.Symbol, evtType, minuteBucket)

	meta := make(map[string]interface{})
	meta["event_type"] = r.EventType
	meta["tf_signal"] = r.TfSignal
	if r.TfLevel != nil {
		meta["tf_level"] = *r.TfLevel
	}

	content := strings.TrimSpace(r.Title)
	if content == "" {
		content = string(r.Details)
	}

	return HubEvent{
		ID:        fmt.Sprintf("ae-%d", r.ID),
		Type:      evtType,
		Level:     level,
		Title:     r.Title,
		Content:   content,
		Symbol:    r.Symbol,
		Market:    r.Market,
		Ts:        r.EventTimeMs,
		Meta:      meta,
		DedupeKey: dedupeKey,
	}
}

func shouldNotifyEventType(eventType string) bool {
	return strings.EqualFold(strings.TrimSpace(eventType), hubNotifyWhaleWallFar)
}

func (s *AnomalyStream) hitCooldown(eventType, symbol string) bool {
	if s.cooldownMs <= 0 {
		return false
	}
	nowMs := time.Now().UnixMilli()
	key := strings.ToUpper(strings.TrimSpace(eventType)) + "|" + strings.ToUpper(strings.TrimSpace(symbol))
	if last, ok := s.recentSent[key]; ok && nowMs-last < s.cooldownMs {
		return true
	}
	s.recentSent[key] = nowMs

	// Keep the map bounded for long-running processes.
	if len(s.recentSent) > 2000 {
		cutoff := nowMs - s.cooldownMs
		for k, last := range s.recentSent {
			if last < cutoff {
				delete(s.recentSent, k)
			}
		}
	}
	return false
}
