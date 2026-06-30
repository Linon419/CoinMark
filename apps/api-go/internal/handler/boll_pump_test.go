package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/model"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
)

func TestBollPumpSignalsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := openHandlerBollPumpStore(t)
	defer store.Close()

	_, err := service.SaveBollPumpSignal(context.Background(), store, model.BollPumpSignal{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "15m",
		SignalLevel:   "WATCH",
		Price:         0.1234,
		Score:         75,
		PriorityScore: 75,
		SignalTimeMs:  time.Now().UnixMilli(),
		CandleStartMs: 60000,
		Reason:        "volume-backed pump",
		Details:       model.JSONB(`{"score":75}`),
	}, false)
	if err != nil {
		t.Fatalf("save signal: %v", err)
	}

	r := gin.New()
	RegisterRoutes(r, &Deps{Cfg: &config.Config{}, Store: store})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/boll-pump/signals?market=swap&limit=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "XYZUSDT") {
		t.Fatalf("response missing symbol: %s", w.Body.String())
	}
}

func TestBollPumpStatesAPIAddsDominantTimeframe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := openHandlerBollPumpStore(t)
	defer store.Close()

	ctx := context.Background()
	if err := service.SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "1m",
		Status:        "COMPLETED",
		WatchScore:    120,
		CurrentScore:  140,
		PriorityScore: 140,
		BounceCount:   2,
		Details:       model.JSONB(`{}`),
	}); err != nil {
		t.Fatalf("save 1m state: %v", err)
	}
	if err := service.SaveBollPumpState(ctx, store, model.BollPumpState{
		Market:        "swap",
		Symbol:        "XYZUSDT",
		Timeframe:     "3m",
		Status:        "WATCH",
		WatchScore:    85,
		CurrentScore:  85,
		PriorityScore: 85,
		Details:       model.JSONB(`{}`),
	}); err != nil {
		t.Fatalf("save 3m state: %v", err)
	}

	r := gin.New()
	RegisterRoutes(r, &Deps{Cfg: &config.Config{}, Store: store})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/boll-pump/states?market=swap&symbol=XYZUSDT&timeframe=1m&limit=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"dominant_timeframe":"3m"`) {
		t.Fatalf("response missing dominant timeframe: %s", w.Body.String())
	}
}

func TestBollPumpSettingsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := openHandlerBollPumpStore(t)
	defer store.Close()

	r := gin.New()
	RegisterRoutes(r, &Deps{Cfg: &config.Config{BollPumpEnabled: true, BollPumpMarket: "swap"}, Store: store})

	cfg := service.DefaultBollPumpConfig()
	cfg.SymbolLimit = 55
	body, _ := json.Marshal(cfg)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/boll-pump/settings", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"symbol_limit":55`) {
		t.Fatalf("put response missing symbol limit: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/boll-pump/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"symbol_limit":55`) {
		t.Fatalf("get response missing symbol limit: %s", w.Body.String())
	}
}

func openHandlerBollPumpStore(t *testing.T) *sqlite.Store {
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
