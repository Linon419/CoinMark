package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

// 回踩类扫描（布林回踩、EMA 回踩）共用的 Telegram 通知：只通知收藏的币，在选定周期上“刚进入”条件时推一次
// （最新一根已收盘 K 线满足、前一根不满足）。事件写入 anomaly_events，由 TG 通知器发送。

var PullbackTimeframes = []string{"15m", "30m", "1h", "4h"}

// 每 15 分钟扫一次，对齐到整刻钟后 30 秒（15m/30m/1h/4h 都在整刻钟收盘，留时间让 1m 聚合出新 K 线）。
const (
	pullbackScanEvery = 15 * time.Minute
	pullbackScanDelay = 30 * time.Second
)

// nextPullbackScan 下一次扫描时间：now 之后最近的 整刻钟 + 30 秒。
func nextPullbackScan(now time.Time) time.Time {
	t := now.Truncate(pullbackScanEvery).Add(pullbackScanDelay)
	if !t.After(now) {
		t = t.Add(pullbackScanEvery)
	}
	return t
}

// runPullbackScans 启动时先扫一次（重启后页面不用等 15 分钟），之后按 nextPullbackScan 扫。
func runPullbackScans(ctx context.Context, stopCh <-chan struct{}, scan func(context.Context)) {
	for {
		scan(ctx)
		timer := time.NewTimer(time.Until(nextPullbackScan(time.Now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// 通知设置存在 boll_pump_settings 表里，按名字区分。
const (
	BollSqueezeNotifyName = "boll_squeeze_notify"
	EMAPullbackNotifyName = "ema_pullback_notify"
)

type PullbackNotifySettings struct {
	Enabled    bool     `json:"enabled"`
	Timeframes []string `json:"timeframes"`
	Favorites  []string `json:"favorites"`
}

func DefaultPullbackNotifySettings() PullbackNotifySettings {
	return PullbackNotifySettings{Enabled: true, Timeframes: append([]string{}, PullbackTimeframes...), Favorites: []string{}}
}

// NormalizePullbackNotifySettings 周期只保留支持的、按固定顺序；币名转大写、补 USDT、去重。
func NormalizePullbackNotifySettings(in PullbackNotifySettings) PullbackNotifySettings {
	out := PullbackNotifySettings{Enabled: in.Enabled, Timeframes: []string{}, Favorites: []string{}}
	want := map[string]bool{}
	for _, tf := range in.Timeframes {
		want[strings.TrimSpace(tf)] = true
	}
	for _, tf := range PullbackTimeframes {
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

func LoadPullbackNotifySettings(ctx context.Context, store *sqlite.Store, name string) (PullbackNotifySettings, error) {
	def := DefaultPullbackNotifySettings()
	if store == nil {
		return def, nil
	}
	var raw string
	err := store.GetContext(ctx, &raw, `SELECT config FROM boll_pump_settings WHERE name = ? LIMIT 1`, name)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	var cfg PullbackNotifySettings
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return def, err
	}
	return NormalizePullbackNotifySettings(cfg), nil
}

func SavePullbackNotifySettings(ctx context.Context, store *sqlite.Store, name string, cfg PullbackNotifySettings) (PullbackNotifySettings, error) {
	cfg = NormalizePullbackNotifySettings(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	err = store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_settings (name, config, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET config = excluded.config, updated_at = CURRENT_TIMESTAMP`,
			name, string(raw))
		return err
	})
	return cfg, err
}

// pullbackSignal 某个币某个周期在最新一根已收盘 K 线上的命中。
type pullbackSignal struct {
	CandleStartMs int64
	Title         string
	Details       map[string]interface{}
}

type pullbackEval func(symbol, tf string, bars []BollPumpBar) (pullbackSignal, bool)

// pullbackJustEntered 最新一根满足、前一根不满足。
func pullbackJustEntered(symbol, tf string, bars []BollPumpBar, eval pullbackEval) (pullbackSignal, bool) {
	sig, ok := eval(symbol, tf, bars)
	if !ok || len(bars) < 2 {
		return sig, false
	}
	if _, prev := eval(symbol, tf, bars[:len(bars)-1]); prev {
		return sig, false
	}
	return sig, true
}

// notifyPullbacks 检查收藏币在选定周期上是否刚进入条件，写入 anomaly_events（同一根 K 线只写一次）。
func notifyPullbacks(ctx context.Context, store *sqlite.Store, source BollPumpSource, market, settingsName, eventType string, nowMs int64, eval pullbackEval) (int, error) {
	if store == nil {
		return 0, nil
	}
	cfg, err := LoadPullbackNotifySettings(ctx, store, settingsName)
	if err != nil || !cfg.Enabled || len(cfg.Favorites) == 0 {
		return 0, err
	}
	var events []map[string]interface{}
	for _, symbol := range cfg.Favorites {
		for _, tf := range cfg.Timeframes {
			bars, err := source.Klines(ctx, market, symbol, tf, 499)
			if err != nil {
				continue
			}
			sig, ok := pullbackJustEntered(symbol, tf, bollPumpClosedBarsBefore(bars, nowMs), eval)
			if !ok {
				continue
			}
			tfMs := bollPumpWSIntervalMs(tf)
			closeMs := sig.CandleStartMs + tfMs
			if nowMs-closeMs > tfMs { // 太久以前的（例如重启前错过的），不再补发
				continue
			}
			events = append(events, yidongEvent(market, symbol, eventType, tf, "", closeMs, sig.Title, sig.Details))
		}
	}
	if len(events) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, events)
}
