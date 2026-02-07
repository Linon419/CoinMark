package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
