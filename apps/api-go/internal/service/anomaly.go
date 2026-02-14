package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
	"coinmark/api-go/internal/repo/sqlite"
)

// ---------------------------------------------------------------------------
// Candle + ZigZag pivots
// ---------------------------------------------------------------------------

type candle struct {
	StartMs       int64
	Open          float64
	High          float64
	Low           float64
	Close         float64
	QuoteNotional float64
}

func findPivots(candles []candle, reversalPct float64, minBars int) [][2]float64 {
	if len(candles) < 2 {
		return nil
	}
	rev := math.Abs(reversalPct)
	type pivot struct {
		ts    float64
		price float64
	}
	var pivots []pivot
	trend := 0
	hi := candles[0].High
	lo := candles[0].Low
	hiIdx, loIdx := 0, 0

	for i := 1; i < len(candles); i++ {
		c := candles[i]
		switch trend {
		case 0:
			if c.High >= hi {
				hi = c.High
				hiIdx = i
			}
			if c.Low <= lo {
				lo = c.Low
				loIdx = i
			}
			if c.High >= lo*(1+rev) && i-loIdx >= minBars {
				pivots = append(pivots, pivot{float64(candles[loIdx].StartMs), lo})
				trend = 1
				hi = c.High
				hiIdx = i
			} else if c.Low <= hi*(1-rev) && i-hiIdx >= minBars {
				pivots = append(pivots, pivot{float64(candles[hiIdx].StartMs), hi})
				trend = -1
				lo = c.Low
				loIdx = i
			}
		case 1:
			if c.High >= hi {
				hi = c.High
				hiIdx = i
			}
			if c.Low <= hi*(1-rev) && i-hiIdx >= minBars {
				pivots = append(pivots, pivot{float64(candles[hiIdx].StartMs), hi})
				trend = -1
				lo = c.Low
				loIdx = i
			}
		case -1:
			if c.Low <= lo {
				lo = c.Low
				loIdx = i
			}
			if c.High >= lo*(1+rev) && i-loIdx >= minBars {
				pivots = append(pivots, pivot{float64(candles[loIdx].StartMs), lo})
				trend = 1
				hi = c.High
				hiIdx = i
			}
		}
	}

	out := make([][2]float64, len(pivots))
	for i, p := range pivots {
		out[i] = [2]float64{p.ts, p.price}
	}
	return out
}

func clusterPrices(prices []float64, clusterPct float64) []float64 {
	if len(prices) == 0 {
		return nil
	}
	sorted := make([]float64, len(prices))
	copy(sorted, prices)
	sort.Float64s(sorted)

	clusters := [][]float64{{sorted[0]}}
	for _, p := range sorted[1:] {
		cur := clusters[len(clusters)-1]
		ref := cur[len(cur)-1]
		tol := math.Max(math.Abs(ref)*clusterPct, math.Abs(p)*clusterPct)
		if math.Abs(p-ref) <= tol {
			clusters[len(clusters)-1] = append(cur, p)
		} else {
			clusters = append(clusters, []float64{p})
		}
	}
	out := make([]float64, len(clusters))
	for i, c := range clusters {
		sort.Float64s(c)
		out[i] = c[len(c)/2]
	}
	return out
}

func quantizeLevelPrice(p float64) float64 {
	ap := math.Abs(p)
	var step float64
	switch {
	case ap < 0.01:
		step = 0.00000001
	case ap < 1:
		step = 0.000001
	case ap < 10:
		step = 0.0001
	case ap < 100:
		step = 0.001
	case ap < 1000:
		step = 0.01
	case ap < 10000:
		step = 0.1
	default:
		step = 1
	}
	return math.Round(p/step) * step
}

// ---------------------------------------------------------------------------
// RefreshSRLevels
// ---------------------------------------------------------------------------

