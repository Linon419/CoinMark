package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// General
	TZ string

	// Database
	DatabaseURL        string
	RedisURL           string
	ClickHouseURL      string
	ClickHouseDB       string
	ClickHouseUser     string
	ClickHousePassword string

	// API server
	Host     string
	Port     int
	LogLevel string

	// Hub (WebSocket)
	HubEnabled                  bool
	HubAllowedOrigins           string
	HubMaxConnections           int
	HubHeartbeatTimeoutSec      int
	HubHeartbeatIntervalSec     int
	HubDedupeWindowSec          int
	HubBroadcastMaxEventsPerSec int
	HubAnomalyScanIntervalSec   int
	HubAnomalyScanBatchSize     int
	HubClimaxScanIntervalSec    int

	// Depth fullscan
	DepthFullscanEnabled         bool
	DepthFullscanMarket          string
	DepthFullscanLimitSwap       int
	DepthFullscanLimitSpot       int
	DepthFullscanSymbols         string
	DepthFullscanFastSymbols     string
	DepthFullscanFastIntervalSec int
	DepthFullscanSlowIntervalSec int
	DepthFullscanConcurrency     int
	DepthFullscanJitterSec       int
	DepthHeatmapEnabled          bool
	DepthHeatmapForceSpot        bool
	DepthHeatmapBandPct          float64
	DepthHeatmapStepBps          float64
	DepthHeatmapMinIntensityUSD  float64
	DepthHeatmapStepOverrides    string

	// Backfill
	BackfillEnable      bool
	BackfillTopN        int
	BackfillConcurrency int
	Backfill1mLimit     int
	Backfill15mLimit    int
	Backfill1hLimit     int
	Backfill4hLimit     int
	Backfill1dLimit     int

	// OI refresh
	OIRefreshTopN        int
	OIRefreshIntervalSec int

	// SR refresh
	SRRefreshTopN        int
	SRRefreshIntervalSec int

	// Anomaly
	AnomalyScanTopN               int
	AnomalyScanIntervalSec        int
	MarketYidongMinuteEnabled     bool
	MarketYidongMinuteIntervalSec int
	MarketYidongVolumeEnabled     bool
	MarketYidongVolumeIntervalSec int
	AbsorptionScanEnabled         bool
	ClimaxScanEnabled             bool
	AnomalyHistory15m             int
	AnomalyBreakoutMarginPct      float64
	AnomalyVolumeSpikeFactory     float64
	AnomalyAmplitudeSpikeFactory  float64

	// Absorption
	AbsorptionSnapshotRetentionHours     int
	AbsorptionSnapshotCleanupIntervalSec int
	TradeBucketRetentionDays             int
	OrderbookBucketRetentionDays         int
	SQLiteVacuumIntervalSec              int

	// Rank
	RankBucket         string
	RankHistoryBuckets int
	RankMinAvgNotional float64

	// Market cap
	MarketCapSource string

	// Telegram
	TGEnabled               bool
	TGNotifyBotToken        string
	TGQueryBotToken         string
	TGNotifyChatID          string
	TGNotifyAdminChatID     string
	TGNotifyMarket          string
	TGNotifyPollIntervalSec int
	TGNotifyBatchWindowSec  int
	TGNotifyBatchMaxItems   int
	TGNotifyMinLevel        string
	TGQueryPollTimeoutSec   int
	TGStateRedisPrefix      string
}

