package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"coinmark/ingest-go/internal/config"
	"github.com/shopspring/decimal"
)

type Client struct {
	httpClient *http.Client
	cfg        *config.Config

	mu         sync.Mutex
	pairsCache map[string]pairsEntry
}

type pairsEntry struct {
	ts    time.Time
	pairs []string
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		cfg:        cfg,
		pairsCache: make(map[string]pairsEntry),
	}
}

func (c *Client) getJSON(ctx context.Context, endpoint string, query map[string]string, out any) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "coinmark-ingest/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("binance status=%d url=%s", resp.StatusCode, u.String())
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type exchangeInfoResp struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		QuoteAsset   string `json:"quoteAsset"`
		Status       string `json:"status"`
		ContractType string `json:"contractType"`
	} `json:"symbols"`
}

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

func isPlainUSDTSymbol(sym string) bool {
	if !strings.HasSuffix(sym, "USDT") || len(sym) <= 4 {
		return false
	}
	base := sym[:len(sym)-4]
	for i := 0; i < len(base); i++ {
		ch := base[i]
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func (c *Client) GetPairs(ctx context.Context, market string) ([]string, error) {
	c.mu.Lock()
	if e, ok := c.pairsCache[market]; ok && time.Since(e.ts) < 6*time.Hour {
		out := append([]string(nil), e.pairs...)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	var endpoint string
	if market == "spot" {
		endpoint = c.cfg.BinanceSpotREST + "/api/v3/exchangeInfo"
	} else {
		endpoint = c.cfg.BinanceFuturesREST + "/fapi/v1/exchangeInfo"
	}

	var resp exchangeInfoResp
	if err := c.getJSON(ctx, endpoint, nil, &resp); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(resp.Symbols))
	for _, s := range resp.Symbols {
		if s.QuoteAsset != "USDT" || s.Status != "TRADING" || s.Symbol == "" {
			continue
		}
		if market == "swap" && s.ContractType != "PERPETUAL" {
			continue
		}
		if !isPlainUSDTSymbol(s.Symbol) {
			continue
		}
		out = append(out, s.Symbol)
	}
	sort.Strings(out)
	out = FilterExcludedSymbols(out)

	c.mu.Lock()
	c.pairsCache[market] = pairsEntry{ts: time.Now(), pairs: append([]string(nil), out...)}
	c.mu.Unlock()

	return out, nil
}

type ticker24hRow struct {
	Symbol      string `json:"symbol"`
	QuoteVolume string `json:"quoteVolume"`
}

func (c *Client) GetTicker24hAll(ctx context.Context, market string) ([]ticker24hRow, error) {
	var endpoint string
	if market == "spot" {
		endpoint = c.cfg.BinanceSpotREST + "/api/v3/ticker/24hr"
	} else {
		endpoint = c.cfg.BinanceFuturesREST + "/fapi/v1/ticker/24hr"
	}
	var rows []ticker24hRow
	if err := c.getJSON(ctx, endpoint, nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (c *Client) TopSymbolsByVolume(ctx context.Context, market string, topN int) ([]string, error) {
	if topN <= 0 {
		topN = 1
	}
	pairs, err := c.GetPairs(ctx, market)
	if err != nil {
		return nil, err
	}
	valid := make(map[string]struct{}, len(pairs))
	for _, p := range pairs {
		valid[p] = struct{}{}
	}

	rows, err := c.GetTicker24hAll(ctx, market)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		Symbol string
		Qv     float64
	}
	arr := make([]ranked, 0, len(rows))
	for _, row := range rows {
		if !strings.HasSuffix(row.Symbol, "USDT") {
			continue
		}
		if IsExcludedSymbol(row.Symbol) {
			continue
		}
		if _, ok := valid[row.Symbol]; !ok {
			continue
		}
		qv, err := strconv.ParseFloat(row.QuoteVolume, 64)
		if err != nil {
			continue
		}
		arr = append(arr, ranked{Symbol: row.Symbol, Qv: qv})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Qv > arr[j].Qv })
	if len(arr) > topN {
		arr = arr[:topN]
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, item.Symbol)
	}
	return out, nil
}

type premiumIndexRow struct {
	Symbol          string `json:"symbol"`
	LastFundingRate string `json:"lastFundingRate"`
	MarkPrice       string `json:"markPrice"`
}

func (c *Client) GetFuturesPremiumIndexAll(ctx context.Context) ([]premiumIndexRow, error) {
	var rows []premiumIndexRow
	if err := c.getJSON(ctx, c.cfg.BinanceFuturesREST+"/fapi/v1/premiumIndex", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

type openInterestResp struct {
	OpenInterest string `json:"openInterest"`
}

func (c *Client) GetFuturesOpenInterest(ctx context.Context, symbol string) (decimal.Decimal, error) {
	var resp openInterestResp
	err := c.getJSON(ctx, c.cfg.BinanceFuturesREST+"/fapi/v1/openInterest", map[string]string{"symbol": symbol}, &resp)
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(resp.OpenInterest)
}

type bapiResp struct {
	Code string                   `json:"code"`
	Data []map[string]interface{} `json:"data"`
}

func (c *Client) GetBinanceBapiProducts(ctx context.Context) ([]map[string]interface{}, error) {
	var resp bapiResp
	if err := c.getJSON(ctx, c.cfg.BinanceBapiProducts, map[string]string{"includeEtf": "true"}, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "000000" {
		return nil, fmt.Errorf("bapi code=%s", resp.Code)
	}
	return resp.Data, nil
}

func (c *Client) GetBinanceComplianceSymbols(ctx context.Context) ([]map[string]interface{}, error) {
	var resp bapiResp
	if err := c.getJSON(ctx, c.cfg.BinanceBapiCompliance, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "000000" {
		return nil, fmt.Errorf("compliance code=%s", resp.Code)
	}
	return resp.Data, nil
}

func (c *Client) GetKlines(ctx context.Context, market, symbol, interval string, limit int) ([][]interface{}, error) {
	var endpoint string
	if market == "spot" {
		endpoint = c.cfg.BinanceSpotREST + "/api/v3/klines"
	} else {
		endpoint = c.cfg.BinanceFuturesREST + "/fapi/v1/klines"
	}
	var rows [][]interface{}
	err := c.getJSON(ctx, endpoint, map[string]string{
		"symbol":   symbol,
		"interval": interval,
		"limit":    strconv.Itoa(limit),
	}, &rows)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
