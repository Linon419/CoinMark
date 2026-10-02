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

// FetchTopUSDTSymbolsByMarketCap 返回按市值从高到低排好的全部 USDT 交易对（不截断，由调用方按市场过滤后取前 N）。
func FetchTopUSDTSymbolsByMarketCap(ctx context.Context, timeout time.Duration) ([]string, error) {
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

	return rankMarketCapRows(payload.Data), nil
}

// rankMarketCapRows 按 价格×流通量 排序。排除 bStocks（Binance 的美股代币）：它们只在现货交易且“市值”很大，
// 会占满排名，导致合约只能选到个位数的币。
func rankMarketCapRows(rows []map[string]any) []string {
	bestBySymbol := make(map[string]marketCapCandidate, len(rows))
	for _, row := range rows {
		if isBStock(row) {
			continue
		}
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

	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, item.Symbol)
	}
	return out
}

func isBStock(row map[string]any) bool {
	tags, _ := row["tags"].([]any)
	for _, t := range tags {
		if s, _ := t.(string); s == "bStocks" {
			return true
		}
	}
	return false
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