func Load() (*Config, error) {
	dbURL := getenv("DATABASE_URL", "")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	// strip Python SQLAlchemy prefix: "sqlite+aiosqlite:///path" → "/path"
	for _, prefix := range []string{"sqlite+aiosqlite://", "sqlite://"} {
		if strings.HasPrefix(dbURL, prefix) {
			dbURL = strings.TrimPrefix(dbURL, prefix)
			break
		}
	}
	redisURL := getenv("REDIS_URL", "")
	if redisURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}

	return &Config{
		TZ: getenv("TZ", "Australia/Sydney"),

		DatabaseURL:        dbURL,
		RedisURL:           redisURL,
		ClickHouseURL:      getenv("CLICKHOUSE_URL", ""),
		ClickHouseDB:       getenv("CLICKHOUSE_DB", "default"),
		ClickHouseUser:     getenv("CLICKHOUSE_USER", ""),
		ClickHousePassword: getenv("CLICKHOUSE_PASSWORD", ""),

		Host:     getenv("API_HOST", "0.0.0.0"),
		Port:     getenvInt("API_PORT", 8000),
		LogLevel: getenv("API_LOG_LEVEL", "info"),

		HubEnabled:                  getenvBool("HUB_ENABLED", true),
		HubAllowedOrigins:           getenv("HUB_ALLOWED_ORIGINS", "*"),
		HubMaxConnections:           getenvInt("HUB_MAX_CONNECTIONS", 1000),
		HubHeartbeatTimeoutSec:      getenvInt("HUB_HEARTBEAT_TIMEOUT_SEC", 45),
		HubHeartbeatIntervalSec:     getenvInt("HUB_HEARTBEAT_INTERVAL_SEC", 15),
		HubDedupeWindowSec:          getenvInt("HUB_DEDUPE_WINDOW_SEC", 60),
		HubBroadcastMaxEventsPerSec: getenvInt("HUB_BROADCAST_MAX_EVENTS_PER_SEC", 200),
		HubAnomalyScanIntervalSec:   getenvInt("HUB_ANOMALY_SCAN_INTERVAL_SEC", 2),
		HubAnomalyScanBatchSize:     getenvInt("HUB_ANOMALY_SCAN_BATCH_SIZE", 200),
		HubClimaxScanIntervalSec:    getenvInt("HUB_CLIMAX_SCAN_INTERVAL_SEC", 60),

		DepthFullscanEnabled:         getenvBool("DEPTH_FULLSCAN_ENABLED", true),
		DepthFullscanMarket:          getenv("DEPTH_FULLSCAN_MARKET", "swap"),
		DepthFullscanLimitSwap:       getenvInt("DEPTH_FULLSCAN_LIMIT_SWAP", 1000),
		DepthFullscanLimitSpot:       getenvInt("DEPTH_FULLSCAN_LIMIT_SPOT", 5000),
		DepthFullscanSymbols:         getenv("DEPTH_FULLSCAN_SYMBOLS", "BTC,ETH,BNB,SOL,DOGE,LTC,LDO,CRV,LINK,ADA,UNI,ONDO,AAVE,AVAX,1000PEPE,SUI,SEI,WLD,HYPE,TRUMP,PUMP,ZEC"),
		DepthFullscanFastSymbols:     getenv("DEPTH_FULLSCAN_FAST_SYMBOLS", "BTC,ETH,BNB,SOL"),
		DepthFullscanFastIntervalSec: getenvInt("DEPTH_FULLSCAN_FAST_INTERVAL_SEC", 300),
		DepthFullscanSlowIntervalSec: getenvInt("DEPTH_FULLSCAN_SLOW_INTERVAL_SEC", 900),
		DepthFullscanConcurrency:     getenvInt("DEPTH_FULLSCAN_CONCURRENCY", 4),
		DepthFullscanJitterSec:       getenvInt("DEPTH_FULLSCAN_JITTER_SEC", 45),
		DepthHeatmapEnabled:          getenvBool("DEPTH_HEATMAP_ENABLED", true),
		DepthHeatmapForceSpot:        getenvBool("DEPTH_HEATMAP_FORCE_SPOT", true),
		DepthHeatmapBandPct:          getenvFloat("DEPTH_HEATMAP_BAND_PCT", 0.05),
		DepthHeatmapStepBps:          getenvFloat("DEPTH_HEATMAP_STEP_BPS", 8.0),
		DepthHeatmapMinIntensityUSD:  getenvFloat("DEPTH_HEATMAP_MIN_INTENSITY_USD", 10000.0),
		DepthHeatmapStepOverrides:    getenv("DEPTH_HEATMAP_STEP_OVERRIDES", ""),

		BackfillEnable:      getenvBool("BACKFILL_ENABLE", true),
		BackfillTopN:        getenvInt("BACKFILL_TOP_N", 120),
		BackfillConcurrency: getenvInt("BACKFILL_CONCURRENCY", 8),
		Backfill1mLimit:     getenvInt("BACKFILL_1M_LIMIT", 0),
		Backfill15mLimit:    getenvInt("BACKFILL_15M_LIMIT", 200),
		Backfill1hLimit:     getenvInt("BACKFILL_1H_LIMIT", 200),
		Backfill4hLimit:     getenvInt("BACKFILL_4H_LIMIT", 180),
		Backfill1dLimit:     getenvInt("BACKFILL_1D_LIMIT", 60),

		OIRefreshTopN:        getenvInt("OI_REFRESH_TOP_N", 300),
		OIRefreshIntervalSec: getenvInt("OI_REFRESH_INTERVAL_SEC", 300),

		SRRefreshTopN:        getenvInt("SR_REFRESH_TOP_N", 200),
		SRRefreshIntervalSec: getenvInt("SR_REFRESH_INTERVAL_SEC", 1800),

		AnomalyScanTopN:               getenvInt("ANOMALY_SCAN_TOP_N", 200),
		AnomalyScanIntervalSec:        getenvInt("ANOMALY_SCAN_INTERVAL_SEC", 60),
		MarketYidongMinuteEnabled:     getenvBool("MARKET_YIDONG_MINUTE_ENABLED", true),
		MarketYidongMinuteIntervalSec: getenvInt("MARKET_YIDONG_MINUTE_INTERVAL_SEC", 60),
		MarketYidongVolumeEnabled:     getenvBool("MARKET_YIDONG_VOLUME_ENABLED", true),
		MarketYidongVolumeIntervalSec: getenvInt("MARKET_YIDONG_VOLUME_INTERVAL_SEC", 60),
		AbsorptionScanEnabled:         getenvBool("ABSORPTION_SCAN_ENABLED", true),
		ClimaxScanEnabled:             getenvBool("CLIMAX_SCAN_ENABLED", true),
		AnomalyHistory15m:             getenvInt("ANOMALY_HISTORY_15M", 96),
		AnomalyBreakoutMarginPct:      getenvFloat("ANOMALY_BREAKOUT_MARGIN_PCT", 0.001),
		AnomalyVolumeSpikeFactory:     getenvFloat("ANOMALY_VOLUME_SPIKE_FACTOR", 3.0),
		AnomalyAmplitudeSpikeFactory:  getenvFloat("ANOMALY_AMPLITUDE_SPIKE_FACTOR", 2.5),

		AbsorptionSnapshotRetentionHours:     getenvInt("ABSORPTION_SNAPSHOT_RETENTION_HOURS", 24),
		AbsorptionSnapshotCleanupIntervalSec: getenvInt("ABSORPTION_SNAPSHOT_CLEANUP_INTERVAL_SEC", 900),
		TradeBucketRetentionDays:             getenvInt("TRADE_BUCKET_RETENTION_DAYS", 7),
		OrderbookBucketRetentionDays:         getenvInt("ORDERBOOK_BUCKET_RETENTION_DAYS", 3),
		SQLiteVacuumIntervalSec:              getenvInt("SQLITE_VACUUM_INTERVAL_SEC", 21600),

		RankBucket:         getenv("RANK_BUCKET", "15m"),
		RankHistoryBuckets: getenvInt("RANK_HISTORY_BUCKETS", 96),
		RankMinAvgNotional: getenvFloat("RANK_MIN_AVG_NOTIONAL", 1000.0),

		MarketCapSource: getenv("MARKET_CAP_SOURCE", "binance_bapi_get_products"),

		TGEnabled:               getenvBool("TG_ENABLED", false),
		TGNotifyBotToken:        getenv("TG_NOTIFY_BOT_TOKEN", ""),
		TGQueryBotToken:         getenv("TG_QUERY_BOT_TOKEN", ""),
		TGNotifyChatID:          getenv("TG_NOTIFY_CHAT_ID", ""),
		TGNotifyAdminChatID:     getenv("TG_NOTIFY_ADMIN_CHAT_ID", ""),
		TGNotifyMarket:          getenv("TG_NOTIFY_MARKET", "swap"),
		TGNotifyPollIntervalSec: getenvInt("TG_NOTIFY_POLL_INTERVAL_SEC", 5),
		TGNotifyBatchWindowSec:  getenvInt("TG_NOTIFY_BATCH_WINDOW_SEC", 30),
		TGNotifyBatchMaxItems:   getenvInt("TG_NOTIFY_BATCH_MAX_ITEMS", 5),
		TGNotifyMinLevel:        getenv("TG_NOTIFY_MIN_LEVEL", "warning"),
		TGQueryPollTimeoutSec:   getenvInt("TG_QUERY_POLL_TIMEOUT_SEC", 25),
		TGStateRedisPrefix:      getenv("TG_STATE_REDIS_PREFIX", "coinmark:tg"),
	}, nil
}

func (c *Config) DepthFullscanLimit() int {
	if strings.ToLower(c.DepthFullscanMarket) == "spot" {
		return c.DepthFullscanLimitSpot
	}
	return c.DepthFullscanLimitSwap
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			return true
		case "0", "false", "no":
			return false
		}
	}
	return fallback
}
