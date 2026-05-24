package service

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

const (
	TGNotifyCategoryMarketAnomaly = "market_anomaly"
	TGNotifyCategoryWhaleWall     = "whale_wall"
	TGNotifyCategoryAbsorption    = "absorption"
)

type TGNotifyPrefs struct {
	ChatID               int64 `db:"chat_id" json:"-"`
	MarketAnomalyEnabled bool  `db:"market_anomaly_enabled" json:"abnormalEventsEnabled"`
	WhaleWallEnabled     bool  `db:"whale_wall_enabled" json:"whaleWallEnabled"`
	AbsorptionEnabled    bool  `db:"absorption_enabled" json:"absorptionEnabled"`
	SignalLabEnabled     bool  `db:"signal_lab_enabled" json:"-"`
	MuteAll              bool  `db:"mute_all" json:"muteAll"`
}

func DefaultTGNotifyPrefs(chatID int64) TGNotifyPrefs {
	return TGNotifyPrefs{
		ChatID:               chatID,
		MarketAnomalyEnabled: true,
		WhaleWallEnabled:     false,
		AbsorptionEnabled:    false,
		SignalLabEnabled:     false,
		MuteAll:              false,
	}
}

func LoadTGNotifyPrefs(ctx context.Context, store *sqlite.Store, chatID int64) (TGNotifyPrefs, error) {
	def := DefaultTGNotifyPrefs(chatID)
	if store == nil || chatID == 0 {
		return def, nil
	}

	var row TGNotifyPrefs
	err := store.GetContext(ctx, &row, `SELECT chat_id, market_anomaly_enabled, whale_wall_enabled, absorption_enabled, signal_lab_enabled, mute_all
FROM tg_notify_prefs WHERE chat_id = ? LIMIT 1`, chatID)
	if err == nil {
		return row, nil
	}
	if err != sql.ErrNoRows {
		return def, err
	}
	if err := SaveTGNotifyPrefs(ctx, store, def); err != nil {
		return def, err
	}
	return def, nil
}

func SaveTGNotifyPrefs(ctx context.Context, store *sqlite.Store, prefs TGNotifyPrefs) error {
	if store == nil || prefs.ChatID == 0 {
		return nil
	}
	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO tg_notify_prefs
(chat_id, market_anomaly_enabled, whale_wall_enabled, absorption_enabled, signal_lab_enabled, mute_all, updated_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(chat_id) DO UPDATE SET
 market_anomaly_enabled = excluded.market_anomaly_enabled,
 whale_wall_enabled = excluded.whale_wall_enabled,
 absorption_enabled = excluded.absorption_enabled,
 signal_lab_enabled = excluded.signal_lab_enabled,
 mute_all = excluded.mute_all,
 updated_at = CURRENT_TIMESTAMP`,
			prefs.ChatID, prefs.MarketAnomalyEnabled, prefs.WhaleWallEnabled, prefs.AbsorptionEnabled, prefs.SignalLabEnabled, prefs.MuteAll,
		)
		return err
	})
}

func TGNotifyEventCategory(eventType string) string {
	et := strings.ToLower(strings.TrimSpace(eventType))
	switch et {
	case "whale_wall_far", "anomaly_whale_wall_far", "whale_wall_filled", "whale_wall_canceled":
		return TGNotifyCategoryWhaleWall
	case "signal_lab_persistent_buy":
		return TGNotifyCategoryAbsorption
	}
	if strings.HasPrefix(et, "absorption") {
		return TGNotifyCategoryAbsorption
	}
	return TGNotifyCategoryMarketAnomaly
}

func IsTGNotifyEventEnabled(eventType string, prefs TGNotifyPrefs) bool {
	if prefs.MuteAll {
		return false
	}
	switch TGNotifyEventCategory(eventType) {
	case TGNotifyCategoryWhaleWall:
		return prefs.WhaleWallEnabled
	case TGNotifyCategoryAbsorption:
		return prefs.AbsorptionEnabled
	default:
		return prefs.MarketAnomalyEnabled
	}
}
