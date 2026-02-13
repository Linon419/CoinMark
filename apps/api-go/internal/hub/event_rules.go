package hub

import "strings"

func toHubEventType(eventType string) string {
	et := strings.TrimSpace(eventType)
	if et == "" {
		return "ANOMALY_UNKNOWN"
	}
	return "ANOMALY_" + strings.ToUpper(et)
}

func toHubEventLevel(eventType string) string {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "whale_wall_far":
		return "warning"
	case "signal_lab_climax_long", "signal_lab_climax_short":
		return "critical"
	case "breakout_up", "breakout_down":
		return "warning"
	default:
		return "info"
	}
}
