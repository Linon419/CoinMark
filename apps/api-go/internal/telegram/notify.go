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
	redisrepo "coinmark/api-go/internal/repo/redis"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
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

type tgNotifyPrefs = service.TGNotifyPrefs

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
	return service.TGNotifyEventCategory(eventType)
}

func (n *AnomalyNotifier) isEventEnabledByPrefs(eventType string, p tgNotifyPrefs) bool {
	return service.IsTGNotifyEventEnabled(eventType, p)
}

func (n *AnomalyNotifier) defaultPrefs() tgNotifyPrefs {
	return service.DefaultTGNotifyPrefs(n.chatIDInt)
}

func (n *AnomalyNotifier) mustLoadPrefs(ctx context.Context) tgNotifyPrefs {
	p, err := n.loadPrefs(ctx)
	if err != nil {
		return n.defaultPrefs()
	}
	return p
}

func (n *AnomalyNotifier) loadPrefs(ctx context.Context) (tgNotifyPrefs, error) {
	return service.LoadTGNotifyPrefs(ctx, n.store, n.chatIDInt)
}

func (n *AnomalyNotifier) savePrefs(ctx context.Context, p tgNotifyPrefs) error {
	p.ChatID = n.chatIDInt
	return service.SaveTGNotifyPrefs(ctx, n.store, p)
}

func (n *AnomalyNotifier) bootstrapLastID(ctx context.Context) {
	key := n.prefix + ":notify:last_id:" + n.market
	if n.redis != nil {
		if v, err := n.redis.Get(ctx, key); err == nil && v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				n.lastID = id
				var dbMaxID int64
				if err := n.store.GetContext(ctx, &dbMaxID, `SELECT coalesce(max(id),0) FROM anomaly_events WHERE market = ?`, n.market); err == nil {
					if n.lastID > dbMaxID {
						log.Printf("tg notify: self-heal last_id drift market=%s redis=%d db=%d", n.market, n.lastID, dbMaxID)
						n.lastID = dbMaxID
						n.persistLastID(ctx)
					}
				}
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
	b.WriteString(fmt.Sprintf("%s %s UTC\n", notifyBatchTitle(events), now.Format("01-02 15:04")))

	type scored struct {
		evt     model.AnomalyEvent
		details map[string]interface{}
		score   float64
	}
	items := make([]scored, 0, len(events))
	for _, e := range events {
		var details map[string]interface{}
		_ = json.Unmarshal(e.Details, &details)
		s := eventSeverityScore(e.EventType, details)
		items = append(items, scored{e, details, s})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].score > items[j].score })

	for i, it := range items {
		e := it.evt
		lvl := eventLevel(it.score)
		b.WriteString(fmt.Sprintf("%d. %s｜%s｜%s %.0f\n",
			i+1, e.Symbol, eventTypeLabel(e.EventType), lvl, it.score))
		if tf := timeframeText(e); tf != "" {
			b.WriteString(fmt.Sprintf("   周期: %s\n", tf))
		}
		if strings.TrimSpace(e.Title) != "" {
			b.WriteString(fmt.Sprintf("   内容: %s\n", e.Title))
		}
		if detail := notifyDetailLine(e.EventType, it.details); detail != "" {
			b.WriteString(fmt.Sprintf("   %s\n", detail))
		}
		b.WriteString(fmt.Sprintf("   时间: %s UTC\n", fmtTs(e.EventTimeMs, loc)))
	}
	return b.String()
}

func notifyBatchTitle(events []model.AnomalyEvent) string {
	if len(events) == 0 {
		return "【CoinMark 信号快讯】"
	}
	category := notifyEventCategory(events[0].EventType)
	for _, e := range events[1:] {
		if notifyEventCategory(e.EventType) != category {
			return "【CoinMark 信号快讯】"
		}
	}
	switch category {
	case service.TGNotifyCategoryWhaleWall:
		return "【大户挂单提醒】"
	case service.TGNotifyCategoryAbsorption:
		return "【吸筹提醒】"
	default:
		return "【Abnormal Events】"
	}
}

