package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"coinmark/api-go/internal/config"
	"coinmark/api-go/internal/repo/sqlite"
)

type LSVolumeItem struct {
	Symbol          string   `json:"symbol"`
	Factor          *float64 `json:"factor"`
	CurrentNotional float64  `json:"currentNotional"`
	AvgNotional     *float64 `json:"avgNotional"`
}

type LSVolumeRank struct {
	Market        string         `json:"market"`
	Bucket        string         `json:"bucket"`
	BucketStartMs int64          `json:"bucketStartMs"`
	Long          []LSVolumeItem `json:"long"`
	Short         []LSVolumeItem `json:"short"`
}

func bucketMs(bucket string) int64 {
	switch bucket {
	case "15m":
		return 15 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	}
	return 60 * 60 * 1000
}

func floorMs(tsMs int64, bucket string) int64 {
	size := bucketMs(bucket)
	return (tsMs / size) * size
}

func GetLongShortVolumeRank(ctx context.Context, store *sqlite.Store, cfg *config.Config, market, bucket string, limit int) (*LSVolumeRank, error) {
	nowMs := time.Now().UnixMilli()
	curStart := floorMs(nowMs, bucket)
	histStart := curStart - bucketMs(bucket)*int64(cfg.RankHistoryBuckets)

	rows, err := queryLSVolumeMain(ctx, store, market, bucket, curStart, histStart, cfg.RankMinAvgNotional)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return queryLSVolumeFallback(ctx, store, market, bucket, curStart, limit)
	}

	var longs, shorts []LSVolumeItem
	for _, r := range rows {
		if r.LongFactor != nil {
			longs = append(longs, LSVolumeItem{
				Symbol:          r.Symbol,
				Factor:          r.LongFactor,
				CurrentNotional: r.BuyNotional,
				AvgNotional:     r.AvgBuy,
			})
		}
		if r.ShortFactor != nil {
			shorts = append(shorts, LSVolumeItem{
				Symbol:          r.Symbol,
				Factor:          r.ShortFactor,
				CurrentNotional: r.SellNotional,
				AvgNotional:     r.AvgSell,
			})
		}
	}
	sort.Slice(longs, func(i, j int) bool { return ptrVal(longs[i].Factor) > ptrVal(longs[j].Factor) })
	sort.Slice(shorts, func(i, j int) bool { return ptrVal(shorts[i].Factor) > ptrVal(shorts[j].Factor) })
	if limit > 0 {
		if len(longs) > limit {
			longs = longs[:limit]
		}
		if len(shorts) > limit {
			shorts = shorts[:limit]
		}
	}
	return &LSVolumeRank{Market: market, Bucket: bucket, BucketStartMs: curStart, Long: longs, Short: shorts}, nil
}

func ptrVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

type lsRow struct {
	Symbol       string
	BuyNotional  float64
	SellNotional float64
	AvgBuy       *float64
	AvgSell      *float64
	LongFactor   *float64
	ShortFactor  *float64
}

func queryLSVolumeMain(ctx context.Context, store *sqlite.Store, market, bucket string, curStart, histStart int64, minAvg float64) ([]lsRow, error) {
	sql := `
WITH cur AS (
  SELECT symbol, taker_buy_notional, taker_sell_notional
  FROM trade_buckets
  WHERE market = ? AND bucket = ? AND bucket_start_ms = ?
),
hist AS (
  SELECT symbol,
         AVG(taker_buy_notional) AS avg_buy,
         AVG(taker_sell_notional) AS avg_sell
  FROM trade_buckets
  WHERE market = ? AND bucket = ? AND bucket_start_ms >= ? AND bucket_start_ms < ?
  GROUP BY symbol
),
j AS (
  SELECT cur.symbol,
         cur.taker_buy_notional,
         cur.taker_sell_notional,
         hist.avg_buy,
         hist.avg_sell,
         CASE WHEN hist.avg_buy > 0 THEN cur.taker_buy_notional / hist.avg_buy ELSE NULL END AS long_factor,
         CASE WHEN hist.avg_sell > 0 THEN cur.taker_sell_notional / hist.avg_sell ELSE NULL END AS short_factor
  FROM cur
  JOIN hist ON hist.symbol = cur.symbol
  WHERE hist.avg_buy >= ? OR hist.avg_sell >= ?
)
SELECT symbol, taker_buy_notional, taker_sell_notional, avg_buy, avg_sell, long_factor, short_factor FROM j`

	type rawRow struct {
		Symbol       string   `db:"symbol"`
		BuyNotional  float64  `db:"taker_buy_notional"`
		SellNotional float64  `db:"taker_sell_notional"`
		AvgBuy       *float64 `db:"avg_buy"`
		AvgSell      *float64 `db:"avg_sell"`
		LongFactor   *float64 `db:"long_factor"`
		ShortFactor  *float64 `db:"short_factor"`
	}

	var raw []rawRow
	err := store.SelectContext(ctx, &raw, sql,
		market, bucket, curStart,
		market, bucket, histStart, curStart,
		minAvg, minAvg,
	)
	if err != nil {
		return nil, fmt.Errorf("ls_volume query: %w", err)
	}
	out := make([]lsRow, len(raw))
	for i, r := range raw {
		out[i] = lsRow{
			Symbol: r.Symbol, BuyNotional: r.BuyNotional, SellNotional: r.SellNotional,
			AvgBuy: r.AvgBuy, AvgSell: r.AvgSell, LongFactor: r.LongFactor, ShortFactor: r.ShortFactor,
		}
	}
	return out, nil
}

func queryLSVolumeFallback(ctx context.Context, store *sqlite.Store, market, bucket string, curStart int64, limit int) (*LSVolumeRank, error) {
	sql := `SELECT symbol, taker_buy_notional, taker_sell_notional
FROM trade_buckets WHERE market = ? AND bucket = ? AND bucket_start_ms = ?`

	type curRow struct {
		Symbol       string  `db:"symbol"`
		BuyNotional  float64 `db:"taker_buy_notional"`
		SellNotional float64 `db:"taker_sell_notional"`
	}
	var rows []curRow
	if err := store.SelectContext(ctx, &rows, sql, market, bucket, curStart); err != nil {
		return nil, err
	}

	longRows := make([]curRow, len(rows))
	copy(longRows, rows)
	sort.Slice(longRows, func(i, j int) bool { return longRows[i].BuyNotional > longRows[j].BuyNotional })

	shortRows := make([]curRow, len(rows))
	copy(shortRows, rows)
	sort.Slice(shortRows, func(i, j int) bool { return shortRows[i].SellNotional > shortRows[j].SellNotional })

	if limit > 0 {
		if len(longRows) > limit {
			longRows = longRows[:limit]
		}
		if len(shortRows) > limit {
			shortRows = shortRows[:limit]
		}
	}

	longs := make([]LSVolumeItem, len(longRows))
	for i, r := range longRows {
		longs[i] = LSVolumeItem{Symbol: r.Symbol, CurrentNotional: r.BuyNotional}
	}
	shorts := make([]LSVolumeItem, len(shortRows))
	for i, r := range shortRows {
		shorts[i] = LSVolumeItem{Symbol: r.Symbol, CurrentNotional: r.SellNotional}
	}
	return &LSVolumeRank{Market: market, Bucket: bucket, BucketStartMs: curStart, Long: longs, Short: shorts}, nil
}
