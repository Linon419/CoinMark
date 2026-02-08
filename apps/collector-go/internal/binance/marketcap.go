package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const binanceBAPIProductsURL = "https://www.binance.com/bapi/asset/v2/public/asset-service/product/get-products?includeEtf=true"

type bapiProductsResponse struct {
	Data []map[string]any `json:"data"`
}

type marketCapCandidate struct {
	Symbol   string
	Cap      float64
	Priority int
	QuoteVol float64
}

func FetchTopUSDTSymbolsByMarketCap(ctx context.Context, limit int, timeout time.Duration) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binanceBAPIProductsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "coinmark-collector-go")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request bapi get-products: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bapi get-products status=%d", resp.StatusCode)
	}

	var payload bapiProductsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode bapi get-products: %w", err)
	}
	if len(payload.Data) == 0 {
		return nil, fmt.Errorf("bapi get-products empty data")
	}

	bestBySymbol := make(map[string]marketCapCandidate, len(payload.Data))
	for _, row := range payload.Data {
		base := strings.ToUpper(strings.TrimSpace(anyToString(row["b"])))
		if base == "" {
			continue
		}

		pm := strings.ToUpper(strings.TrimSpace(anyToString(row["pm"])))
		priority := 10
		switch pm {
		case "USDT":
			priority = 0
		case "USDC":
			priority = 1
		default:
			continue
		}

		price, ok := anyToFloat(row["c"])
		if !ok || price <= 0 {
			continue
		}
		supply, ok := anyToFloat(row["cs"])
		if !ok || supply <= 0 {
			continue
		}
		quoteVol, _ := anyToFloat(row["qv"])

		symbol := base + "USDT"
		candidate := marketCapCandidate{
			Symbol:   symbol,
			Cap:      price * supply,
			Priority: priority,
			QuoteVol: quoteVol,
		}

		prev, exists := bestBySymbol[symbol]
		if !exists {
			bestBySymbol[symbol] = candidate
			continue
		}
		if candidate.Priority < prev.Priority || (candidate.Priority == prev.Priority && candidate.QuoteVol > prev.QuoteVol) {
			bestBySymbol[symbol] = candidate
		}
	}

	arr := make([]marketCapCandidate, 0, len(bestBySymbol))
	for _, item := range bestBySymbol {
		arr = append(arr, item)
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].Cap == arr[j].Cap {
			return arr[i].Symbol < arr[j].Symbol
		}
		return arr[i].Cap > arr[j].Cap
	})

	if len(arr) > limit {
		arr = arr[:limit]
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, item.Symbol)
	}
	return out, nil
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	default:
		return ""
	}
}

func anyToFloat(v any) (float64, bool) {
	s := strings.TrimSpace(anyToString(v))
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

