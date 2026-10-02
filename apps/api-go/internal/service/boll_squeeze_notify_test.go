package service

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizePullbackNotifySettings(t *testing.T) {
	got := NormalizePullbackNotifySettings(PullbackNotifySettings{
		Enabled:    true,
		Timeframes: []string{"4h", "5m", "15m", "4h"},
		Favorites:  []string{" zec", "ZECUSDT", "pumpusdt", ""},
	})
	if !reflect.DeepEqual(got.Timeframes, []string{"15m", "4h"}) {
		t.Fatalf("timeframes = %v", got.Timeframes)
	}
	if !reflect.DeepEqual(got.Favorites, []string{"ZECUSDT", "PUMPUSDT"}) {
		t.Fatalf("favorites = %v", got.Favorites)
	}
}

func TestPullbackNotifySettingsRoundTripPerName(t *testing.T) {
	store := openBollPumpTestStore(t)
	defer store.Close()
	ctx := context.Background()
	def, err := LoadPullbackNotifySettings(ctx, store, BollSqueezeNotifyName)
	if err != nil || !def.Enabled || len(def.Timeframes) != 4 || len(def.Favorites) != 0 {
		t.Fatalf("default = %+v err=%v", def, err)
	}
	if _, err := SavePullbackNotifySettings(ctx, store, BollSqueezeNotifyName, PullbackNotifySettings{Timeframes: []string{"1h"}, Favorites: []string{"zec"}}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPullbackNotifySettings(ctx, store, BollSqueezeNotifyName)
	if err != nil || got.Enabled || !reflect.DeepEqual(got.Timeframes, []string{"1h"}) || !reflect.DeepEqual(got.Favorites, []string{"ZECUSDT"}) {
		t.Fatalf("loaded = %+v err=%v", got, err)
	}
	// EMA 回踩的设置是另一份，不受影响
	if ema, _ := LoadPullbackNotifySettings(ctx, store, EMAPullbackNotifyName); !ema.Enabled || len(ema.Favorites) != 0 {
		t.Fatalf("ema settings = %+v, want defaults", ema)
	}
}

func TestBollSqueezeJustEnteredOnlyOnFirstHitCandle(t *testing.T) {
	bars := loadZECSqueezeBars(t)
	// 10-01 04:00 收盘 %B 0.32：第一根满足（前一根 00:00 %B 0.37 不满足）
	if _, ok := pullbackJustEntered("ZECUSDT", "4h", bars[:len(bars)-3], bollSqueezeSignal); !ok {
		t.Fatal("expected just entered at 10-01 04:00")
	}
	// 之后连续满足的 08:00、12:00、16:00 不再推
	for cut := 2; cut >= 0; cut-- {
		if _, ok := pullbackJustEntered("ZECUSDT", "4h", bars[:len(bars)-cut], bollSqueezeSignal); ok {
			t.Fatalf("cut=%d: expected no notify while still in condition", cut)
		}
	}
}

func TestBollSqueezeNotifyWritesEventOncePerCandle(t *testing.T) {
	store := openBollPumpTestStore(t)
	defer store.Close()
	ctx := context.Background()
	bars := loadZECSqueezeBars(t)
	bars = bars[:len(bars)-3] // 最后一根 = 10-01 04:00（刚进入）
	closeMs := bars[len(bars)-1].OpenTimeMs + 4*potentialTestHour
	s := NewBollSqueezeScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"4h": bars, "1h": bars}}, "swap", store)

	if n, _ := s.notify(ctx, closeMs+60_000); n != 0 {
		t.Fatalf("no favorites: events = %d, want 0", n)
	}
	if _, err := SavePullbackNotifySettings(ctx, store, BollSqueezeNotifyName, PullbackNotifySettings{Enabled: true, Timeframes: []string{"4h"}, Favorites: []string{"ZEC"}}); err != nil {
		t.Fatal(err)
	}
	// 4h 周期：收盘后 3 小时仍在一根周期内，照常发送
	if n, err := s.notify(ctx, closeMs+3*potentialTestHour); err != nil || n != 1 {
		t.Fatalf("events = %d err=%v, want 1", n, err)
	}
	var titles []string
	if err := store.SelectContext(ctx, &titles, `SELECT title FROM anomaly_events WHERE event_type = ?`, BollSqueezeEventType); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 1 || !strings.HasPrefix(titles[0], "布林回踩 · ZEC 4h\n") {
		t.Fatalf("titles = %q", titles)
	}
	if n, _ := s.notify(ctx, closeMs+5*60_000); n != 0 {
		t.Fatalf("same candle again: events = %d, want 0", n)
	}
}

func TestBollSqueezeNotifySkipsStaleCandle(t *testing.T) {
	store := openBollPumpTestStore(t)
	defer store.Close()
	ctx := context.Background()
	bars := loadZECSqueezeBars(t)
	bars = bars[:len(bars)-3]
	closeMs := bars[len(bars)-1].OpenTimeMs + 4*potentialTestHour
	if _, err := SavePullbackNotifySettings(ctx, store, BollSqueezeNotifyName, PullbackNotifySettings{Enabled: true, Timeframes: []string{"4h"}, Favorites: []string{"ZEC"}}); err != nil {
		t.Fatal(err)
	}
	s := NewBollSqueezeScanner(&fakeBollPumpSource{bars: map[string][]BollPumpBar{"4h": bars}}, "swap", store)
	if n, _ := s.notify(ctx, closeMs+5*potentialTestHour); n != 0 {
		t.Fatalf("stale candle: events = %d, want 0", n)
	}
}

func TestBollSqueezeTGCategoryIgnoresMarketAnomalySwitch(t *testing.T) {
	prefs := DefaultTGNotifyPrefs(1)
	prefs.MarketAnomalyEnabled = false
	if !IsTGNotifyEventEnabled(BollSqueezeEventType, prefs) {
		t.Fatal("boll squeeze should not follow the market anomaly switch")
	}
	prefs.MuteAll = true
	if IsTGNotifyEventEnabled(BollSqueezeEventType, prefs) {
		t.Fatal("mute all should silence boll squeeze")
	}
}
