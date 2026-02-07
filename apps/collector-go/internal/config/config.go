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
	KafkaBrokers     []string
	KafkaTopic       string
	KafkaDepthTopic  string
	KafkaClientID    string
	Symbols          []string
	SymbolLimit      int
	StreamsPerConn   int
	Market           string
	EnableDepth      bool
	DepthUpdateMs    int
	LogIntervalSec   int
}

func Load() (Config, error) {
	cfg := Config{
		BinanceWSBaseURL: getenv("COLLECTOR_BINANCE_WS_BASE_URL", "wss://fstream.binance.com/stream"),
		BinanceRESTBase:  getenv("COLLECTOR_BINANCE_REST_BASE", "https://fapi.binance.com"),
		KafkaBrokers:     splitCSV(getenv("COLLECTOR_KAFKA_BROKERS", "redpanda:9092")),
		KafkaTopic:       getenv("COLLECTOR_KAFKA_TOPIC", "coinmark.raw_trade.poc"),
		KafkaDepthTopic:  getenv("COLLECTOR_KAFKA_DEPTH_TOPIC", "coinmark.raw_depth.poc"),
		KafkaClientID:    getenv("COLLECTOR_KAFKA_CLIENT_ID", "collector-go-poc"),
		Symbols:          splitCSV(getenv("COLLECTOR_SYMBOLS", "")),
		SymbolLimit:      getenvInt("COLLECTOR_SYMBOL_LIMIT", 0),
		StreamsPerConn:   getenvInt("COLLECTOR_STREAMS_PER_CONN", 200),
		Market:           strings.ToLower(getenv("COLLECTOR_MARKET", "swap")),
		EnableDepth:      getenvBool("COLLECTOR_ENABLE_DEPTH", true),
		DepthUpdateMs:    getenvInt("COLLECTOR_DEPTH_UPDATE_MS", 100),
		LogIntervalSec:   getenvInt("COLLECTOR_LOG_INTERVAL_SEC", 15),
	}

	if cfg.Market != "spot" && cfg.Market != "swap" {
		return Config{}, fmt.Errorf("COLLECTOR_MARKET must be 'spot' or 'swap', got: %s", cfg.Market)
	}
	if len(cfg.KafkaBrokers) == 0 {
		return Config{}, fmt.Errorf("COLLECTOR_KAFKA_BROKERS is empty")
	}
	if cfg.KafkaTopic == "" {
		return Config{}, fmt.Errorf("COLLECTOR_KAFKA_TOPIC is empty")
	}
	if cfg.EnableDepth && cfg.KafkaDepthTopic == "" {
		return Config{}, fmt.Errorf("COLLECTOR_KAFKA_DEPTH_TOPIC is empty while COLLECTOR_ENABLE_DEPTH=true")
	}
	if cfg.DepthUpdateMs < 100 {
		cfg.DepthUpdateMs = 100
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
