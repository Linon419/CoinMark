package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type exchangeInfoResponse struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		Status       string `json:"status"`
		ContractType string `json:"contractType"`
		QuoteAsset   string `json:"quoteAsset"`
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

func FetchSwapUSDTPerpetualSymbols(ctx context.Context, restBase string, timeout time.Duration) ([]string, error) {
	data, err := fetchExchangeInfo(ctx, strings.TrimRight(restBase, "/")+"/fapi/v1/exchangeInfo", timeout)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(data.Symbols))
	for _, item := range data.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if symbol == "" {
			continue
		}
		if !strings.HasSuffix(symbol, "USDT") {
			continue
		}
		if strings.ToUpper(item.Status) != "TRADING" {
			continue
		}
		if strings.ToUpper(item.ContractType) != "PERPETUAL" {
			continue
		}
		if IsExcludedSymbol(symbol) {
			continue
		}
		out = append(out, symbol)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no swap usdt perpetual symbols found")
	}

	return out, nil
}

func FetchSpotUSDTTradingSymbols(ctx context.Context, restBase string, timeout time.Duration) ([]string, error) {
	data, err := fetchExchangeInfo(ctx, strings.TrimRight(restBase, "/")+"/api/v3/exchangeInfo", timeout)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(data.Symbols))
	for _, item := range data.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if symbol == "" {
			continue
		}
		if !strings.HasSuffix(symbol, "USDT") {
			continue
		}
		if strings.ToUpper(item.QuoteAsset) != "USDT" {
			continue
		}
		if strings.ToUpper(item.Status) != "TRADING" {
			continue
		}
		if IsExcludedSymbol(symbol) {
			continue
		}
		out = append(out, symbol)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no spot usdt trading symbols found")
	}

	return out, nil
}

func fetchExchangeInfo(ctx context.Context, url string, timeout time.Duration) (exchangeInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return exchangeInfoResponse{}, fmt.Errorf("new request: %w", err)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return exchangeInfoResponse{}, fmt.Errorf("request exchangeInfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return exchangeInfoResponse{}, fmt.Errorf("exchangeInfo status=%d", resp.StatusCode)
	}

	var data exchangeInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return exchangeInfoResponse{}, fmt.Errorf("decode exchangeInfo: %w", err)
	}
	return data, nil
}
