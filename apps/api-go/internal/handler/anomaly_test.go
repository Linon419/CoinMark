package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"coinmark/api-go/internal/config"
)

func TestWhaleWallScanRequiresDepthScannerDeps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, &Deps{Cfg: &config.Config{}})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/aggregate/whaleWallScan?market=swap", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}
