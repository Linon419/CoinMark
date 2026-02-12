package hub

import (
	"context"
	"fmt"
	"log"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
)

type AnomalyStream struct {
	store     *sqlite.Store
	pub       *Publisher
	batchSize int
	lastID    int64
}

func NewAnomalyStream(store *sqlite.Store, pub *Publisher, batchSize int) *AnomalyStream {
	if batchSize < 1 {
		batchSize = 200
	}
	return &AnomalyStream{store: store, pub: pub, batchSize: batchSize}
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
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		evt := anomalyToHubEvent(r)
		s.pub.Publish(evt)
	}
}

func anomalyToHubEvent(r model.AnomalyEvent) HubEvent {
	level := "info"
	evtType := "ANOMALY_" + r.EventType
	switch r.EventType {
	case "breakout_up", "breakout_down":
		level = "warning"
		if r.EventType == "breakout_up" {
			evtType = "ANOMALY_BREAKOUT_UP"
		} else {
			evtType = "ANOMALY_BREAKOUT_DOWN"
		}
	case "volume_spike":
		evtType = "ANOMALY_VOLUME_SPIKE"
	case "amplitude_spike":
		evtType = "ANOMALY_AMPLITUDE_SPIKE"
	}

	minuteBucket := r.EventTimeMs / 60000
	dedupeKey := fmt.Sprintf("anomaly:%s:%s:%s:%d", r.Market, r.Symbol, r.EventType, minuteBucket)

	meta := make(map[string]interface{})
	meta["event_type"] = r.EventType
	meta["tf_signal"] = r.TfSignal
	if r.TfLevel != nil {
		meta["tf_level"] = *r.TfLevel
	}

	return HubEvent{
		ID:        fmt.Sprintf("ae-%d", r.ID),
		Type:      evtType,
		Level:     level,
		Title:     r.Title,
		Content:   string(r.Details),
		Symbol:    r.Symbol,
		Market:    r.Market,
		Ts:        r.EventTimeMs,
		Meta:      meta,
		DedupeKey: dedupeKey,
	}
}