func RefreshSRLevels(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, market string, symbols []string, lookback4h int, zigzagPct, clusterPct float64, minTouches, maxLevels int) error {
	if len(symbols) == 0 {
		return nil
	}
	bucket4hMs := int64(4 * 60 * 60 * 1000)
	var allValues []map[string]interface{}

	for _, sym := range symbols {
		rows, err := ch.QueryTradeBuckets(ctx, market, sym, nil, "4h", 0, 0, "desc", lookback4h)
		if err != nil || len(rows) < 20 {
			continue
		}
		var candles []candle
		for i := len(rows) - 1; i >= 0; i-- {
			r := rows[i]
			if r.OpenPrice == nil || r.HighPrice == nil || r.LowPrice == nil || r.ClosePrice == nil {
				continue
			}
			candles = append(candles, candle{
				StartMs: r.BucketStartMs, Open: *r.OpenPrice, High: *r.HighPrice,
				Low: *r.LowPrice, Close: *r.ClosePrice, QuoteNotional: r.QuoteNotional,
			})
		}
		pivots := findPivots(candles, zigzagPct, 3)
		if len(pivots) == 0 {
			continue
		}
		prices := make([]float64, len(pivots))
		for i, p := range pivots {
			prices[i] = p[1]
		}
		levels := clusterPrices(prices, clusterPct)
		if len(levels) == 0 {
			continue
		}

		nowMs := time.Now().UnixMilli()
		var values []map[string]interface{}
		for _, lp := range levels {
			level := quantizeLevelPrice(lp)
			tol := math.Abs(level) * clusterPct
			touches := 0
			lastTouchMs := int64(0)
			touchQV := 0.0
			for _, c := range candles {
				if c.Low-tol <= level && level <= c.High+tol {
					touches++
					t := c.StartMs + bucket4hMs
					if t > lastTouchMs {
						lastTouchMs = t
					}
					touchQV += c.QuoteNotional
				}
			}
			if touches < minTouches {
				continue
			}
			recencyDays := math.Max(0, float64(nowMs-lastTouchMs)/86400000.0)
			recencyFactor := math.Exp(-recencyDays / 10.0)
			volumeBonus := 1.0 + math.Log1p(touchQV/1000000.0)
			strength := float64(touches) * volumeBonus * recencyFactor
			values = append(values, map[string]interface{}{
				"market": market, "symbol": sym, "level_price": level,
				"timeframe": "4h", "touches": touches, "strength_score": strength,
				"last_touch_ms": lastTouchMs,
			})
		}
		sort.Slice(values, func(i, j int) bool {
			return values[i]["strength_score"].(float64) > values[j]["strength_score"].(float64)
		})
		if len(values) > maxLevels {
			values = values[:maxLevels]
		}
		allValues = append(allValues, values...)
	}
	if len(allValues) == 0 {
		return nil
	}
	return upsertSRLevels(ctx, store, allValues)
}

