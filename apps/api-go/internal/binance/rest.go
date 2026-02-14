package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SpotREST         = "https://api.binance.com"
	FuturesREST      = "https://fapi.binance.com"
	BAPIProducts     = "https://www.binance.com/bapi/asset/v2/public/asset-service/product/get-products"
	pairsCacheTTL    = 6 * time.Hour
	oiHistCacheTTL   = 90 * time.Second
	lsrHistCacheTTL  = 90 * time.Second
)

type Client struct {
	http *http.Client

	mu          sync.Mutex
	pairsCache  map[string]cacheEntry[[]string]
	statusCache map[string]cacheEntry[map[string]string]
	oiHistCache map[string]cacheEntry[[]map[string]interface{}]
	lsrCache    map[string]cacheEntry[[]map[string]interface{}]
}

type cacheEntry[T any] struct {
	ts    time.Time
	value T
}

func NewClient() *Client {
	return &Client{
		http:        &http.Client{Timeout: 20 * time.Second},
		pairsCache:  make(map[string]cacheEntry[[]string]),
		statusCache: make(map[string]cacheEntry[map[string]string]),
		oiHistCache: make(map[string]cacheEntry[[]map[string]interface{}]),
		lsrCache:    make(map[string]cacheEntry[[]map[string]interface{}]),
	}
}

