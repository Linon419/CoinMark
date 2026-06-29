package handler

import (
	"context"
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
