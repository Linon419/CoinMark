package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	redisrepo "coinmark/api-go/internal/repo/redis"
	"coinmark/api-go/internal/repo/sqlite"
	"github.com/jmoiron/sqlx"
)

type AnomalyNotifier struct {
	bot interface {
		Send(to interface{}, what interface{}, opts ...interface{}) (*interface{}, error)
	}
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
	chatIDInt       int64
}

type tgNotifyPrefs struct {
	ChatID               int64 `db:"chat_id"`
	MarketAnomalyEnabled bool  `db:"market_anomaly_enabled"`
	WhaleWallEnabled     bool  `db:"whale_wall_enabled"`
	SignalLabEnabled     bool  `db:"signal_lab_enabled"`
	MuteAll              bool  `db:"mute_all"`
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
	n.bootstrapLastID(ctx)

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
			if len(rows) > 0 {
				batch = append(batch, rows...)
			}

			shouldFlush := len(batch) >= n.batchMaxItems ||
				(len(batch) > 0 && time.Since(lastFlush) >= time.Duration(n.batchWindowSec)*time.Second)

			if shouldFlush && len(batch) > 0 {
				chunks := n.buildChunks(batch)
				for _, chunk := range chunks {
					if err := sendFn(n.formatBatch(chunk)); err != nil {
						log.Printf("tg notify: send error: %v", err)
					}
				}
				batch = nil
				lastFlush = time.Now()
			}
		}
	}
}

func (n *AnomalyNotifier) poll(ctx context.Context) []model.AnomalyEvent {
	var rows []model.AnomalyEvent
	q := `SELECT * FROM anomaly_events WHERE id > ? AND market = ? ORDER BY id ASC LIMIT 500`
	if err := n.store.SelectContext(ctx, &rows, q, n.lastID, n.market); err != nil {
		return nil
	}
	prefs := n.mustLoadPrefs(ctx)

	var filtered []model.AnomalyEvent
	for _, r := range rows {
		if r.ID > n.lastID {
			n.lastID = r.ID
		}
		if !n.isEventEnabledByPrefs(r.EventType, prefs) {
			continue
		}
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		var details map[string]interface{}
		_ = json.Unmarshal(r.Details, &details)
		score := eventSeverityScore(r.EventType, details)
		lvl := eventLevel(score)
		if !levelGTE(lvl, n.minLevel) {
			continue
		}
		filtered = append(filtered, r)
	}

	n.persistLastID(ctx)
	return filtered
}

func notifyEventCategory(eventType string) string {
	et := strings.ToLower(strings.TrimSpace(eventType))
	switch et {
	case "whale_wall_far", "anomaly_whale_wall_far", "whale_wall_filled", "whale_wall_canceled":
		return "whale_wall"
	}
	if strings.HasPrefix(et, "signal_lab_") {
		return "signal_lab"
	}
	return "market_anomaly"
}

func (n *AnomalyNotifier) isEventEnabledByPrefs(eventType string, p tgNotifyPrefs) bool {
	if p.MuteAll {
		return false
	}
	switch notifyEventCategory(eventType) {
	case "whale_wall":
		return p.WhaleWallEnabled
	case "signal_lab":
		return p.SignalLabEnabled
	default:
		return p.MarketAnomalyEnabled
	}
}

func (n *AnomalyNotifier) defaultPrefs() tgNotifyPrefs {
	return tgNotifyPrefs{
		ChatID:               n.chatIDInt,
		MarketAnomalyEnabled: true,
		WhaleWallEnabled:     false,
		SignalLabEnabled:     false,
		MuteAll:              false,
	}
}

func (n *AnomalyNotifier) mustLoadPrefs(ctx context.Context) tgNotifyPrefs {
	p, err := n.loadPrefs(ctx)
	if err != nil {
		return n.defaultPrefs()
	}
	return p
}

