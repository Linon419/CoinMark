package service

import (
	"context"
	"encoding/json"
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
// Window evaluation
// ---------------------------------------------------------------------------

type windowResult struct {
	Passed                  bool     `json:"passed"`
	BuyPersistenceRatio     float64  `json:"buyPersistenceRatio"`
	SellPersistenceRatio    float64  `json:"sellPersistenceRatio"`
	BuyPersistenceThreshold float64  `json:"buyPersistenceThreshold"`
	NetRetAbsPct            float64  `json:"netRetAbsPct"`
	NetRetAbsThresholdPct   float64  `json:"netRetAbsThresholdPct"`
	SpreadThinRank          *float64 `json:"spreadThinRank"`
	ThinThreshold           float64  `json:"thinThreshold"`
	ReplenishAvg            *float64 `json:"replenishAvg"`
	ReplenishThreshold      float64  `json:"replenishThreshold"`
	Score                   float64  `json:"score,omitempty"`
}

type absRow struct {
	Ts             int64
	NetBuyNotional float64
	AggrBuyRatio   *float64
	SpreadBps      *float64
	ReplenishScore *float64
	ClosePrice     *float64
	Ret1m          float64
}

type absValue struct {
	Market, Symbol                string
	BucketStartMs                 int64
	Direction, SignalState         string
	Score                         float64
	NetFlowStrength, ImpactPerNot *float64
	W4h, W1d, W3d                 bool
	Windows                       json.RawMessage
	Reasons                       json.RawMessage
}

func windowEval(rows []absRow, spreadValues []float64, minutes int, side string, persistThresh, impactThreshPct, thinThresh, replenishThresh float64) windowResult {
	win := rows
	if len(rows) > minutes {
		win = rows[len(rows)-minutes:]
	}
	if len(win) == 0 {
		return windowResult{BuyPersistenceThreshold: persistThresh, NetRetAbsThresholdPct: impactThreshPct, ThinThreshold: thinThresh, ReplenishThreshold: replenishThresh}
	}

	buyFlags, sellFlags := 0, 0
	for _, r := range win {
		if r.AggrBuyRatio != nil {
			ratio := *r.AggrBuyRatio
			if ratio >= 0.58 && r.NetBuyNotional > 0 {
				buyFlags++
			}
			if ratio <= 0.42 && r.NetBuyNotional < 0 {
				sellFlags++
			}
		}
	}
	buyPR := float64(buyFlags) / float64(len(win))
	sellPR := float64(sellFlags) / float64(len(win))
	pr := buyPR
	if side == "SHORT_BIAS" {
		pr = sellPR
	}

	retSum := 0.0
	for _, r := range win {
		retSum += r.Ret1m
	}
	netRetAbsPct := math.Abs(retSum) * 100.0

	var lastSpread *float64
	for i := len(win) - 1; i >= 0; i-- {
		if win[i].SpreadBps != nil {
			lastSpread = win[i].SpreadBps
			break
		}
	}
	var spreadRank *float64
	if lastSpread != nil {
		r := percentileRank(spreadValues, *lastSpread)
		spreadRank = &r
	}

	var replenishVals []float64
	for _, r := range win {
		if r.ReplenishScore != nil {
			replenishVals = append(replenishVals, *r.ReplenishScore)
		}
	}
	var replenishAvg *float64
	if len(replenishVals) > 0 {
		avg := mean(replenishVals)
		replenishAvg = &avg
	}

	thinOk := spreadRank != nil && *spreadRank >= thinThresh
	repOk := replenishAvg != nil && *replenishAvg >= replenishThresh
	passed := pr >= persistThresh && netRetAbsPct <= impactThreshPct && (thinOk || repOk)

	return windowResult{
		Passed: passed, BuyPersistenceRatio: round4(buyPR), SellPersistenceRatio: round4(sellPR),
		BuyPersistenceThreshold: persistThresh, NetRetAbsPct: round4(netRetAbsPct),
		NetRetAbsThresholdPct: impactThreshPct, SpreadThinRank: roundPtr4(spreadRank),
		ThinThreshold: thinThresh, ReplenishAvg: roundPtr2(replenishAvg), ReplenishThreshold: replenishThresh,
	}
}

func scoreWindow(w windowResult, side string) float64 {
	pr := w.BuyPersistenceRatio
	if side == "SHORT_BIAS" {
		pr = w.SellPersistenceRatio
	}
	pt := math.Max(1e-9, w.BuyPersistenceThreshold)
	it := math.Max(1e-9, w.NetRetAbsThresholdPct)
	tt := math.Max(1e-9, w.ThinThreshold)
	rt := math.Max(1e-9, w.ReplenishThreshold)

	s1 := clamp((pr/pt)*40.0, 0, 40)
	s2 := clamp(((it-w.NetRetAbsPct)/it)*35.0, 0, 35)

	s3a := 0.0
	if w.SpreadThinRank != nil {
		s3a = clamp((*w.SpreadThinRank/tt)*25.0, 0, 25)
	}
	s3b := 0.0
	if w.ReplenishAvg != nil {
		s3b = clamp((*w.ReplenishAvg/rt)*25.0, 0, 25)
	}
	s3 := math.Max(s3a, s3b)
	return math.Round(clamp(s1+s2+s3, 0, 100)*10) / 10
}

// ---------------------------------------------------------------------------
// RefreshAbsorptionSignalSnapshots
// ---------------------------------------------------------------------------

func RefreshAbsorptionSignalSnapshots(ctx context.Context, ch *chrepo.Client, bn *binance.Client, store *sqlite.Store, market string, topN int) error {
	nowMs := time.Now().UnixMilli()
	fullStartMs := nowMs - 3*24*60*60*1000
	bucketStartMs := (nowMs / 60000) * 60000

	tickers, err := bn.GetTicker24hAll(ctx, market)
	if err != nil {
		return err
	}
	type symVol struct {
		sym string
		qv  float64
	}
	var ranked []symVol
	for _, row := range tickers {
		sym, _ := row["symbol"].(string)
		if sym == "" || !endsWithUSDT(sym) {
			continue
		}
		qv := toFloat64(row["quoteVolume"])
		ranked = append(ranked, symVol{sym, qv})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].qv > ranked[j].qv })
	if topN < 20 {
		topN = 20
	}
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	var symbols []string
	for _, r := range ranked {
		symbols = append(symbols, r.sym)
	}
	symbols = binance.FilterExcludedSymbols(symbols)
	if len(symbols) == 0 {
		return nil
	}

	obRows, err := ch.QueryOrderbookFeatures(ctx, market, "", symbols, "1m", fullStartMs, 0, "asc")
	if err != nil {
		return err
	}
	trRows, err := ch.QueryTradeBuckets(ctx, market, "", symbols, "1m", fullStartMs, 0, "asc", 0)
	if err != nil {
		return err
	}

	obBySym := make(map[string]map[int64]model.CHOBFeatureRow)
	for _, r := range obRows {
		m, ok := obBySym[r.Symbol]
		if !ok {
			m = make(map[int64]model.CHOBFeatureRow)
			obBySym[r.Symbol] = m
		}
		m[r.BucketStartMs] = r
	}
	trBySym := make(map[string]map[int64]model.CHTradeRow)
	for _, r := range trRows {
		m, ok := trBySym[r.Symbol]
		if !ok {
			m = make(map[int64]model.CHTradeRow)
			trBySym[r.Symbol] = m
		}
		m[r.BucketStartMs] = r
	}

	var values []absValue
	for _, sym := range symbols {
		obMap := obBySym[sym]
		trMap := trBySym[sym]
		tsSet := make(map[int64]struct{})
		for ts := range obMap {
			tsSet[ts] = struct{}{}
		}
		for ts := range trMap {
			tsSet[ts] = struct{}{}
		}
		var timeline []int64
		for ts := range tsSet {
			timeline = append(timeline, ts)
		}
		sort.Slice(timeline, func(i, j int) bool { return timeline[i] < timeline[j] })
		if len(timeline) < 240 {
			continue
		}

		var rows []absRow
		for _, ts := range timeline {
			ob, obOk := obMap[ts]
			tr, trOk := trMap[ts]

			buy, sell := 0.0, 0.0
			if obOk {
				buy = ob.TakerBuyNotional
				sell = ob.TakerSellNotional
			} else if trOk {
				buy = tr.TakerBuyNotional
				sell = tr.TakerSellNotional
			}
			net := buy - sell
			denom := buy + sell
			var aggrBuyRatio *float64
			if denom > 0 {
				v := buy / denom
				aggrBuyRatio = &v
			}

			var spreadBps *float64
			if obOk && ob.SampleCount > 0 {
				v := ob.SpreadBpsSum / float64(ob.SampleCount)
				spreadBps = &v
			}

			var replenishScore *float64
			if obOk {
				dep := ob.DepletionEvents
				rep := ob.ReplenishmentEvents
				if dep <= 0 {
					v := 50.0
					replenishScore = &v
				} else {
					v := clamp(float64(rep)/float64(dep)*100.0, 0, 100)
					replenishScore = &v
				}
			}

			var closePrice *float64
			if trOk && tr.ClosePrice != nil {
				closePrice = tr.ClosePrice
			}
			rows = append(rows, absRow{Ts: ts, NetBuyNotional: net, AggrBuyRatio: aggrBuyRatio, SpreadBps: spreadBps, ReplenishScore: replenishScore, ClosePrice: closePrice})
		}

		for i := range rows {
			if i == 0 {
				rows[i].Ret1m = 0
				continue
			}
			if rows[i].ClosePrice != nil && rows[i-1].ClosePrice != nil && *rows[i-1].ClosePrice > 0 {
				rows[i].Ret1m = *rows[i].ClosePrice / *rows[i-1].ClosePrice - 1.0
			}
		}

		var spreadValues []float64
		for _, r := range rows {
			if r.SpreadBps != nil {
				spreadValues = append(spreadValues, *r.SpreadBps)
			}
		}

		for _, side := range []string{"LONG_BIAS", "SHORT_BIAS"} {
			w4h := windowEval(rows, spreadValues, 240, side, 0.60, 0.35, 0.60, 55.0)
			w1d := windowEval(rows, spreadValues, 1440, side, 0.55, 0.80, 0.60, 55.0)
			w3d := windowEval(rows, spreadValues, 4320, side, 0.50, 1.60, 0.60, 55.0)
			s4h := scoreWindow(w4h, side)
			s1d := scoreWindow(w1d, side)
			s3d := scoreWindow(w3d, side)

			state := "NONE"
			if w3d.Passed && s3d >= 78 {
				state = "STRONG"
			} else if w1d.Passed && s1d >= 65 {
				state = "CONFIRM"
			} else if w4h.Passed && s4h >= 55 {
				state = "WATCH"
			}

			score := 0.0
			var reasons []string
			contReason := ""

			if state == "NONE" {
				n4h := 240
				if len(rows) < n4h {
					n4h = len(rows)
				}
				n12h := 720
				if len(rows) < n12h {
					n12h = len(rows)
				}
				recent4h := rows[len(rows)-n4h:]
				recent12h := rows[len(rows)-n12h:]
				flow4h := meanNetBuy(recent4h)
				flow12h := meanNetBuy(recent12h)
				p1d := w1d.BuyPersistenceRatio
				if side == "SHORT_BIAS" {
					p1d = w1d.SellPersistenceRatio
				}
				contOk := false
				if side == "LONG_BIAS" {
					contOk = flow4h > 0 && flow12h > 0
				} else {
					contOk = flow4h < 0 && flow12h < 0
				}
				contOk = contOk && p1d >= 0.50 && w1d.NetRetAbsPct <= 1.20
				if contOk && math.Max(s4h, s1d) >= 52 {
					state = "WATCH"
					score = math.Max(52, math.Min(68, math.Max(s4h, s1d)))
					contReason = "FLOW_CONTINUATION_12H"
				}
			}

			switch state {
			case "WATCH":
				score = math.Max(score, s4h)
			case "CONFIRM":
				score = math.Max(s4h, s1d)
			case "STRONG":
				score = math.Max(math.Max(s4h, s1d), s3d)
			}

			var impactVals []float64
			tail := rows
			if len(tail) > 20 {
				tail = tail[len(tail)-20:]
			}
			for _, r := range tail {
				if math.Abs(r.NetBuyNotional) > 1e-9 {
					impactVals = append(impactVals, math.Abs(r.Ret1m)/math.Abs(r.NetBuyNotional))
				}
			}
			var impactAvg, netStrength *float64
			if len(impactVals) > 0 {
				v := mean(impactVals)
				impactAvg = &v
			}
			ns := meanNetBuy(tail)
			netStrength = &ns

			if w4h.Passed {
				reasons = append(reasons, "4h通过")
			}
			if w1d.Passed {
				reasons = append(reasons, "1d通过")
			}
			if w3d.Passed {
				reasons = append(reasons, "3d通过")
			}
			if len(reasons) == 0 {
				reasons = append(reasons, "未触发")
			}
			if contReason != "" {
				reasons = append(reasons, contReason)
			}

			w4h.Score = s4h
			w1d.Score = s1d
			w3d.Score = s3d
			wj, _ := json.Marshal(map[string]windowResult{"4h": w4h, "1d": w1d, "3d": w3d})
			rj, _ := json.Marshal(reasons)

			values = append(values, absValue{
				Market: market, Symbol: sym, BucketStartMs: bucketStartMs,
				Direction: side, SignalState: state, Score: score,
				NetFlowStrength: netStrength, ImpactPerNot: impactAvg,
				W4h: w4h.Passed, W1d: w1d.Passed, W3d: w3d.Passed,
				Windows: wj, Reasons: rj,
			})
		}
	}

	if len(values) == 0 {
		return nil
	}
	return upsertAbsorptionSnapshots(ctx, store, values)
}

