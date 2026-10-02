package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

// 布林回踩 Telegram 通知：只通知收藏的币，在选定周期上“刚进入”条件时推一次
// （最新一根已收盘 K 线满足、前一根不满足）。事件写入 anomaly_events，由 TG 通知器发送。
const (
	BollSqueezeEventType          = "boll_squeeze"
	bollSqueezeNotifySettingsName = "boll_squeeze_notify" // 存在 boll_pump_settings 表里
)

type BollSqueezeNotifySettings struct {
	Enabled    bool     `json:"enabled"`
	Timeframes []string `json:"timeframes"`
	Favorites  []string `json:"favorites"`
}

func DefaultBollSqueezeNotifySettings() BollSqueezeNotifySettings {
	return BollSqueezeNotifySettings{Enabled: true, Timeframes: append([]string{}, BollSqueezeTimeframes...), Favorites: []string{}}
}

// NormalizeBollSqueezeNotifySettings 周期只保留支持的、按固定顺序；币名转大写、补 USDT、去重。
func NormalizeBollSqueezeNotifySettings(in BollSqueezeNotifySettings) BollSqueezeNotifySettings {
	out := BollSqueezeNotifySettings{Enabled: in.Enabled, Timeframes: []string{}, Favorites: []string{}}
	want := map[string]bool{}
	for _, tf := range in.Timeframes {
		want[strings.TrimSpace(tf)] = true
	}
	for _, tf := range BollSqueezeTimeframes {
		if want[tf] {
			out.Timeframes = append(out.Timeframes, tf)
		}
	}
	seen := map[string]bool{}
	for _, sym := range in.Favorites {
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if sym == "" {
			continue
		}
		if !strings.HasSuffix(sym, "USDT") {
			sym += "USDT"
		}
		if !seen[sym] {
			seen[sym] = true
			out.Favorites = append(out.Favorites, sym)
		}
	}
	return out
}

func LoadBollSqueezeNotifySettings(ctx context.Context, store *sqlite.Store) (BollSqueezeNotifySettings, error) {
	def := DefaultBollSqueezeNotifySettings()
	if store == nil {
		return def, nil
	}
	var raw string
	err := store.GetContext(ctx, &raw, `SELECT config FROM boll_pump_settings WHERE name = ? LIMIT 1`, bollSqueezeNotifySettingsName)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	var cfg BollSqueezeNotifySettings
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return def, err
	}
	return NormalizeBollSqueezeNotifySettings(cfg), nil
}

func SaveBollSqueezeNotifySettings(ctx context.Context, store *sqlite.Store, cfg BollSqueezeNotifySettings) (BollSqueezeNotifySettings, error) {
	cfg = NormalizeBollSqueezeNotifySettings(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	err = store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_settings (name, config, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET config = excluded.config, updated_at = CURRENT_TIMESTAMP`,
			bollSqueezeNotifySettingsName, string(raw))
		return err
	})
	return cfg, err
}

// bollSqueezeJustEntered 最新一根已收盘 K 线满足条件、前一根不满足。
func bollSqueezeJustEntered(bars []BollPumpBar) (BollSqueezeHit, bool) {
	hit, ok := EvaluateBollSqueeze(bars)
	if !ok {
		return hit, false
	}
	if _, prev := EvaluateBollSqueeze(bars[:len(bars)-1]); prev {
		return hit, false
	}
	return hit, true
}

func bollSqueezeNum(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// formatBollSqueezeNotify Telegram 消息正文（发送时间由 Telegram 按本地时区显示，正文不再写时间）。
func formatBollSqueezeNotify(symbol string, hit BollSqueezeHit) string {
	return fmt.Sprintf("布林回踩 · %s %s\n₮%s  %%B %.2f（下轨 %s）\nEMA100 %s > EMA200 %s · 带宽 %.1f%%（近 20 根最大 %.1f%%）",
		strings.TrimSuffix(symbol, "USDT"), hit.Timeframe,
		bollSqueezeNum(hit.Close), hit.PercentB, bollSqueezeNum(hit.Lower),
		bollSqueezeNum(hit.EMA100), bollSqueezeNum(hit.EMA200),
		hit.Bandwidth*100, hit.BandwidthMax*100)
}

// notify 检查收藏币在选定周期上是否刚进入条件，写入 anomaly_events（同一根 K 线只写一次）。
func (s *BollSqueezeScanner) notify(ctx context.Context, nowMs int64) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	cfg, err := LoadBollSqueezeNotifySettings(ctx, s.store)
	if err != nil || !cfg.Enabled || len(cfg.Favorites) == 0 {
		return 0, err
	}
	var events []map[string]interface{}
	for _, symbol := range cfg.Favorites {
		for _, tf := range cfg.Timeframes {
			bars, err := s.source.Klines(ctx, s.market, symbol, tf, 499)
			if err != nil {
				continue
			}
			bars = bollPumpClosedBarsBefore(bars, nowMs)
			hit, ok := bollSqueezeJustEntered(bars)
			if !ok {
				continue
			}
			tfMs := bollPumpWSIntervalMs(tf)
			closeMs := hit.CandleStartMs + tfMs
			if nowMs-closeMs > tfMs { // 太久以前的（例如重启前错过的），不再补发
				continue
			}
			hit.Timeframe = tf
			events = append(events, yidongEvent(s.market, symbol, BollSqueezeEventType, tf, "", closeMs, formatBollSqueezeNotify(symbol, hit), map[string]interface{}{
				"close": hit.Close, "lower": hit.Lower, "percentB": hit.PercentB, "ema100": hit.EMA100, "ema200": hit.EMA200, "bandwidth": hit.Bandwidth,
			}))
		}
	}
	if len(events) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, s.store, events)
}
