package telegram

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
)

func TestPollRespectsAbnormalEventsSwitchForMarketMovement(t *testing.T) {
	ctx := context.Background()
	store := openTelegramNotifyStore(t)
	defer store.Close()

	prefs := service.DefaultTGNotifyPrefs(12345)
	prefs.MarketAnomalyEnabled = false
	prefs.WhaleWallEnabled = true
	prefs.AbsorptionEnabled = true
	if err := service.SaveTGNotifyPrefs(ctx, store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	insertTelegramNotifyEvent(t, store, "swap", "BTCUSDT", "price_rise_large_5m", "5m", "", "BTCUSDT 5分钟大涨", `{"retPct":12}`)

	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "info", chatIDInt: 12345, batchMaxItems: 5}
	got := n.poll(ctx)
	if len(got) != 0 {
		t.Fatalf("poll returned %d events, want 0", len(got))
	}
	if n.lastID == 0 {
		t.Fatalf("lastID = 0, want filtered rows to be acknowledged")
	}
}

func TestPollAllowsMarketMovementWhenAbnormalEventsEnabled(t *testing.T) {
	ctx := context.Background()
	store := openTelegramNotifyStore(t)
	defer store.Close()

	prefs := service.DefaultTGNotifyPrefs(12345)
	prefs.MarketAnomalyEnabled = true
	if err := service.SaveTGNotifyPrefs(ctx, store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	insertTelegramNotifyEvent(t, store, "swap", "BTCUSDT", "volume_rise_large_15m", "15m", "24x15m", "BTCUSDT 放量大涨", `{"retPct":12,"volumeFactor":55}`)

	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "info", chatIDInt: 12345, batchMaxItems: 5}
	got := n.poll(ctx)
	if len(got) != 1 {
		t.Fatalf("poll returned %d events, want 1", len(got))
	}
	if got[0].EventType != "volume_rise_large_15m" {
		t.Fatalf("event type = %q, want volume_rise_large_15m", got[0].EventType)
	}
}

func TestPollRespectsBollPumpSwitchForCandidates(t *testing.T) {
	ctx := context.Background()
	store := openTelegramNotifyStore(t)
	defer store.Close()

	prefs := service.DefaultTGNotifyPrefs(12345)
	prefs.BollPumpEnabled = false
	if err := service.SaveTGNotifyPrefs(ctx, store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	insertTelegramNotifyEvent(t, store, "swap", "XYZUSDT", "boll_pump", "15m", "CONFIRM_2", "CONFIRM_2 XYZUSDT 15m price=0.1234 score=92", `{"signalLevel":"CONFIRM_2","score":92,"volumeRatio":2.7,"bollBandwidth":0.034,"bounceCount":2}`)

	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "info", chatIDInt: 12345, batchMaxItems: 5}
	got := n.poll(ctx)
	if len(got) != 0 {
		t.Fatalf("poll returned %d events, want 0", len(got))
	}
	if n.lastID == 0 {
		t.Fatalf("lastID = 0, want filtered rows to be acknowledged")
	}
}

