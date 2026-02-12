package service

import (
	"context"
	"math"
	"sort"

	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
)

type FundingRateItem struct {
	Symbol          string  `json:"symbol"`
	LastFundingRate float64 `json:"lastFundingRate"`
	MarkPrice       float64 `json:"markPrice"`
	TimeMs          int64   `json:"timeMs"`
}

func GetFundingRateTop(ctx context.Context, ch *chrepo.Client, limit int, order string) ([]FundingRateItem, error) {
	rows, err := ch.QueryFundingSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	sortFundingRows(rows, order)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]FundingRateItem, len(rows))
	for i, r := range rows {
		out[i] = FundingRateItem{
			Symbol:          r.Symbol,
			LastFundingRate: r.LastFundingRate,
			MarkPrice:       r.MarkPrice,
			TimeMs:          r.EventTimeMs,
		}
	}
	return out, nil
}

func sortFundingRows(rows []model.CHFundingRow, order string) {
	switch order {
	case "abs":
		sort.Slice(rows, func(i, j int) bool {
			return math.Abs(rows[i].LastFundingRate) > math.Abs(rows[j].LastFundingRate)
		})
	case "asc":
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].LastFundingRate < rows[j].LastFundingRate
		})
	default:
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].LastFundingRate > rows[j].LastFundingRate
		})
	}
}