func upsertAbsorptionSnapshots(ctx context.Context, store *sqlite.Store, values []absValue) error {
	sql := `INSERT INTO absorption_signal_snapshots
(market, symbol, bucket_start_ms, direction, signal_state, score, net_flow_strength, impact_per_notional,
 window_4h_passed, window_1d_passed, window_3d_passed, windows, reasons)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(market, symbol, bucket_start_ms, direction) DO UPDATE SET
  signal_state = excluded.signal_state, score = excluded.score,
  net_flow_strength = excluded.net_flow_strength, impact_per_notional = excluded.impact_per_notional,
  window_4h_passed = excluded.window_4h_passed, window_1d_passed = excluded.window_1d_passed,
  window_3d_passed = excluded.window_3d_passed, windows = excluded.windows, reasons = excluded.reasons`

	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, v := range values {
			if _, err := tx.Exec(sql,
				v.Market, v.Symbol, v.BucketStartMs, v.Direction, v.SignalState, v.Score,
				v.NetFlowStrength, v.ImpactPerNot,
				v.W4h, v.W1d, v.W3d, string(v.Windows), string(v.Reasons),
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func CleanupAbsorptionSnapshots(ctx context.Context, store *sqlite.Store, retentionHours int) (int, error) {
	if retentionHours < 1 {
		retentionHours = 1
	}
	cutoffMs := time.Now().UnixMilli() - int64(retentionHours)*60*60*1000
	var count int
	err := store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		res, err := tx.Exec("DELETE FROM absorption_signal_snapshots WHERE bucket_start_ms < ?", cutoffMs)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		count = int(n)
		return nil
	})
	return count, err
}

func ListLatestAbsorptionSignals(ctx context.Context, store *sqlite.Store, market string, onlySignals bool, limit, lookbackMinutes int, direction string) ([]model.AbsorptionSignalSnapshot, error) {
	nowMs := time.Now().UnixMilli()
	if lookbackMinutes < 15 {
		lookbackMinutes = 15
	}
	lookbackStartMs := nowMs - int64(lookbackMinutes)*60*1000

	args := []interface{}{market}
	where := "market = ?"
	normDir := ""
	if direction == "long" {
		normDir = "LONG_BIAS"
	} else if direction == "short" {
		normDir = "SHORT_BIAS"
	}
	if normDir != "" {
		where += " AND direction = ?"
		args = append(args, normDir)
	}
	if onlySignals {
		where += " AND signal_state != 'NONE' AND bucket_start_ms >= ?"
		args = append(args, lookbackStartMs)
	}

	sql := `SELECT a.* FROM absorption_signal_snapshots a
INNER JOIN (
  SELECT symbol, direction, MAX(bucket_start_ms) AS bucket_start_ms
  FROM absorption_signal_snapshots WHERE ` + where + `
  GROUP BY symbol, direction
) sub ON a.symbol = sub.symbol AND a.direction = sub.direction AND a.bucket_start_ms = sub.bucket_start_ms
WHERE a.market = ?`
	args = append(args, market)
	if normDir != "" {
		sql += " AND a.direction = ?"
		args = append(args, normDir)
	}
	sql += " ORDER BY a.bucket_start_ms DESC, a.score DESC"
	if limit > 0 {
		sql += " LIMIT ?"
		args = append(args, limit)
	}

	var rows []model.AbsorptionSignalSnapshot
	if err := store.SelectContext(ctx, &rows, sql, args...); err != nil {
		return nil, err
	}
	var out []model.AbsorptionSignalSnapshot
	for _, r := range rows {
		if !binance.IsExcludedSymbol(r.Symbol) {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func roundPtr4(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := round4(*p)
	return &v
}

func roundPtr2(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := math.Round(*p*100) / 100
	return &v
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range vals {
		s += v
	}
	return s / float64(len(vals))
}

func percentileRank(sorted []float64, value float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	s := make([]float64, len(sorted))
	copy(s, sorted)
	sort.Float64s(s)
	le := 0
	for _, v := range s {
		if v <= value {
			le++
		} else {
			break
		}
	}
	return float64(le) / float64(len(s))
}

func meanNetBuy(rows []absRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	s := 0.0
	for _, r := range rows {
		s += r.NetBuyNotional
	}
	return s / float64(len(rows))
}

func endsWithUSDT(s string) bool {
	return len(s) > 4 && s[len(s)-4:] == "USDT"
}

func toFloat64(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case string:
		return 0
	default:
		return 0
	}
}
