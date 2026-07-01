package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"coinmark/api-go/internal/model"
)

type BollPumpOIGrowthProvider interface {
	GetOpenInterestHist(ctx context.Context, symbol, period string, limit int) ([]map[string]interface{}, error)
}

type bollPumpOIGrowthScore struct {
	Available bool
	GrowthPct float64
	Bonus     float64
	Period    string
	Periods   int
	PrevValue float64
	CurrValue float64
	Reason    string
}

type bollPumpOIPoint struct {
	ts    int64
	value float64
}

func bollPumpOIGrowthScoreFromHist(hist []map[string]interface{}, cfg BollPumpConfig, nowMs int64) bollPumpOIGrowthScore {
	cfg = NormalizeBollPumpConfig(cfg)
	if !cfg.OIGrowthScoreEnabled || cfg.OIGrowthMaxBonus <= 0 {
		return bollPumpOIGrowthScore{}
	}
	bucketMs := bollPumpOIGrowthBucketMs(cfg.OIGrowthPeriod)
	if bucketMs <= 0 || nowMs <= 0 {
		return bollPumpOIGrowthScore{}
	}
	cutoff := (nowMs / bucketMs) * bucketMs
	points := make([]bollPumpOIPoint, 0, len(hist))
	for _, row := range hist {
		ts := bollPumpMapInt64(row["timestamp"])
		if ts <= 0 || ts >= cutoff {
			continue
		}
		value := bollPumpMapFloat(row["sumOpenInterestValue"])
		if value <= 0 {
			value = bollPumpMapFloat(row["openInterest"])
		}
		if value <= 0 {
			value = bollPumpMapFloat(row["sumOpenInterest"])
		}
		if value > 0 {
			points = append(points, bollPumpOIPoint{ts: ts, value: value})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ts < points[j].ts })
	periods := cfg.OIGrowthPeriods
	if periods < 1 {
		periods = 1
	}
	if len(points) < periods+1 {
		return bollPumpOIGrowthScore{}
	}
	curr := points[len(points)-1]
	prev := points[len(points)-1-periods]
	if prev.value <= 0 || curr.value <= 0 {
		return bollPumpOIGrowthScore{}
	}
	growth := curr.value/prev.value - 1
	span := cfg.OIGrowthFullPct - cfg.OIGrowthMinPct
	if span <= 0 {
		span = 0.001
	}
	normalized := (growth - cfg.OIGrowthMinPct) / span
	normalized = math.Max(0, math.Min(1, normalized))
	bonus := bollPumpScoreFloor(normalized * cfg.OIGrowthMaxBonus)
	return bollPumpOIGrowthScore{
		Available: true,
		GrowthPct: growth,
		Bonus:     bonus,
		Period:    cfg.OIGrowthPeriod,
		Periods:   periods,
		PrevValue: prev.value,
		CurrValue: curr.value,
		Reason:    fmt.Sprintf("OI growth %.2f%% over %dx%s bonus %.0f", growth*100, periods, cfg.OIGrowthPeriod, bonus),
	}
}

func bollPumpApplyOIGrowthScore(sig model.BollPumpSignal, score bollPumpOIGrowthScore) model.BollPumpSignal {
	if !score.Available {
		return sig
	}
	sig.Details = bollPumpMergeSignalDetails(sig.Details, map[string]interface{}{
		"oi_growth_pct":        score.GrowthPct,
		"oi_growth_bonus":      score.Bonus,
		"oi_growth_period":     score.Period,
		"oi_growth_periods":    score.Periods,
		"oi_growth_prev_value": score.PrevValue,
		"oi_growth_curr_value": score.CurrValue,
	})
	if score.Bonus <= 0 {
		return sig
	}
	sig.Score = bollPumpScoreFloor(sig.Score + score.Bonus)
	sig.PriorityScore = bollPumpScoreFloor(sig.PriorityScore + score.Bonus)
	if strings.TrimSpace(sig.Reason) == "" {
		sig.Reason = score.Reason
	} else {
		sig.Reason += ", " + score.Reason
	}
	return sig
}

func bollPumpMergeSignalDetails(base model.JSONB, patch map[string]interface{}) model.JSONB {
	out := map[string]interface{}{}
	if len(base) > 0 && strings.TrimSpace(string(base)) != "" && strings.TrimSpace(string(base)) != "null" {
		_ = json.Unmarshal(base, &out)
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	for k, v := range patch {
		out[k] = v
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return base
	}
	return model.JSONB(raw)
}

func bollPumpOIGrowthBucketMs(period string) int64 {
	switch normalizeBollPumpOIGrowthPeriod(period) {
	case "15m":
		return 15 * 60 * 1000
	case "30m":
		return 30 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	default:
		return 0
	}
}

func bollPumpOIGrowthHistLimit(cfg BollPumpConfig) int {
	cfg = NormalizeBollPumpConfig(cfg)
	limit := cfg.OIGrowthPeriods + 6
	if limit < 8 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}
	return limit
}

func bollPumpMapInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func bollPumpMapFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}
