package telegram

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func fmtCompact(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	abs := math.Abs(v)
	switch {
	case abs >= 1e8:
		return fmt.Sprintf("%.2f亿", v/1e8)
	case abs >= 1e4:
		return fmt.Sprintf("%.2f万", v/1e4)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func fmtPct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	return fmt.Sprintf("%.3f%%", v)
}

func fmtSignedPct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	if v >= 0 {
		return fmt.Sprintf("+%.2f%%", v)
	}
	return fmt.Sprintf("%.2f%%", v)
}

func fmtBigUSD(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	abs := math.Abs(v)
	switch {
	case abs >= 1e12:
		return fmt.Sprintf("%.2f万亿", v/1e12)
	case abs >= 1e8:
		return fmt.Sprintf("%.2f亿", v/1e8)
	case abs >= 1e4:
		return fmt.Sprintf("%.2f万", v/1e4)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func fmtFactor(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2fx", v)
}

func fmtTs(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return "-"
	}
	t := time.UnixMilli(ms).In(loc)
	return t.Format("01-02 15:04")
}

func fmtHHMM(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).In(loc).Format("15:04")
}

func trendLabel(score float64) string {
	switch {
	case score >= 0.6:
		return "强上升"
	case score >= 0.2:
		return "上升"
	case score > -0.2:
		return "震荡"
	case score > -0.6:
		return "下降"
	default:
		return "强下降"
	}
}

func normalizeSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "/")
	if !strings.HasSuffix(s, "USDT") {
		s += "USDT"
	}
	return s
}

func eventTypeLabel(et string) string {
	labels := map[string]string{
		"breakout_up":     "突破阻力",
		"breakout_down":   "跌破支撑",
		"volume_spike":    "量能异常",
		"amplitude_spike": "振幅异常",
		"climax_reversal": "高潮反转",
	}
	if l, ok := labels[et]; ok {
		return l
	}
	return et
}

func eventSeverityScore(et string, details map[string]interface{}) float64 {
	base := 40.0
	switch et {
	case "breakout_up", "breakout_down":
		base = 60
		if touches, ok := detailFloat(details, "touches"); ok && touches >= 5 {
			base += 10
		}
		if vf, ok := detailFloat(details, "volumeFactor"); ok && vf >= 2 {
			base += 10
		}
	case "volume_spike":
		if vf, ok := detailFloat(details, "volumeFactor"); ok {
			base += math.Min(30, vf*5)
		}
	case "amplitude_spike":
		if af, ok := detailFloat(details, "amplitudeFactor"); ok {
			base += math.Min(20, af*5)
		}
	case "climax_reversal":
		base = 70
	}
	return math.Min(100, base)
}

func detailFloat(details map[string]interface{}, key string) (float64, bool) {
	if details == nil {
		return 0, false
	}
	raw, ok := details[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case string:
		var out float64
		if _, err := fmt.Sscanf(v, "%f", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

func eventLevel(score float64) string {
	switch {
	case score >= 80:
		return "critical"
	case score >= 55:
		return "warning"
	default:
		return "info"
	}
}
