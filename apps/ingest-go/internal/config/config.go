package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	IngestClickHouseURL string

	IngestEnableSpot  bool
	IngestEnableSwap  bool
	IngestEnableDepth bool

	NATSURL            string
	NATSStreamRaw      string
	NATSSubjectTrade   string
	NATSSubjectDepth   string
	NATSConsumerPrefix string

	IngestFlushIntervalSec      int
	IngestDBBatchSize           int
	IngestRuntimeReportInterval int

	BackfillEnable      bool
	BackfillTopN        int
	BackfillConcurrency int
	Backfill1mLimit     int

	BucketWatchdogEnable           bool
	BucketWatchdogIntervalSec      int
	BucketWatchdogTopN             int
	BucketWatchdogWindowMin        int
	BucketWatchdogMaxRepairMinutes int
	BucketWatchdogCooldownSec      int
	BucketWatchdogDiffCheckTopN    int
	BucketWatchdogDiffCheckBatch   int
	BucketWatchdogHotSymbols       []string

	OIRefreshTopN        int
	OIRefreshIntervalSec int

	BinanceSpotREST       string
	BinanceFuturesREST    string
	BinanceBapiProducts   string
	BinanceBapiCompliance string
}

func mustInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func mustBool(name string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func mustString(name, def string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	return v
}

func mustStringSlice(name string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return append([]string(nil), def...)
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		s := strings.ToUpper(strings.TrimSpace(p))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func Load() (*Config, error) {
	c := &Config{
		IngestClickHouseURL: mustString("INGEST_CLICKHOUSE_URL", mustString("CLICKHOUSE_URL", "")),

		IngestEnableSpot:  mustBool("INGEST_ENABLE_SPOT", true),
		IngestEnableSwap:  mustBool("INGEST_ENABLE_SWAP", true),
		IngestEnableDepth: mustBool("INGEST_ENABLE_DEPTH", true),

		NATSURL:            mustString("INGEST_NATS_URL", "nats://nats:4222"),
		NATSStreamRaw:      mustString("INGEST_NATS_STREAM_RAW", "COINMARK_RAW"),
		NATSSubjectTrade:   mustString("INGEST_NATS_SUBJECT_TRADE", "coinmark.raw.trade"),
		NATSSubjectDepth:   mustString("INGEST_NATS_SUBJECT_DEPTH", "coinmark.raw.depth"),
		NATSConsumerPrefix: mustString("INGEST_NATS_CONSUMER_PREFIX", "coinmark-ingest"),

		IngestFlushIntervalSec:      mustInt("INGEST_FLUSH_INTERVAL_SEC", 2),
		IngestDBBatchSize:           mustInt("INGEST_DB_BATCH_SIZE", 1200),
		IngestRuntimeReportInterval: mustInt("INGEST_RUNTIME_REPORT_INTERVAL_SEC", 30),

		BackfillEnable:      mustBool("BACKFILL_ENABLE", false),
		BackfillTopN:        mustInt("BACKFILL_TOP_N", 120),
		BackfillConcurrency: mustInt("BACKFILL_CONCURRENCY", 8),
		Backfill1mLimit:     mustInt("BACKFILL_1M_LIMIT", 1500),

		BucketWatchdogEnable:           mustBool("BUCKET_WATCHDOG_ENABLE", true),
		BucketWatchdogIntervalSec:      mustInt("BUCKET_WATCHDOG_INTERVAL_SEC", 60),
		BucketWatchdogTopN:             mustInt("BUCKET_WATCHDOG_TOP_N", 120),
		BucketWatchdogWindowMin:        mustInt("BUCKET_WATCHDOG_WINDOW_MIN", 10),
		BucketWatchdogMaxRepairMinutes: mustInt("BUCKET_WATCHDOG_MAX_REPAIR_MIN", 180),
		BucketWatchdogCooldownSec:      mustInt("BUCKET_WATCHDOG_COOLDOWN_SEC", 90),
		BucketWatchdogDiffCheckTopN:    mustInt("BUCKET_WATCHDOG_DIFF_CHECK_TOP_N", 120),
		BucketWatchdogDiffCheckBatch:   mustInt("BUCKET_WATCHDOG_DIFF_CHECK_BATCH", 10),
		BucketWatchdogHotSymbols:       mustStringSlice("BUCKET_WATCHDOG_HOT_SYMBOLS", []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}),

		OIRefreshTopN:        mustInt("OI_REFRESH_TOP_N", 300),
		OIRefreshIntervalSec: mustInt("OI_REFRESH_INTERVAL_SEC", 300),

		BinanceSpotREST:       mustString("BINANCE_SPOT_REST", "https://api.binance.com"),
		BinanceFuturesREST:    mustString("BINANCE_FUTURES_REST", "https://fapi.binance.com"),
		BinanceBapiProducts:   mustString("BINANCE_BAPI_PRODUCTS", "https://www.binance.com/bapi/asset/v2/public/asset-service/product/get-products"),
		BinanceBapiCompliance: mustString("BINANCE_BAPI_COMPLIANCE", "https://www.binance.com/bapi/apex/v1/friendly/apex/marketing/complianceSymbolList"),
	}

	if !c.IngestEnableSpot && !c.IngestEnableSwap {
		return nil, fmt.Errorf("no market enabled, check INGEST_ENABLE_SPOT / INGEST_ENABLE_SWAP")
	}
	return c, nil
}

func (c *Config) FlushInterval() time.Duration {
	return time.Duration(max(1, c.IngestFlushIntervalSec)) * time.Second
}

func (c *Config) RuntimeReportInterval() time.Duration {
	return time.Duration(max(10, c.IngestRuntimeReportInterval)) * time.Second
}

func (c *Config) OIRefreshInterval() time.Duration {
	return time.Duration(max(30, c.OIRefreshIntervalSec)) * time.Second
}

func (c *Config) BucketWatchdogInterval() time.Duration {
	return time.Duration(max(5, c.BucketWatchdogIntervalSec)) * time.Second
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
