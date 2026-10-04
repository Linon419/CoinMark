package binance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestGuard(now *time.Time) *restGuard {
	g := newRestGuard()
	g.now = func() time.Time { return *now }
	return g
}

func TestRestGuardPausesUntilBanEnds(t *testing.T) {
	now := time.UnixMilli(1790931000000)
	g := newTestGuard(&now)
	body := []byte(`{"code":-1003,"msg":"Way too many requests; IP(1.2.3.4) banned until 1790931239698. Please use the websocket for live updates to avoid bans."}`)
	g.observe("fapi.binance.com", "/fapi/v1/klines", &http.Response{StatusCode: http.StatusTeapot, Header: http.Header{}}, body)

	if err := g.check("fapi.binance.com"); err == nil || !strings.Contains(err.Error(), "paused until") {
		t.Fatalf("expected pause during ban, got %v", err)
	}
	if err := g.check("api.binance.com"); err != nil {
		t.Fatalf("spot is limited separately, got %v", err)
	}
	now = time.UnixMilli(1790931239698 + 1)
	if err := g.check("fapi.binance.com"); err != nil {
		t.Fatalf("expected resume after ban, got %v", err)
	}
}

func TestRestGuardHonorsRetryAfterOn429(t *testing.T) {
	now := time.Unix(1000, 0)
	g := newTestGuard(&now)
	g.observe("fapi.binance.com", "/fapi/v1/premiumIndex", &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"120"}}}, nil)
	now = now.Add(119 * time.Second)
	if g.check("fapi.binance.com") == nil {
		t.Fatal("expected pause before Retry-After")
	}
	now = now.Add(2 * time.Second)
	if err := g.check("fapi.binance.com"); err != nil {
		t.Fatalf("expected resume, got %v", err)
	}
}

func TestRestGuardDefaultsToOneMinute(t *testing.T) {
	now := time.Unix(1000, 0)
	g := newTestGuard(&now)
	g.observe("fapi.binance.com", "/x", &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, nil)
	now = now.Add(59 * time.Second)
	if g.check("fapi.binance.com") == nil {
		t.Fatal("expected default 1 minute pause")
	}
}

func TestRestGuardCountsTopPathsPerMinute(t *testing.T) {
	now := time.Unix(6000, 0)
	g := newTestGuard(&now)
	ok := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	for i := 0; i < 3; i++ {
		g.observe("fapi.binance.com", "/fapi/v1/klines?interval=15m", ok, nil)
	}
	g.observe("fapi.binance.com", "/fapi/v1/premiumIndex", ok, nil)
	if got := g.topCounts(1); got != "fapi.binance.com/fapi/v1/klines?interval=15m×3" {
		t.Fatalf("top = %q", got)
	}
	now = now.Add(time.Minute) // 下一分钟重新计数
	g.observe("fapi.binance.com", "/fapi/v1/premiumIndex", ok, nil)
	if got := g.topCounts(3); got != "fapi.binance.com/fapi/v1/premiumIndex×1" {
		t.Fatalf("top after minute = %q", got)
	}
}
