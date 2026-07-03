package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestTGNotifyPrefsAPIUpdatesCategorySwitches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := openHandlerTGPrefsStore(t)
	defer store.Close()

	r := gin.New()
	RegisterRoutes(r, &Deps{
		Cfg: &config.Config{
			TGEnabled:      true,
			TGNotifyChatID: "12345",
			TGNotifyMarket: "swap",
		},
		Store: store,
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/telegram/notify-prefs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var initial map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if initial["abnormalEventsEnabled"] != true {
		t.Fatalf("default abnormalEventsEnabled = %v, want true", initial["abnormalEventsEnabled"])
	}
	if initial["whaleWallEnabled"] != false {
		t.Fatalf("default whaleWallEnabled = %v, want false", initial["whaleWallEnabled"])
	}
	if initial["absorptionEnabled"] != false {
		t.Fatalf("default absorptionEnabled = %v, want false", initial["absorptionEnabled"])
	}
	if initial["bollPumpEnabled"] != true {
		t.Fatalf("default bollPumpEnabled = %v, want true", initial["bollPumpEnabled"])
	}

	body := bytes.NewBufferString(`{"abnormalEventsEnabled":false,"whaleWallEnabled":true,"absorptionEnabled":true,"bollPumpEnabled":false}`)
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/telegram/notify-prefs", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var updated map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PATCH response: %v", err)
	}
	if updated["abnormalEventsEnabled"] != false {
		t.Fatalf("updated abnormalEventsEnabled = %v, want false", updated["abnormalEventsEnabled"])
	}
	if updated["whaleWallEnabled"] != true {
		t.Fatalf("updated whaleWallEnabled = %v, want true", updated["whaleWallEnabled"])
	}
	if updated["absorptionEnabled"] != true {
		t.Fatalf("updated absorptionEnabled = %v, want true", updated["absorptionEnabled"])
	}
	if updated["bollPumpEnabled"] != false {
		t.Fatalf("updated bollPumpEnabled = %v, want false", updated["bollPumpEnabled"])
	}
}

func openHandlerTGPrefsStore(t *testing.T) *sqlite.Store {
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
