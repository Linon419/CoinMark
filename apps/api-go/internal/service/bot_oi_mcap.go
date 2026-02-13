package service

import (
	"context"
	"sort"
	"strings"

	chrepo "coinmark/api-go/internal/repo/ch"
)

type OIMcapItem struct {
	Symbol        string  `json:"symbol"`
	Asset         string  `json:"asset"`
	OINotionalUSD float64 `json:"oiNotionalUsd"`
	MarketCapUSD  float64 `json:"marketCapUsd"`
	Ratio         float64 `json:"ratio"`
	MarketCapSrc  string  `json:"marketCapSource"`
	UpdatedAtMs   int64   `json:"updatedAtMs"`
}

func baseFromSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") {
		return symbol[:len(symbol)-4]
	}
	return symbol
}

func isPlainUSDTSymbol(symbol string) bool {
	if !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return false
	}
	base := symbol[:len(symbol)-4]
	for i := 0; i < len(base); i++ {
		ch := base[i]
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func GetOIMarketCapRank(ctx context.Context, ch *chrepo.Client, limit int) ([]OIMcapItem, error) {
	oiRows, err := ch.QueryOISnapshots(ctx)
	if err != nil {
		return nil, err
	}
	capRows, err := ch.QueryMarketCaps(ctx, nil)
	if err != nil {
		return nil, err
	}
	capMap := make(map[string]int, len(capRows))
	for i, r := range capRows {
		capMap[r.Asset] = i
	}

	var items []OIMcapItem
	for _, oi := range oiRows {
		if !isPlainUSDTSymbol(oi.Symbol) {
			continue
		}
		asset := baseFromSymbol(oi.Symbol)
		idx, ok := capMap[asset]
		if !ok {
			continue
		}
		cap := capRows[idx]
		if cap.MarketCapUSD <= 0 {
			continue
		}
		ratio := oi.OINotionalUSD / cap.MarketCapUSD
		updAt := oi.EventTimeMs
		if cap.EventTimeMs < updAt {
			updAt = cap.EventTimeMs
		}
		items = append(items, OIMcapItem{
			Symbol:        oi.Symbol,
			Asset:         asset,
			OINotionalUSD: oi.OINotionalUSD,
			MarketCapUSD:  cap.MarketCapUSD,
			Ratio:         ratio,
			MarketCapSrc:  cap.Source,
			UpdatedAtMs:   updAt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ratio > items[j].Ratio })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
