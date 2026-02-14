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
	Direction, SignalState        string
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
	if ch == nil || bn == nil || store == nil {
		return nil
	}

	const (
		b1mMs = int64(60 * 1000)
		b4hMs = int64(4 * 60 * 60 * 1000)
		d1Ms  = int64(24 * 60 * 60 * 1000)

		// Robustness guards:
		// - minute coverage avoids distorted percentile baselines from sparse data
		// - 4h coverage avoids persistence ratios over missing windows
		minMinuteCoverage7d = 0.72
		minWindowCoverage7d = 0.75

		// dual threshold: rolling percentile + absolute floor
		bigAbsFloorNotional = 20000.0

		// hysteresis to reduce edge flicker around confirm/fading border
		confirmEntryRatio = 0.60
		confirmExitRatio  = 0.55
		minFadingDrop     = 0.08
	)

	nowMs := time.Now().UnixMilli()
	lastClosed1mStart := (nowMs/b1mMs)*b1mMs - b1mMs
	lastClosed4hStart := (lastClosed1mStart/b4hMs)*b4hMs - b4hMs
	if lastClosed1mStart <= 0 || lastClosed4hStart <= 0 {
		return nil
	}
	// Keep enough history for:
	// - 7d percentile baselines
	// - recent 30 and previous 30 of 4h windows
	fullStartMs := lastClosed1mStart - 20*d1Ms
	bucketStartMs := (nowMs / b1mMs) * b1mMs

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
		ranked = append(ranked, symVol{sym: sym, qv: qv})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].qv > ranked[j].qv })
	if topN < 20 {
		topN = 20
	}
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	symbols := make([]string, 0, len(ranked))
	for _, r := range ranked {
		symbols = append(symbols, r.sym)
	}
	symbols = binance.FilterExcludedSymbols(symbols)
	if len(symbols) == 0 {
		return nil
	}

	trRows, err := ch.QueryTradeBuckets(ctx, market, "", symbols, "1m", fullStartMs, lastClosed1mStart, "asc", 0)
	if err != nil {
		return err
	}
	if len(trRows) == 0 {
		return nil
	}

	type point4h struct {
		StartMs  int64
		Delta4h  float64
		BigDelta float64
	}
	type agg4h struct {
		Delta4h  float64
		BigDelta float64
	}
	type symData struct {
		rows []model.CHTradeRow
	}
	bySym := make(map[string]*symData, len(symbols))
	for _, r := range trRows {
		sym := r.Symbol
		if sym == "" || binance.IsExcludedSymbol(sym) {
			continue
		}
		if bySym[sym] == nil {
			bySym[sym] = &symData{}
		}
		bySym[sym].rows = append(bySym[sym].rows, r)
	}

	values := make([]absValue, 0, len(symbols))
	for _, sym := range symbols {
		data := bySym[sym]
		if data == nil || len(data.rows) < 1200 {
			continue
		}
		rows := data.rows

		recentStart1m := lastClosed1mStart - 7*d1Ms
		recentRows := 0
		for _, r := range rows {
			if r.BucketStartMs >= recentStart1m && r.BucketStartMs <= lastClosed1mStart {
				recentRows++
			}
		}
		expRows := int((lastClosed1mStart-recentStart1m)/b1mMs) + 1
		if expRows <= 0 || float64(recentRows)/float64(expRows) < minMinuteCoverage7d {
			continue
		}

		last7dAbsDelta1m := make([]float64, 0, 20000)
		winAgg := make(map[int64]*agg4h, 256)

		for _, r := range rows {
			ts := r.BucketStartMs
			if ts > lastClosed1mStart {
				continue
			}
			delta := r.TakerBuyNotional - r.TakerSellNotional
			wStart := (ts / b4hMs) * b4hMs
			if winAgg[wStart] == nil {
				winAgg[wStart] = &agg4h{}
			}
			winAgg[wStart].Delta4h += delta
			if ts >= lastClosed1mStart-7*d1Ms {
				last7dAbsDelta1m = append(last7dAbsDelta1m, math.Abs(delta))
			}
		}
		if len(last7dAbsDelta1m) < 500 {
			continue
		}
		p95 := percentileValue(last7dAbsDelta1m, 95.0)
		p85 := percentileValue(last7dAbsDelta1m, 85.0)
		bigThreshold := math.Max(p95, math.Max(bigAbsFloorNotional, p85*1.6))
		if bigThreshold <= 0 {
			continue
		}

		for _, r := range rows {
			ts := r.BucketStartMs
			if ts > lastClosed1mStart {
				continue
			}
			delta := r.TakerBuyNotional - r.TakerSellNotional
			if math.Abs(delta) < bigThreshold {
				continue
			}
			wStart := (ts / b4hMs) * b4hMs
			if winAgg[wStart] == nil {
				continue
			}
			winAgg[wStart].BigDelta += delta
		}

		starts := make([]int64, 0, len(winAgg))
		for s := range winAgg {
			if s <= lastClosed4hStart {
				starts = append(starts, s)
			}
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
		if len(starts) < 80 {
			continue
		}

		points := make([]point4h, 0, len(starts))
		latestIdx := -1
		for i, s := range starts {
			a := winAgg[s]
			points = append(points, point4h{StartMs: s, Delta4h: a.Delta4h, BigDelta: a.BigDelta})
			if s == lastClosed4hStart {
				latestIdx = i
			}
		}
		if latestIdx < 0 {
			continue
		}

		histStart := lastClosed4hStart - 7*d1Ms
		histDelta := make([]float64, 0, 64)
		histBig := make([]float64, 0, 64)
		hist4hObs := 0
		for _, p := range points {
			if p.StartMs >= histStart && p.StartMs < lastClosed4hStart {
				histDelta = append(histDelta, p.Delta4h)
				histBig = append(histBig, p.BigDelta)
				hist4hObs++
			}
		}
		if len(histDelta) < 20 {
			continue
		}
		exp4h := int((lastClosed4hStart - histStart) / b4hMs)
		if exp4h <= 0 || float64(hist4hObs)/float64(exp4h) < minWindowCoverage7d {
			continue
		}
		p80DeltaBuy := percentileValue(histDelta, 80.0)
		p80DeltaSell := percentileValue(negSeries(histDelta), 80.0)
		p80BigDeltaBuy := percentileValue(histBig, 80.0)
		p80BigDeltaSell := percentileValue(negSeries(histBig), 80.0)
		deltaThresholdBuy := math.Max(0, p80DeltaBuy)
		deltaThresholdSell := math.Max(0, p80DeltaSell)
		bigDeltaThresholdBuy := math.Max(0, p80BigDeltaBuy)
		bigDeltaThresholdSell := math.Max(0, p80BigDeltaSell)

		abnormalBuyFlags := make([]bool, len(points))
		abnormalSellFlags := make([]bool, len(points))
		for i := range points {
			abnormalBuyFlags[i] = points[i].Delta4h > deltaThresholdBuy
			abnormalSellFlags[i] = (-points[i].Delta4h) > deltaThresholdSell
		}

		priceChange5d := 0.0
		hasPrice5d := false
		latestClose, okLatestClose := closeAtOrBefore(rows, lastClosed1mStart)
		close5dAgo, okClose5d := closeAtOrBefore(rows, lastClosed1mStart-5*d1Ms)
		if okLatestClose && okClose5d && close5dAgo > 0 {
			priceChange5d = latestClose/close5dAgo - 1.0
			hasPrice5d = true
		}

		buildSignal := func(side string, abnormalFlags []bool, deltaNow, deltaThreshold, bigNow, bigThreshold float64) absValue {
			l1 := deltaNow > deltaThreshold
			recentRatio, recentOK := ratioRange(abnormalFlags, latestIdx-29, latestIdx)
			prevRatio, prevOK := ratioRange(abnormalFlags, latestIdx-59, latestIdx-30)
			l2 := recentOK && recentRatio > confirmEntryRatio
			// hysteresis: keep CONFIRM when ratio only small pullback
			if !l2 && l1 && recentOK && prevOK && prevRatio > confirmEntryRatio && recentRatio >= confirmExitRatio {
				l2 = true
			}
			l3 := l2 && bigNow > bigThreshold && bigNow > 0

			cumSlope := 0.0
			if recentOK {
				winDeltas := make([]float64, 0, 30)
				for i := latestIdx - 29; i <= latestIdx; i++ {
					if i < 0 || i >= len(points) {
						continue
					}
					d := points[i].Delta4h
					if side == "SHORT_BIAS" {
						d = -d
					}
					winDeltas = append(winDeltas, d)
				}
				if len(winDeltas) >= 10 {
					cum := make([]float64, len(winDeltas))
					s := 0.0
					for i, v := range winDeltas {
						s += v
						cum[i] = s
					}
					cumSlope = linearSlope(cum)
				}
			}

			l3Plus := l3 && hasPrice5d && cumSlope > 0 && math.Abs(priceChange5d) <= 0.10
			fading := prevOK && recentOK && prevRatio > confirmEntryRatio &&
				recentRatio <= confirmExitRatio &&
				(prevRatio-recentRatio) >= minFadingDrop &&
				!l2 && !l3

			state := "NONE"
			score := 0.0
			switch {
			case l3Plus:
				state, score = "STRONG", 95
			case l3:
				state, score = "STRONG", 86
			case l2:
				state, score = "CONFIRM", 72
			case fading:
				state, score = "FADING", 52
			case l1:
				state, score = "WATCH", 58
			}

			reasons := make([]string, 0, 9)
			if l1 {
				reasons = append(reasons, "L1_DELTA4H_ABNORMAL")
			}
			if l2 {
				reasons = append(reasons, "L2_PERSISTENCE_RATIO_GT_60")
			}
			if l3 {
				reasons = append(reasons, "L3_BIG_DELTA_CONFIRM")
			}
			if l3Plus {
				reasons = append(reasons, "L3_PLUS_PRICE_DELTA_DIVERGENCE")
			}
			if fading {
				reasons = append(reasons, "FADING_RECENT_RATIO_DROPPED")
			}
			if len(reasons) == 0 {
				reasons = append(reasons, "NO_TRIGGER")
			}
			if side == "SHORT_BIAS" {
				for i := range reasons {
					reasons[i] = "SHORT_" + reasons[i]
				}
			}

			windows, _ := json.Marshal(map[string]map[string]interface{}{
				"4h": {
					"passed":     l1,
					"score":      round4(clamp((deltaNow/math.Max(1e-9, deltaThreshold))*100.0, 0, 100)),
					"delta4h":    round4(deltaNow),
					"p80":        round4(deltaThreshold),
					"coverage7d": round4(float64(hist4hObs) / math.Max(1.0, float64(exp4h))),
				},
				"1d": {
					"passed":               l2,
					"score":                round4(clamp(recentRatio*100.0, 0, 100)),
					"persistenceRatio":     round4(recentRatio),
					"persistenceRatioPrev": round4(prevRatio),
				},
				"3d": {
					"passed":           l3 || l3Plus,
					"score":            round4(clamp((bigNow/math.Max(1e-9, bigThreshold))*100.0, 0, 100)),
					"bigDelta4h":       round4(bigNow),
					"bigDeltaP80":      round4(bigThreshold),
					"cumDelta5dSlope":  round4(cumSlope),
					"priceChange5dPct": round4(priceChange5d * 100),
					"divergencePassed": l3Plus,
				},
			})
			reasonsJSON, _ := json.Marshal(reasons)

			netFlowStrength := points[latestIdx].Delta4h
			if side == "SHORT_BIAS" {
				netFlowStrength = -math.Abs(netFlowStrength)
			}
			var impactPerNotional *float64
			if hasPrice5d {
				v := math.Abs(priceChange5d) / math.Max(1e-9, math.Abs(points[latestIdx].Delta4h))
				impactPerNotional = &v
			}

			return absValue{
				Market:          market,
				Symbol:          sym,
				BucketStartMs:   bucketStartMs,
				Direction:       side,
				SignalState:     state,
				Score:           score,
				NetFlowStrength: &netFlowStrength,
				ImpactPerNot:    impactPerNotional,
				W4h:             l1,
				W1d:             l2,
				W3d:             l3 || l3Plus,
				Windows:         windows,
				Reasons:         reasonsJSON,
			}
		}

		longSig := buildSignal(
			"LONG_BIAS",
			abnormalBuyFlags,
			points[latestIdx].Delta4h,
			deltaThresholdBuy,
			points[latestIdx].BigDelta,
			bigDeltaThresholdBuy,
		)
		shortSig := buildSignal(
			"SHORT_BIAS",
			abnormalSellFlags,
			-points[latestIdx].Delta4h,
			deltaThresholdSell,
			-points[latestIdx].BigDelta,
			bigDeltaThresholdSell,
		)

		values = append(values, longSig, shortSig)
	}

	if len(values) == 0 {
		return nil
	}
	return upsertAbsorptionSnapshots(ctx, store, values)
}

