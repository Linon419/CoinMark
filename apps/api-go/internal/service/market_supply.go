package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const supplyCacheTTL = 6 * time.Hour

type SupplySnapshot struct {
	SymbolPair        string   `json:"symbol_pair,omitempty"`
	CirculatingSupply *float64 `json:"circulating_supply,omitempty"`
	TotalSupply       *float64 `json:"total_supply,omitempty"`
	MaxSupply         *float64 `json:"max_supply,omitempty"`
	MarketCap         *float64 `json:"market_cap,omitempty"`
	Price             *float64 `json:"price,omitempty"`
	Source            string   `json:"source,omitempty"`
}

type SupplyCache struct {
	mu     sync.Mutex
	cache  map[string]*SupplySnapshot
	ts     time.Time
	client *http.Client
}

func NewSupplyCache() *SupplyCache {
	return &SupplyCache{
		cache:  make(map[string]*SupplySnapshot),
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (sc *SupplyCache) Get(ctx context.Context, symbol string) (*SupplySnapshot, error) {
	sc.mu.Lock()
	if time.Since(sc.ts) > supplyCacheTTL || len(sc.cache) == 0 {
		sc.mu.Unlock()
		if err := sc.refresh(ctx); err != nil {
			return nil, err
		}
		sc.mu.Lock()
	}
	s := sc.cache[strings.ToUpper(symbol)]
	sc.mu.Unlock()
	return s, nil
}

func (sc *SupplyCache) refresh(ctx context.Context) error {
	sc.mu.Lock()
	if time.Since(sc.ts) <= supplyCacheTTL && len(sc.cache) > 0 {
		sc.mu.Unlock()
		return nil
	}
	sc.mu.Unlock()

	next := make(map[string]*SupplySnapshot)

	apex, _ := sc.fetchApexCompliance(ctx)
	for _, row := range apex {
		base := strings.ToUpper(str(row, "name"))
		pair := strings.ToUpper(str(row, "symbol"))
		if base == "" && strings.HasSuffix(pair, "USDT") {
			base = pair[:len(pair)-4]
		}
		if base == "" {
			continue
		}
		entry := &SupplySnapshot{
			SymbolPair:        pair,
			CirculatingSupply: optFloat(row, "circulatingSupply"),
			TotalSupply:       optFloat(row, "totalSupply"),
			MaxSupply:         optFloat(row, "maxSupply"),
			MarketCap:         optFloat(row, "marketCap"),
			Price:             optFloat(row, "price"),
			Source:            "binance_apex",
		}
		mergeSupply(next, base, entry)
	}

	products, _ := sc.fetchAssetProducts(ctx)
	for _, row := range products {
		base := strings.ToUpper(str(row, "b"))
		quote := strings.ToUpper(str(row, "q"))
		pair := strings.ToUpper(str(row, "s"))
		if base == "" && strings.HasSuffix(pair, "USDT") {
			base = pair[:len(pair)-4]
			quote = "USDT"
		}
		if base == "" || (quote != "" && quote != "USDT") {
			continue
		}
		entry := &SupplySnapshot{
			SymbolPair:        pair,
			CirculatingSupply: optFloat(row, "cs"),
			Price:             optFloat(row, "c"),
			Source:            "binance_asset",
		}
		mergeSupply(next, base, entry)
	}

	sc.mu.Lock()
	sc.cache = next
	sc.ts = time.Now()
	sc.mu.Unlock()
	return nil
}

func mergeSupply(m map[string]*SupplySnapshot, base string, entry *SupplySnapshot) {
	cur, ok := m[base]
	if !ok {
		m[base] = entry
		return
	}
	if cur.CirculatingSupply == nil && entry.CirculatingSupply != nil {
		cur.CirculatingSupply = entry.CirculatingSupply
	}
	if cur.TotalSupply == nil && entry.TotalSupply != nil {
		cur.TotalSupply = entry.TotalSupply
	}
	if cur.MaxSupply == nil && entry.MaxSupply != nil {
		cur.MaxSupply = entry.MaxSupply
	}
	if cur.MarketCap == nil && entry.MarketCap != nil {
		cur.MarketCap = entry.MarketCap
	}
	if cur.Price == nil && entry.Price != nil {
		cur.Price = entry.Price
	}
	if cur.Source == "" && entry.Source != "" {
		cur.Source = entry.Source
	}
}

func (sc *SupplyCache) fetchApexCompliance(ctx context.Context) ([]map[string]interface{}, error) {
	return sc.fetchJSON(ctx, "https://www.binance.com/bapi/apex/v1/friendly/apex/marketing/complianceSymbolList", nil)
}

func (sc *SupplyCache) fetchAssetProducts(ctx context.Context) ([]map[string]interface{}, error) {
	return sc.fetchJSON(ctx, "https://www.binance.com/exchange-api/v2/public/asset-service/product/get-products", map[string]string{"includeEtf": "true"})
}

func (sc *SupplyCache) fetchJSON(ctx context.Context, rawURL string, params map[string]string) ([]map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "coinmark/1.0")
	if len(params) > 0 {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := sc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market_supply: %d %s", resp.StatusCode, string(body))
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	data := raw["data"]
	switch d := data.(type) {
	case []interface{}:
		return toMapSlice(d), nil
	case map[string]interface{}:
		if inner, ok := d["data"].([]interface{}); ok {
			return toMapSlice(inner), nil
		}
	}
	return nil, nil
}

func toMapSlice(items []interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func optFloat(m map[string]interface{}, key string) *float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		return &t
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}
