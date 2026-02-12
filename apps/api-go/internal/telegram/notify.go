package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
	redisrepo "coinmark/api-go/internal/repo/redis"
)

type AnomalyNotifier struct {
	bot             interface{ Send(to interface{}, what interface{}, opts ...interface{}) (*interface{}, error) }
	store           *sqlite.Store
	redis           *redisrepo.Store
	chatID          string
	market          string
	minLevel        string
	prefix          string
	pollIntervalSec int
	batchWindowSec  int
	batchMaxItems   int
	lastID          int64
}

func NewAnomalyNotifier(store *sqlite.Store, redis *redisrepo.Store, chatID, market, minLevel, prefix string, pollSec, batchWin, batchMax int) *AnomalyNotifier {
	if pollSec < 2 {
		pollSec = 5
	}
	if batchWin < 10 {
		batchWin = 30
	}
	if batchMax < 1 {
		batchMax = 5
	}
	return &AnomalyNotifier{
		store: store, redis: redis,
		chatID: chatID, market: market, minLevel: minLevel, prefix: prefix,
		pollIntervalSec: pollSec, batchWindowSec: batchWin, batchMaxItems: batchMax,
	}
}

func (n *AnomalyNotifier) RunLoop(ctx context.Context, sendFn func(text string) error, stopCh <-chan struct{}) {
	// restore lastID from redis
	key := n.prefix + ":notify:last_id:" + n.market
	if n.redis != nil {
		if v, err := n.redis.Get(ctx, key); err == nil && v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				n.lastID = id
			}
		}
	}

	ticker := time.NewTicker(time.Duration(n.pollIntervalSec) * time.Second)
	defer ticker.Stop()

	var batch []model.AnomalyEvent
	lastFlush := time.Now()

	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows := n.poll(ctx)
			batch = append(batch, rows...)

			shouldFlush := len(batch) >= n.batchMaxItems ||
				(len(batch) > 0 && time.Since(lastFlush) >= time.Duration(n.batchWindowSec)*time.Second)

			if shouldFlush && len(batch) > 0 {
				text := n.formatBatch(batch)
				if err := sendFn(text); err != nil {
					log.Printf("tg notify: send error: %v", err)
				}
				batch = nil
				lastFlush = time.Now()

				// persist lastID
				if n.redis != nil {
					n.redis.Set(ctx, key, strconv.FormatInt(n.lastID, 10), 0)
				}
			}
		}
	}
}

func (n *AnomalyNotifier) poll(ctx context.Context) []model.AnomalyEvent {
	var rows []model.AnomalyEvent
	q := `SELECT * FROM anomaly_events WHERE id > ? AND market = ? ORDER BY id ASC LIMIT 50`
	if err := n.store.SelectContext(ctx, &rows, q, n.lastID, n.market); err != nil {
		return nil
	}

	var filtered []model.AnomalyEvent
	for _, r := range rows {
		if r.ID > n.lastID {
			n.lastID = r.ID
		}
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		// severity filter
		var details map[string]interface{}
		json.Unmarshal(r.Details, &details)
		score := eventSeverityScore(r.EventType, details)
		lvl := eventLevel(score)
		if !levelGTE(lvl, n.minLevel) {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

func (n *AnomalyNotifier) formatBatch(events []model.AnomalyEvent) string {
	loc := time.UTC
	now := time.Now().In(loc)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("【市场异动快讯】%s\n", now.Format("01-02 15:04")))

	// sort by severity desc
	type scored struct {
		evt   model.AnomalyEvent
		score float64
	}
	var items []scored
	for _, e := range events {
		var details map[string]interface{}
		json.Unmarshal(e.Details, &details)
		s := eventSeverityScore(e.EventType, details)
		items = append(items, scored{e, s})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].score > items[j].score })

	for i, it := range items {
		e := it.evt
		lvl := eventLevel(it.score)
		b.WriteString(fmt.Sprintf("%d. %s | %s | %s/%s | %s %.0f\n",
			i+1, e.Symbol, eventTypeLabel(e.EventType),
			e.TfSignal, ptrStr(e.TfLevel), lvl, it.score))
		b.WriteString(fmt.Sprintf("   %s\n", e.Title))
		b.WriteString(fmt.Sprintf("   时间：%s\n", fmtTs(e.EventTimeMs, loc)))
	}
	return b.String()
}

func levelGTE(a, threshold string) bool {
	order := map[string]int{"info": 0, "warning": 1, "critical": 2}
	return order[a] >= order[threshold]
}

func ptrStr(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