func timeframeText(e model.AnomalyEvent) string {
	tf := strings.TrimSpace(e.TfSignal)
	level := ptrStr(e.TfLevel)
	if level == "-" {
		return tf
	}
	if tf == "" {
		return level
	}
	return tf + " / " + level
}

func notifyDetailLine(eventType string, details map[string]interface{}) string {
	category := notifyEventCategory(eventType)
	parts := make([]string, 0, 4)

	switch category {
	case service.TGNotifyCategoryWhaleWall:
		side := detailString(details, "side")
		if side == "" {
			side = "-"
		}
		if wallPrice, ok := detailFloat(details, "wallPrice"); ok {
			parts = append(parts, fmt.Sprintf("挂单: %s %.2f", side, wallPrice))
		}
		if distancePct, ok := detailFloat(details, "distancePct"); ok {
			parts = append(parts, "距离: "+fmtPct(distancePct))
		}
		if valueUSDT, ok := detailFloat(details, "valueUSDT"); ok {
			parts = append(parts, "规模: "+fmtBigUSD(valueUSDT)+" USDT")
		}
	case service.TGNotifyCategoryAbsorption:
		if direction := detailString(details, "direction"); direction != "" {
			parts = append(parts, "方向: "+direction)
		}
		if score, ok := detailFloat(details, "strengthScore"); ok {
			parts = append(parts, fmt.Sprintf("强度: %.0f", score))
		} else if score, ok := detailFloat(details, "score"); ok {
			parts = append(parts, fmt.Sprintf("强度: %.0f", score))
		}
		if netFlow, ok := detailFloat(details, "netFlowStrength"); ok {
			parts = append(parts, "净流: "+fmtBigUSD(netFlow)+" USDT")
		}
		if buyRatio, ok := detailFloat(details, "buyRatio"); ok {
			parts = append(parts, fmt.Sprintf("主动买占比: %.1f%%", buyRatio*100))
		}
		if span, ok := detailFloat(details, "persistentSpanMinutes"); ok {
			parts = append(parts, fmt.Sprintf("持续: %.0f分钟", span))
		}
		if windows := passedWindowText(details); windows != "" {
			parts = append(parts, "窗口: "+windows)
		}
	default:
		if retPct, ok := detailFloat(details, "retPct"); ok {
			parts = append(parts, "涨跌: "+fmtSignedPct(retPct))
		}
		if volumeFactor, ok := detailFloat(details, "volumeFactor"); ok {
			parts = append(parts, "量能: "+fmtFactor(volumeFactor))
		}
		if latestHigh, ok := detailFloat(details, "latestHigh"); ok {
			parts = append(parts, fmt.Sprintf("高点: %.6g", latestHigh))
		}
		if latestLow, ok := detailFloat(details, "latestLow"); ok {
			parts = append(parts, fmt.Sprintf("低点: %.6g", latestLow))
		}
		if pullbackPct, ok := detailFloat(details, "pullbackPct"); ok {
			parts = append(parts, "回撤: "+fmtSignedPct(pullbackPct))
		}
		if reboundPct, ok := detailFloat(details, "reboundPct"); ok {
			parts = append(parts, "反弹: "+fmtSignedPct(reboundPct))
		}
	}

	return strings.Join(parts, " | ")
}

func detailString(details map[string]interface{}, key string) string {
	if details == nil {
		return ""
	}
	v, ok := details[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func passedWindowText(details map[string]interface{}) string {
	passed := make([]string, 0, 3)
	if detailBool(details, "window4hPassed") {
		passed = append(passed, "4h")
	}
	if detailBool(details, "window1dPassed") {
		passed = append(passed, "1d")
	}
	if detailBool(details, "window3dPassed") {
		passed = append(passed, "3d")
	}
	return strings.Join(passed, "/")
}

func detailBool(details map[string]interface{}, key string) bool {
	if details == nil {
		return false
	}
	v, ok := details[key]
	if !ok || v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}

func levelGTE(a, threshold string) bool {
	return levelRank(a, 0) >= levelRank(threshold, 1)
}

func levelRank(level string, fallback int) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info":
		return 0
	case "warn", "warning":
		return 1
	case "error", "critical":
		return 2
	default:
		return fallback
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