func (c *Client) getJSON(ctx context.Context, rawURL string, params map[string]string, timeout time.Duration) (json.RawMessage, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	httpClient := c.http
	if timeout > 0 && timeout != 20*time.Second {
		httpClient = &http.Client{Timeout: timeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance: %s status %d: %s", rawURL, resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

func restBase(market string) string {
	if market == "spot" {
		return SpotREST
	}
	return FuturesREST
}

// ---------------------------------------------------------------------------
// Symbol filter (translates symbol_filter.py)
// ---------------------------------------------------------------------------

var stableBaseAssets = map[string]bool{
	"USDC": true, "USDT": true, "BUSD": true, "FDUSD": true,
	"TUSD": true, "USDP": true, "DAI": true, "FRAX": true,
	"USDD": true, "USDE": true, "USD1": true, "PYUSD": true,
	"RLUSD": true, "LUSD": true, "SUSD": true, "USDS": true,
}

var leadingDigitsRe = regexp.MustCompile(`^\d+`)

func symbolBaseAsset(symbol string) string {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return ""
	}
	base := sym
	for _, quote := range []string{"USDT", "USDC", "BUSD", "FDUSD", "TUSD", "USDP"} {
		if strings.HasSuffix(base, quote) && len(base) > len(quote) {
			base = base[:len(base)-len(quote)]
			break
		}
	}
	base = leadingDigitsRe.ReplaceAllString(base, "")
	return base
}

func IsExcludedSymbol(symbol string) bool {
	if symbol == "" {
		return true
	}
	base := symbolBaseAsset(symbol)
	if base == "" {
		return true
	}
	if stableBaseAssets[base] {
		return true
	}
	return strings.Contains(base, "USD")
}

func FilterExcludedSymbols(symbols []string) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		if !IsExcludedSymbol(s) {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Pairs & status
// ---------------------------------------------------------------------------

func (c *Client) fetchPairsAndStatus(ctx context.Context, market string) ([]string, map[string]string, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/exchangeInfo"
	} else {
		endpoint = FuturesREST + "/fapi/v1/exchangeInfo"
	}
	raw, err := c.getJSON(ctx, endpoint, nil, 0)
	if err != nil {
		return nil, nil, err
	}
	var data struct {
		Symbols []struct {
			Symbol     string `json:"symbol"`
			QuoteAsset string `json:"quoteAsset"`
			Status     string `json:"status"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, nil, err
	}
	var pairs []string
	statusMap := make(map[string]string)
	for _, s := range data.Symbols {
		if s.QuoteAsset != "USDT" || s.Symbol == "" {
			continue
		}
		if s.Status != "" {
			statusMap[s.Symbol] = s.Status
		}
		if s.Status == "TRADING" {
			pairs = append(pairs, s.Symbol)
		}
	}
	sort.Strings(pairs)
	return pairs, statusMap, nil
}

func (c *Client) GetPairs(ctx context.Context, market string) ([]string, error) {
	c.mu.Lock()
	cached, ok := c.pairsCache[market]
	if ok && time.Since(cached.ts) < pairsCacheTTL {
		c.mu.Unlock()
		return cached.value, nil
	}
	c.mu.Unlock()

	pairs, statusMap, err := c.fetchPairsAndStatus(ctx, market)
	if err != nil {
		return nil, err
	}
	pairs = FilterExcludedSymbols(pairs)

	c.mu.Lock()
	c.pairsCache[market] = cacheEntry[[]string]{ts: time.Now(), value: pairs}
	c.statusCache[market] = cacheEntry[map[string]string]{ts: time.Now(), value: statusMap}
	c.mu.Unlock()
	return pairs, nil
}

func (c *Client) GetSymbolStatus(ctx context.Context, market, symbol string) (string, error) {
	sym := strings.ToUpper(symbol)
	c.mu.Lock()
	cached, ok := c.statusCache[market]
	if ok && time.Since(cached.ts) < pairsCacheTTL {
		c.mu.Unlock()
		return cached.value[sym], nil
	}
	c.mu.Unlock()

	_, statusMap, err := c.fetchPairsAndStatus(ctx, market)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.statusCache[market] = cacheEntry[map[string]string]{ts: time.Now(), value: statusMap}
	c.mu.Unlock()
	return statusMap[sym], nil
}

// ---------------------------------------------------------------------------
// Futures data
// ---------------------------------------------------------------------------

func (c *Client) GetFuturesPremiumIndexAll(ctx context.Context) ([]map[string]interface{}, error) {
	raw, err := c.getJSON(ctx, FuturesREST+"/fapi/v1/premiumIndex", nil, 0)
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) GetFuturesPremiumIndex(ctx context.Context, symbol string) (map[string]interface{}, error) {
	raw, err := c.getJSON(ctx, FuturesREST+"/fapi/v1/premiumIndex", map[string]string{"symbol": symbol}, 0)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) GetFuturesOpenInterest(ctx context.Context, symbol string) (map[string]interface{}, error) {
	raw, err := c.getJSON(ctx, FuturesREST+"/fapi/v1/openInterest", map[string]string{"symbol": symbol}, 0)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) GetOpenInterestHist(ctx context.Context, symbol, period string, limit int) ([]map[string]interface{}, error) {
	sym := strings.ToUpper(symbol)
	key := fmt.Sprintf("%s:%s:%d", sym, period, limit)

	c.mu.Lock()
	cached, ok := c.oiHistCache[key]
	if ok && time.Since(cached.ts) < oiHistCacheTTL {
		c.mu.Unlock()
		return cached.value, nil
	}
	c.mu.Unlock()

	raw, err := c.getJSON(ctx, FuturesREST+"/futures/data/openInterestHist", map[string]string{
		"symbol": sym, "period": period, "limit": strconv.Itoa(limit),
	}, 0)
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.oiHistCache[key] = cacheEntry[[]map[string]interface{}]{ts: time.Now(), value: data}
	c.mu.Unlock()
	return data, nil
}

func (c *Client) getLSRHist(ctx context.Context, endpoint, symbol, period string, limit int) ([]map[string]interface{}, error) {
	sym := strings.ToUpper(symbol)
	key := fmt.Sprintf("%s:%s:%s:%d", endpoint, sym, period, limit)

	c.mu.Lock()
	cached, ok := c.lsrCache[key]
	if ok && time.Since(cached.ts) < lsrHistCacheTTL {
		c.mu.Unlock()
		return cached.value, nil
	}
	c.mu.Unlock()

	raw, err := c.getJSON(ctx, FuturesREST+"/futures/data/"+endpoint, map[string]string{
		"symbol": sym, "period": period, "limit": strconv.Itoa(limit),
	}, 0)
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.lsrCache[key] = cacheEntry[[]map[string]interface{}]{ts: time.Now(), value: data}
	c.mu.Unlock()
	return data, nil
}

func (c *Client) GetGlobalLongShortAccountRatio(ctx context.Context, symbol, period string, limit int) ([]map[string]interface{}, error) {
	return c.getLSRHist(ctx, "globalLongShortAccountRatio", symbol, period, limit)
}

func (c *Client) GetTopLongShortAccountRatio(ctx context.Context, symbol, period string, limit int) ([]map[string]interface{}, error) {
	return c.getLSRHist(ctx, "topLongShortAccountRatio", symbol, period, limit)
}

func (c *Client) GetTopLongShortPositionRatio(ctx context.Context, symbol, period string, limit int) ([]map[string]interface{}, error) {
	return c.getLSRHist(ctx, "topLongShortPositionRatio", symbol, period, limit)
}

// ---------------------------------------------------------------------------
// BAPI products
// ---------------------------------------------------------------------------

func (c *Client) GetBAPIProducts(ctx context.Context, includeETF bool) ([]map[string]interface{}, error) {
	etf := "true"
	if !includeETF {
		etf = "false"
	}
	raw, err := c.getJSON(ctx, BAPIProducts, map[string]string{"includeEtf": etf}, 0)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code string                   `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Code == "000000" && resp.Data != nil {
		return resp.Data, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Ticker
// ---------------------------------------------------------------------------

func (c *Client) GetTicker24hAll(ctx context.Context, market string) ([]map[string]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/ticker/24hr"
	} else {
		endpoint = FuturesREST + "/fapi/v1/ticker/24hr"
	}
	raw, err := c.getJSON(ctx, endpoint, nil, 0)
	if err != nil {
		return nil, err
	}
	var data []map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) GetTicker24h(ctx context.Context, market, symbol string) (map[string]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/ticker/24hr"
	} else {
		endpoint = FuturesREST + "/fapi/v1/ticker/24hr"
	}
	raw, err := c.getJSON(ctx, endpoint, map[string]string{"symbol": symbol}, 0)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Orderbook depth
// ---------------------------------------------------------------------------

func (c *Client) GetOrderbookDepth(ctx context.Context, market, symbol string, limit int) (map[string]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/depth"
	} else {
		endpoint = FuturesREST + "/fapi/v1/depth"
	}
	raw, err := c.getJSON(ctx, endpoint, map[string]string{
		"symbol": symbol, "limit": strconv.Itoa(limit),
	}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Klines
// ---------------------------------------------------------------------------

func (c *Client) GetKlines(ctx context.Context, market, symbol, interval string, limit int) ([][]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/klines"
	} else {
		endpoint = FuturesREST + "/fapi/v1/klines"
	}
	raw, err := c.getJSON(ctx, endpoint, map[string]string{
		"symbol": symbol, "interval": interval, "limit": strconv.Itoa(limit),
	}, 0)
	if err != nil {
		return nil, err
	}
	var data [][]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func intervalMs(interval string) int64 {
	if strings.HasSuffix(interval, "m") {
		n, _ := strconv.ParseInt(interval[:len(interval)-1], 10, 64)
		return n * 60 * 1000
	}
	if strings.HasSuffix(interval, "h") {
		n, _ := strconv.ParseInt(interval[:len(interval)-1], 10, 64)
		return n * 3600 * 1000
	}
	if strings.HasSuffix(interval, "d") {
		n, _ := strconv.ParseInt(interval[:len(interval)-1], 10, 64)
		return n * 86400 * 1000
	}
	return 60000
}

func (c *Client) GetKlinesRange(ctx context.Context, market, symbol, interval string, startMs, endMs int64, limit int) ([][]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = SpotREST + "/api/v3/klines"
	} else {
		endpoint = FuturesREST + "/fapi/v1/klines"
	}
	step := intervalMs(interval)
	cur := startMs
	var out [][]interface{}
	for cur <= endMs {
		raw, err := c.getJSON(ctx, endpoint, map[string]string{
			"symbol":    symbol,
			"interval":  interval,
			"startTime": strconv.FormatInt(cur, 10),
			"endTime":   strconv.FormatInt(endMs, 10),
			"limit":     strconv.Itoa(limit),
		}, 0)
		if err != nil {
			return out, err
		}
		var data [][]interface{}
		if err := json.Unmarshal(raw, &data); err != nil || len(data) == 0 {
			break
		}
		out = append(out, data...)
		lastOpen, _ := data[len(data)-1][0].(float64)
		nextStart := int64(lastOpen) + step
		if nextStart <= cur {
			break
		}
		cur = nextStart
	}
	return out, nil
}
