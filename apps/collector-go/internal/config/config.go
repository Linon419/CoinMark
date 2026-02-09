package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BinanceWSBaseURL string
	BinanceRESTBase  string
	NATSURL          string
	NATSStreamRaw    string
	NATSSubjectTrade string
	NATSSubjectDepth string
	NATSClientName   string
	Symbols          []string
	SymbolLimit      int
	StreamsPerConn   int
	Market           string
	EnableDepth      bool
	DepthUpdateMs    int
	DepthLevel       int
	DepthSampleEvery int
	LogIntervalSec   int
}

func Load() (Config, error) {
	cfg := Config{
		BinanceWSBaseURL: getenv("COLLECTOR_BINANCE_WS_BASE_URL", "wss://fstream.binance.com/stream"),
		BinanceRESTBase:  getenv("COLLECTOR_BINANCE_REST_BASE", "https://fapi.binance.com"),
		NATSURL:          getenv("COLLECTOR_NATS_URL", "nats://nats:4222"),
		NATSStreamRaw:    getenv("COLLECTOR_NATS_STREAM_RAW", "COINMARK_RAW"),
		NATSSubjectTrade: getenv("COLLECTOR_NATS_SUBJECT_TRADE", "coinmark.raw.trade"),
		NATSSubjectDepth: getenv("COLLECTOR_NATS_SUBJECT_DEPTH", "coinmark.raw.depth"),
		NATSClientName:   getenv("COLLECTOR_NATS_CLIENT_NAME", "collector-go"),
		Symbols:          splitCSV(getenv("COLLECTOR_SYMBOLS", "")),
		SymbolLimit:      getenvInt("COLLECTOR_SYMBOL_LIMIT", 0),
		StreamsPerConn:   getenvInt("COLLECTOR_STREAMS_PER_CONN", 200),
		Market:           strings.ToLower(getenv("COLLECTOR_MARKET", "swap")),
		EnableDepth:      getenvBool("COLLECTOR_ENABLE_DEPTH", true),
		DepthUpdateMs:    getenvInt("COLLECTOR_DEPTH_UPDATE_MS", 100),
		DepthLevel:       getenvInt("COLLECTOR_DEPTH_LEVEL", 20),
		DepthSampleEvery: getenvInt("COLLECTOR_DEPTH_SAMPLE_EVERY", 2),
		LogIntervalSec:   getenvInt("COLLECTOR_LOG_INTERVAL_SEC", 15),
	}

	if cfg.Market != "spot" && cfg.Market != "swap" {
		return Config{}, fmt.Errorf("COLLECTOR_MARKET must be 'spot' or 'swap', got: %s", cfg.Market)
	}
	if cfg.NATSURL == "" {
		return Config{}, fmt.Errorf("COLLECTOR_NATS_URL is empty")
	}
	if cfg.NATSStreamRaw == "" {
		return Config{}, fmt.Errorf("COLLECTOR_NATS_STREAM_RAW is empty")
	}
	if cfg.NATSSubjectTrade == "" {
		return Config{}, fmt.Errorf("COLLECTOR_NATS_SUBJECT_TRADE is empty")
	}
	if cfg.EnableDepth && cfg.NATSSubjectDepth == "" {
		return Config{}, fmt.Errorf("COLLECTOR_NATS_SUBJECT_DEPTH is empty while COLLECTOR_ENABLE_DEPTH=true")
	}
	if cfg.NATSClientName == "" {
		cfg.NATSClientName = "collector-go"
	}
	if cfg.DepthUpdateMs < 100 {
		cfg.DepthUpdateMs = 100
	}
	if cfg.DepthLevel != 5 && cfg.DepthLevel != 10 && cfg.DepthLevel != 20 {
		cfg.DepthLevel = 20
	}
	if cfg.DepthSampleEvery < 1 {
		cfg.DepthSampleEvery = 1
	}
	if cfg.LogIntervalSec < 5 {
		cfg.LogIntervalSec = 5
	}
	if cfg.StreamsPerConn <= 0 {
		cfg.StreamsPerConn = 200
	}
	if cfg.SymbolLimit < 0 {
		cfg.SymbolLimit = 0
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getenvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func splitCSV(v string) []string {
	items := strings.Split(v, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		t := strings.TrimSpace(item)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
