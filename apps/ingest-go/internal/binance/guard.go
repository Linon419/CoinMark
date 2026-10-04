package binance

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// restGuard 币安 REST 限流保护（按域名分开：合约 fapi、现货 api 各有各的限额）：
//   - 收到 429/418 后，在币安给的时间之前不再发请求。限流后继续请求会被封 IP，封禁中继续请求会延长封禁（最长 3 天）。
//   - 已用权重超过一半时记日志，方便找出是谁在大量请求。
type restGuard struct {
	mu           sync.Mutex
	blockedUntil map[string]time.Time
	lastWarn     map[string]time.Time
	now          func() time.Time
}

var bannedUntilRe = regexp.MustCompile(`banned until (\d{13})`)

// 每分钟权重上限：合约 2400、现货 6000。超过一半记日志。
func restWeightLimit(host string) int {
	if host == "fapi.binance.com" {
		return 2400
	}
	return 6000
}

func newRestGuard() *restGuard {
	return &restGuard{blockedUntil: map[string]time.Time{}, lastWarn: map[string]time.Time{}, now: time.Now}
}

// check 封禁/限流期间直接返回错误，不发请求。
func (g *restGuard) check(host string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.blockedUntil[host]; ok && g.now().Before(until) {
		return fmt.Errorf("binance: %s rate limited, paused until %s", host, until.UTC().Format("15:04:05"))
	}
	return nil
}

// observe 记录响应：429/418 时暂停到币安给的时间；权重过半时记日志。
func (g *restGuard) observe(host, path string, resp *http.Response, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
		until := now.Add(time.Minute)
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			until = now.Add(time.Duration(s) * time.Second)
		}
		if m := bannedUntilRe.FindSubmatch(body); m != nil {
			if ms, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil && time.UnixMilli(ms).After(until) {
				until = time.UnixMilli(ms)
			}
		}
		if until.After(g.blockedUntil[host]) {
			g.blockedUntil[host] = until
			log.Printf("binance: %s status %d on %s, pausing REST until %s UTC", host, resp.StatusCode, path, until.UTC().Format("01-02 15:04:05"))
		}
		return
	}
	used, err := strconv.Atoi(resp.Header.Get("X-Mbx-Used-Weight-1m"))
	if err != nil || used*2 < restWeightLimit(host) {
		return
	}
	if now.Sub(g.lastWarn[host]) >= 10*time.Second {
		g.lastWarn[host] = now
		log.Printf("binance: %s used weight %d/%d (last %s)", host, used, restWeightLimit(host), path)
	}
}