func percentileValue(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			s = append(s, v)
		}
	}
	if len(s) == 0 {
		return 0
	}
	sort.Float64s(s)
	if p <= 0 {
		return s[0]
	}
	if p >= 100 {
		return s[len(s)-1]
	}
	pos := (p / 100.0) * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return s[lo]
	}
	frac := pos - float64(lo)
	return s[lo] + (s[hi]-s[lo])*frac
}

func ratioRange(flags []bool, start, end int) (float64, bool) {
	if len(flags) == 0 {
		return 0, false
	}
	if start < 0 || end < 0 || start >= len(flags) || end >= len(flags) || start > end {
		return 0, false
	}
	total := end - start + 1
	if total <= 0 {
		return 0, false
	}
	hit := 0
	for i := start; i <= end; i++ {
		if flags[i] {
			hit++
		}
	}
	return float64(hit) / float64(total), true
}

func closeAtOrBefore(rows []model.CHTradeRow, ts int64) (float64, bool) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].BucketStartMs <= ts && rows[i].ClosePrice != nil && *rows[i].ClosePrice > 0 {
			return *rows[i].ClosePrice, true
		}
	}
	return 0, false
}

func linearSlope(vals []float64) float64 {
	n := len(vals)
	if n < 2 {
		return 0
	}
	meanX := float64(n-1) / 2.0
	meanY := 0.0
	for _, v := range vals {
		meanY += v
	}
	meanY /= float64(n)
	num, den := 0.0, 0.0
	for i, v := range vals {
		x := float64(i)
		dx := x - meanX
		dy := v - meanY
		num += dx * dy
		den += dx * dx
	}
	if den <= 0 {
		return 0
	}
	return num / den
}

func negSeries(vals []float64) []float64 {
	out := make([]float64, 0, len(vals))
	for _, v := range vals {
		out = append(out, -v)
	}
	return out
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