func (n *AnomalyNotifier) loadPrefs(ctx context.Context) (tgNotifyPrefs, error) {
	def := n.defaultPrefs()
	if n.chatIDInt == 0 {
		return def, nil
	}
	var row tgNotifyPrefs
	err := n.store.GetContext(ctx, &row, `SELECT chat_id, market_anomaly_enabled, whale_wall_enabled, signal_lab_enabled, mute_all
FROM tg_notify_prefs WHERE chat_id = ? LIMIT 1`, n.chatIDInt)
	if err == nil {
		return row, nil
	}
	if err != sql.ErrNoRows {
		return def, err
	}
	if err := n.savePrefs(ctx, def); err != nil {
		return def, err
	}
	return def, nil
}

func (n *AnomalyNotifier) savePrefs(ctx context.Context, p tgNotifyPrefs) error {
	if n.chatIDInt == 0 {
		return nil
	}
	p.ChatID = n.chatIDInt
	return n.store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO tg_notify_prefs
(chat_id, market_anomaly_enabled, whale_wall_enabled, signal_lab_enabled, mute_all, updated_at)
VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(chat_id) DO UPDATE SET
 market_anomaly_enabled = excluded.market_anomaly_enabled,
 whale_wall_enabled = excluded.whale_wall_enabled,
 signal_lab_enabled = excluded.signal_lab_enabled,
 mute_all = excluded.mute_all,
 updated_at = CURRENT_TIMESTAMP`,
			p.ChatID, p.MarketAnomalyEnabled, p.WhaleWallEnabled, p.SignalLabEnabled, p.MuteAll,
		)
		return err
	})
}

func (n *AnomalyNotifier) bootstrapLastID(ctx context.Context) {
	key := n.prefix + ":notify:last_id:" + n.market
	if n.redis != nil {
		if v, err := n.redis.Get(ctx, key); err == nil && v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				n.lastID = id
				return
			}
		}
	}

	_ = n.store.GetContext(ctx, &n.lastID, `SELECT coalesce(max(id),0) FROM anomaly_events WHERE market = ?`, n.market)
	n.persistLastID(ctx)
}

func (n *AnomalyNotifier) persistLastID(ctx context.Context) {
	if n.redis == nil {
		return
	}
	key := n.prefix + ":notify:last_id:" + n.market
	_ = n.redis.Set(ctx, key, strconv.FormatInt(n.lastID, 10), 0)
}

func (n *AnomalyNotifier) buildChunks(events []model.AnomalyEvent) [][]model.AnomalyEvent {
	type evtKey struct {
		symbol string
		typ    string
		ts     int64
	}
	seen := map[evtKey]struct{}{}
	uniq := make([]model.AnomalyEvent, 0, len(events))
	for _, e := range events {
		k := evtKey{symbol: e.Symbol, typ: e.EventType, ts: e.EventTimeMs}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, e)
	}

	sort.Slice(uniq, func(i, j int) bool {
		var di, dj map[string]interface{}
		_ = json.Unmarshal(uniq[i].Details, &di)
		_ = json.Unmarshal(uniq[j].Details, &dj)
		return eventSeverityScore(uniq[i].EventType, di) > eventSeverityScore(uniq[j].EventType, dj)
	})

	chunks := make([][]model.AnomalyEvent, 0, len(uniq)/n.batchMaxItems+1)
	for i := 0; i < len(uniq); i += n.batchMaxItems {
		end := i + n.batchMaxItems
		if end > len(uniq) {
			end = len(uniq)
		}
		chunks = append(chunks, uniq[i:end])
	}
	return chunks
}

func (n *AnomalyNotifier) formatBatch(events []model.AnomalyEvent) string {
	loc := time.UTC
	now := time.Now().In(loc)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("【市场异动快讯】%s\n", now.Format("01-02 15:04")))

	type scored struct {
		evt   model.AnomalyEvent
		score float64
	}
	items := make([]scored, 0, len(events))
	for _, e := range events {
		var details map[string]interface{}
		_ = json.Unmarshal(e.Details, &details)
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
		if strings.TrimSpace(e.Title) != "" {
			b.WriteString(fmt.Sprintf("   %s\n", e.Title))
		}
		b.WriteString(fmt.Sprintf("   时间: %s\n", fmtTs(e.EventTimeMs, loc)))
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