func upsertSRLevels(ctx context.Context, store *sqlite.Store, values []map[string]interface{}) error {
	sql := `INSERT INTO sr_levels (market, symbol, level_price, timeframe, touches, strength_score, last_touch_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(market, symbol, timeframe, level_price) DO UPDATE SET
  touches = excluded.touches,
  strength_score = excluded.strength_score,
  last_touch_ms = excluded.last_touch_ms,
  updated_at = CURRENT_TIMESTAMP`

	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, v := range values {
			if _, err := tx.Exec(sql,
				v["market"], v["symbol"], v["level_price"], v["timeframe"],
				v["touches"], v["strength_score"], v["last_touch_ms"],
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// ScanAnomalies
// ---------------------------------------------------------------------------

func ScanAnomalies(ctx context.Context, ch *chrepo.Client, store *sqlite.Store, market string, symbols []string, history15m int, breakoutMarginPct, volumeSpikeFactor, amplitudeSpikeFactor float64) (int, error) {
	symbols = binance.FilterExcludedSymbols(symbols)
	if len(symbols) == 0 {
		return 0, nil
	}

	nowMs := time.Now().UnixMilli()
	b15 := int64(15 * 60 * 1000)
	curStart := (nowMs / b15) * b15
	lastClosedStart := curStart - b15
	startMs := lastClosedStart - int64(history15m+4)*b15

	tradeRows, err := ch.QueryTradeBuckets(ctx, market, "", symbols, "15m", startMs, lastClosedStart, "asc", 0)
	if err != nil {
		return 0, err
	}

	series := make(map[string][]model.CHTradeRow)
	for _, r := range tradeRows {
		series[r.Symbol] = append(series[r.Symbol], r)
	}

	var srLevels []model.SRLevel
	if err := store.SelectContext(ctx, &srLevels,
		`SELECT * FROM sr_levels WHERE market = ? AND timeframe = '4h' AND symbol IN (`+placeholders(len(symbols))+`) ORDER BY symbol ASC, strength_score DESC`,
		toArgs(market, symbols)...,
	); err != nil {
		return 0, err
	}
	levelsBySym := make(map[string][]model.SRLevel)
	for _, l := range srLevels {
		levelsBySym[l.Symbol] = append(levelsBySym[l.Symbol], l)
	}

	var newEvents []map[string]interface{}
	for sym, s := range series {
		if binance.IsExcludedSymbol(sym) || len(s) < 10 {
			continue
		}
		lvls := levelsBySym[sym]
		if len(lvls) == 0 {
			continue
		}
		var cands []candle
		for _, tb := range s {
			if tb.OpenPrice == nil || tb.HighPrice == nil || tb.LowPrice == nil || tb.ClosePrice == nil {
				continue
			}
			cands = append(cands, candle{
				StartMs: tb.BucketStartMs, Open: *tb.OpenPrice, High: *tb.HighPrice,
				Low: *tb.LowPrice, Close: *tb.ClosePrice, QuoteNotional: tb.QuoteNotional,
			})
		}
		if len(cands) < 10 {
			continue
		}
		last := cands[len(cands)-1]
		if len(cands) < 3 {
			continue
		}
		prev1 := cands[len(cands)-2]
		prev2 := cands[len(cands)-3]

		hist := cands
		if len(hist) > history15m+1 {
			hist = hist[len(hist)-history15m-1 : len(hist)-1]
		} else if len(hist) > 1 {
			hist = hist[:len(hist)-1]
		}

		avgVol, avgAmp := 0.0, 0.0
		if len(hist) >= 10 {
			for _, c := range hist {
				avgVol += c.QuoteNotional
				if c.Open > 0 {
					avgAmp += (c.High - c.Low) / c.Open
				}
			}
			avgVol /= float64(len(hist))
			avgAmp /= float64(len(hist))
		}

		curAmp := 0.0
		if last.Open > 0 {
			curAmp = (last.High - last.Low) / last.Open
		}
		var volFactor, ampFactor *float64
		if avgVol > 0 {
			v := last.QuoteNotional / avgVol
			volFactor = &v
		}
		if avgAmp > 0 {
			a := curAmp / avgAmp
			ampFactor = &a
		}

		if avgVol > 0 && last.QuoteNotional >= avgVol*volumeSpikeFactor {
			details, _ := json.Marshal(map[string]interface{}{
				"bucketStartMs": last.StartMs, "quoteNotional": last.QuoteNotional,
				"avgQuoteNotional": avgVol, "volumeFactor": volFactor,
			})
			newEvents = append(newEvents, map[string]interface{}{
				"market": market, "symbol": sym, "event_type": "volume_spike",
				"tf_signal": "15m", "tf_level": nil,
				"event_time_ms": last.StartMs + b15,
				"title":   fmt.Sprintf("%s 量能异常放大", sym),
				"details": string(details),
			})
		}
		if avgAmp > 0 && curAmp >= avgAmp*amplitudeSpikeFactor && curAmp >= 0.005 {
			details, _ := json.Marshal(map[string]interface{}{
				"bucketStartMs": last.StartMs, "amplitude": curAmp,
				"avgAmplitude": avgAmp, "amplitudeFactor": ampFactor,
			})
			newEvents = append(newEvents, map[string]interface{}{
				"market": market, "symbol": sym, "event_type": "amplitude_spike",
				"tf_signal": "15m", "tf_level": nil,
				"event_time_ms": last.StartMs + b15,
				"title":   fmt.Sprintf("%s 振幅异常放大", sym),
				"details": string(details),
			})
		}

		maxLvl := 12
		if len(lvls) < maxLvl {
			maxLvl = len(lvls)
		}
		for _, lvl := range lvls[:maxLvl] {
			lp := lvl.LevelPrice
			if lp <= 0 {
				continue
			}
			up := lp * (1 + breakoutMarginPct)
			down := lp * (1 - breakoutMarginPct)

			if prev1.Close > up && last.Close > up && prev2.Close <= lp {
				details, _ := json.Marshal(map[string]interface{}{
					"bucketStartMs": last.StartMs, "levelPrice": lp,
					"marginPct": breakoutMarginPct, "confirmCloses": 2,
					"close": last.Close, "prevClose": prev1.Close,
					"touches": lvl.Touches, "strengthScore": lvl.StrengthScore,
					"volumeFactor": volFactor, "amplitude": curAmp,
				})
				newEvents = append(newEvents, map[string]interface{}{
					"market": market, "symbol": sym, "event_type": "breakout_up",
					"tf_signal": "15m", "tf_level": "4h",
					"event_time_ms": last.StartMs + b15,
					"title":   fmt.Sprintf("%s 突破阻力 %v", sym, lp),
					"details": string(details),
				})
			}
			if prev1.Close < down && last.Close < down && prev2.Close >= lp {
				details, _ := json.Marshal(map[string]interface{}{
					"bucketStartMs": last.StartMs, "levelPrice": lp,
					"marginPct": breakoutMarginPct, "confirmCloses": 2,
					"close": last.Close, "prevClose": prev1.Close,
					"touches": lvl.Touches, "strengthScore": lvl.StrengthScore,
					"volumeFactor": volFactor, "amplitude": curAmp,
				})
				newEvents = append(newEvents, map[string]interface{}{
					"market": market, "symbol": sym, "event_type": "breakout_down",
					"tf_signal": "15m", "tf_level": "4h",
					"event_time_ms": last.StartMs + b15,
					"title":   fmt.Sprintf("%s 跌破支撑 %v", sym, lp),
					"details": string(details),
				})
			}
		}
	}

	if len(newEvents) == 0 {
		return 0, nil
	}
	return insertAnomalyEvents(ctx, store, newEvents)
}

func insertAnomalyEvents(ctx context.Context, store *sqlite.Store, events []map[string]interface{}) (int, error) {
	sqlCheck := `SELECT id FROM anomaly_events
WHERE market = ? AND symbol = ? AND event_type = ? AND tf_signal = ? AND event_time_ms = ?
LIMIT 1`
	sqlInsert := `INSERT INTO anomaly_events (market, symbol, event_type, tf_signal, tf_level, event_time_ms, title, details)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	inserted := 0
	err := store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, e := range events {
			var existedID int64
			checkErr := tx.GetContext(ctx, &existedID, sqlCheck,
				e["market"], e["symbol"], e["event_type"], e["tf_signal"], e["event_time_ms"],
			)
			if checkErr == nil {
				continue
			}
			if checkErr != sql.ErrNoRows {
				return checkErr
			}

			res, insErr := tx.Exec(sqlInsert,
				e["market"], e["symbol"], e["event_type"], e["tf_signal"],
				e["tf_level"], e["event_time_ms"], e["title"], e["details"],
			)
			if insErr != nil {
				continue
			}
			n, _ := res.RowsAffected()
			inserted += int(n)
		}
		return nil
	})
	return inserted, err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	s := "?"
	for i := 1; i < n; i++ {
		s += ",?"
	}
	return s
}

func toArgs(first string, rest []string) []interface{} {
	args := make([]interface{}, 0, 1+len(rest))
	args = append(args, first)
	for _, s := range rest {
		args = append(args, s)
	}
	return args
}
