package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

// 潜力区“短时暴涨”的 Telegram 通知：币新进入名单时推一次（沿用上榜记录的 24 小时去重），默认关闭。
const (
	PotentialPumpEventType  = "potential_pump"
	PotentialPumpNotifyName = "potential_pump_notify"
)

type PotentialNotifySettings struct {
	Enabled bool `json:"enabled"`
}

func LoadPotentialNotifySettings(ctx context.Context, store *sqlite.Store) (PotentialNotifySettings, error) {
	var cfg PotentialNotifySettings
	if store == nil {
		return cfg, nil
	}
	var raw string
	err := store.GetContext(ctx, &raw, `SELECT config FROM boll_pump_settings WHERE name = ? LIMIT 1`, PotentialPumpNotifyName)
	if err == sql.ErrNoRows {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(raw), &cfg)
	return cfg, err
}

func SavePotentialNotifySettings(ctx context.Context, store *sqlite.Store, cfg PotentialNotifySettings) (PotentialNotifySettings, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	err = store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO boll_pump_settings (name, config, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET config = excluded.config, updated_at = CURRENT_TIMESTAMP`,
			PotentialPumpNotifyName, string(raw))
		return err
	})
	return cfg, err
}

// potentialWan 金额按“万”显示，例如 4628万、-26万。
func potentialWan(v float64) string {
	return fmt.Sprintf("%.0f万", math.Round(v/1e4))
}

func formatPotentialPumpNotify(it PotentialItem) string {
	return fmt.Sprintf("短时暴涨 · %s\n₮%s  4h %+.1f%%\n24h 成交额 %s · 持仓 %s · 3 天积累 %s",
		strings.TrimSuffix(it.Symbol, "USDT"), bollSqueezeNum(it.Price), it.Ret4h*100,
		potentialWan(it.Vol24), potentialWan(it.OIUSD), potentialWan(it.Acc3d))
}

// notifyPump items 是这一轮新上榜的币；通知打开时写入 anomaly_events，由 TG 通知器发送。
func (s *PotentialScanner) notifyPump(ctx context.Context, items []PotentialItem) (int, error) {
	if s.store == nil || len(items) == 0 {
		return 0, nil
	}
	cfg, err := LoadPotentialNotifySettings(ctx, s.store)
	if err != nil || !cfg.Enabled {
		return 0, err
	}
	events := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		events = append(events, yidongEvent("swap", it.Symbol, PotentialPumpEventType, "1h", "", it.DecisionMs, formatPotentialPumpNotify(it),
			map[string]interface{}{"price": it.Price, "ret4h": it.Ret4h, "vol24h": it.Vol24, "oiUsd": it.OIUSD, "acc3d": it.Acc3d}))
	}
	return insertAnomalyEvents(ctx, s.store, events)
}
