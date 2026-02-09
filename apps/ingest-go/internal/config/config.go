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

	OIRefreshTopN        int
	OIRefreshIntervalSec int

	BinanceSpotREST     string
	BinanceFuturesREST  string
	BinanceBapiProducts string
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

		OIRefreshTopN:        mustInt("OI_REFRESH_TOP_N", 300),
		OIRefreshIntervalSec: mustInt("OI_REFRESH_INTERVAL_SEC", 300),

		BinanceSpotREST:     mustString("BINANCE_SPOT_REST", "https://api.binance.com"),
		BinanceFuturesREST:  mustString("BINANCE_FUTURES_REST", "https://fapi.binance.com"),
		BinanceBapiProducts: mustString("BINANCE_BAPI_PRODUCTS", "https://www.binance.com/bapi/asset/v2/public/asset-service/product/get-products"),
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