func TestFormatBatchUsesCategoryAwareHeadingAndReadableLabels(t *testing.T) {
	store := openTelegramNotifyStore(t)
	defer store.Close()

	prefs := service.DefaultTGNotifyPrefs(12345)
	prefs.WhaleWallEnabled = true
	if err := service.SaveTGNotifyPrefs(context.Background(), store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	insertTelegramNotifyEvent(t, store, "swap", "BTCUSDT", "whale_wall_far", "1m", "", "BTCUSDT bid wall 2.50M USDT, 3.20% away", `{"side":"bid","wallPrice":68000,"latestPrice":70000,"distancePct":3.2,"valueUSDT":2500000,"score":90}`)

	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "info", chatIDInt: 12345, batchMaxItems: 5}
	rows := n.poll(context.Background())
	got := n.formatBatch(rows)

	for _, want := range []string{"【大户挂单提醒】", "BTCUSDT", "远离现价大挂单", "挂单: bid 68000.00", "规模: 250.00万 USDT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatBatchUsesAbsorptionHeading(t *testing.T) {
	n := &AnomalyNotifier{}
	got := n.formatBatch([]model.AnomalyEvent{
		{
			Market:      "swap",
			Symbol:      "ETHUSDT",
			EventType:   "signal_lab_persistent_buy",
			TfSignal:    "1m",
			EventTimeMs: time.Now().UnixMilli(),
			Title:       "ETHUSDT 资金持续吸筹信号",
			Details:     model.JSONB(`{"score":88,"buyRatio":0.82,"persistentSpanMinutes":45}`),
		},
	})

	for _, want := range []string{"【吸筹提醒】", "持续吸筹", "强度: 88", "主动买占比: 82.0%", "持续: 45分钟"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatBatchUsesAbsorptionScanDetails(t *testing.T) {
	level := "LONG_BIAS"
	n := &AnomalyNotifier{}
	got := n.formatBatch([]model.AnomalyEvent{
		{
			Market:      "swap",
			Symbol:      "BTCUSDT",
			EventType:   "absorption_signal_long",
			TfSignal:    "4h",
			TfLevel:     &level,
			EventTimeMs: time.Now().UnixMilli(),
			Title:       "BTCUSDT 吸筹扫描看多 STRONG (95)",
			Details:     model.JSONB(`{"score":95,"direction":"LONG_BIAS","netFlowStrength":2500000,"window4hPassed":true,"window1dPassed":true,"window3dPassed":true}`),
		},
	})

	for _, want := range []string{"【吸筹提醒】", "吸筹扫描看多", "方向: LONG_BIAS", "净流: 250.00万 USDT", "窗口: 4h/1d/3d"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatBatchUsesBollPumpDetails(t *testing.T) {
	n := &AnomalyNotifier{}
	got := n.formatBatch([]model.AnomalyEvent{
		{
			Market:      "swap",
			Symbol:      "XYZUSDT",
			EventType:   "boll_pump",
			TfSignal:    "15m",
			EventTimeMs: time.Now().UnixMilli(),
			Title:       "CONFIRM_2 XYZUSDT 15m price=0.1234 score=92",
			Details:     model.JSONB(`{"signalLevel":"CONFIRM_2","score":92,"volumeRatio":2.7,"bollBandwidth":0.034,"bounceCount":2,"confluenceScore":6}`),
		},
	})

	for _, want := range []string{"【BOLL候选提醒】", "BOLL泵盘", "XYZUSDT", "周期: 15m", "强度: 92", "量能: 2.70x", "带宽: 0.0340", "反弹: 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format output missing %q:\n%s", want, got)
		}
	}
}

func TestLevelGTEAcceptsWarnAlias(t *testing.T) {
	if levelGTE("info", "warn") {
		t.Fatalf("info passed warn threshold")
	}
	if !levelGTE("warning", "warn") {
		t.Fatalf("warning did not pass warn threshold")
	}
	if !levelGTE("critical", "error") {
		t.Fatalf("critical did not pass error threshold")
	}
}

func openTelegramNotifyStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migration.Migrate(context.Background(), store); err != nil {
		store.Close()
		t.Fatalf("migrate sqlite: %v", err)
	}
	return store
}

func insertTelegramNotifyEvent(t *testing.T, store *sqlite.Store, market, symbol, eventType, tfSignal, tfLevel, title, details string) {
	t.Helper()
	var tfLevelArg interface{}
	if strings.TrimSpace(tfLevel) != "" {
		tfLevelArg = tfLevel
	}
	_, err := store.DB.Exec(`INSERT INTO anomaly_events
(market, symbol, event_type, tf_signal, tf_level, event_time_ms, title, details)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		market, symbol, eventType, tfSignal, tfLevelArg, time.Now().UnixMilli(), title, details)
	if err != nil {
		t.Fatalf("insert anomaly event: %v", err)
	}
}

func TestPollPassesArchEventsRegardlessOfLevel(t *testing.T) {
	ctx := context.Background()
	store := openTelegramNotifyStore(t)
	defer store.Close()
	if err := service.SaveTGNotifyPrefs(ctx, store, service.DefaultTGNotifyPrefs(12345)); err != nil {
		t.Fatalf("save prefs: %v", err)
	}
	insertTelegramNotifyEvent(t, store, "swap", "MOVRUSDT", service.ArchEventPriceShock, "1m", "", "MOVR - ₮2.4733 (13.36%) , 5分钟涨 5.02% [3]", `{"retPct":5.02}`)

	// minLevel 设得很高：旧事件会被评分过滤，arch 事件按原文直推不参与评分
	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "critical", chatIDInt: 12345, batchMaxItems: 5}
	if got := n.poll(ctx); len(got) != 1 {
		t.Fatalf("poll returned %d events, want 1", len(got))
	}
}

func TestFormatArchBatchSendsTitlesVerbatim(t *testing.T) {
	events := []model.AnomalyEvent{
		{ID: 2, EventType: service.ArchEventVolumeSpike, Title: "HEI - ₮0.15454 (11.51%) (中继) , 60分钟成交226.06万 = 昨日80% , 今日净流出-35.98万 , 量能7.07x  [18]"},
		{ID: 1, EventType: service.ArchEventPriceShock, Title: "MOVR - ₮2.4733 (13.36%) , 5分钟涨 5.02% [3]"},
	}
	got := formatArchBatch(events, time.Date(2026, 10, 1, 13, 38, 16, 0, time.UTC))
	want := "MOVR - ₮2.4733 (13.36%) , 5分钟涨 5.02% [3]\nHEI - ₮0.15454 (11.51%) (中继) , 60分钟成交226.06万 = 昨日80% , 今日净流出-35.98万 , 量能7.07x  [18]\n\n2026-10-01 13:38:16 UTC"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSplitArchEvents(t *testing.T) {
	arch, others := splitArchEvents([]model.AnomalyEvent{{EventType: service.ArchEventFundingRate}, {EventType: "boll_pump"}, {EventType: service.ArchEventPriceShock}})
	if len(arch) != 2 || len(others) != 1 || others[0].EventType != "boll_pump" {
		t.Fatalf("arch=%d others=%d", len(arch), len(others))
	}
}

func TestPollPassesBollSqueezeEventsEvenWithMarketAnomalyOff(t *testing.T) {
	ctx := context.Background()
	store := openTelegramNotifyStore(t)
	defer store.Close()
	prefs := service.DefaultTGNotifyPrefs(12345)
	prefs.MarketAnomalyEnabled = false
	if err := service.SaveTGNotifyPrefs(ctx, store, prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}
	insertTelegramNotifyEvent(t, store, "swap", "ZECUSDT", service.BollSqueezeEventType, "4h", "", "布林回踩 · ZEC 4h\n₮1335.45  %B -0.01（下轨 1337）", `{}`)

	n := &AnomalyNotifier{store: store, market: "swap", minLevel: "critical", chatIDInt: 12345, batchMaxItems: 5}
	if got := n.poll(ctx); len(got) != 1 {
		t.Fatalf("poll returned %d events, want 1", len(got))
	}
}

func TestSplitBollSqueezeEvents(t *testing.T) {
	boll, others := splitBollSqueezeEvents([]model.AnomalyEvent{{EventType: service.BollSqueezeEventType}, {EventType: "boll_pump"}})
	if len(boll) != 1 || len(others) != 1 || others[0].EventType != "boll_pump" {
		t.Fatalf("boll=%d others=%d", len(boll), len(others))
	}
}
