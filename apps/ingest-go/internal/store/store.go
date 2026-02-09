package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"coinmark/ingest-go/internal/ingest"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
)

type Store struct {
	ch      clickhouse.Conn
	tradeMu sync.Mutex
	tradeAc map[tradeBucketKey]*tradeBucketState
	obMu    sync.Mutex
	obAc    map[orderbookBucketKey]*orderbookBucketState
}

func New(ctx context.Context, clickhouseURL string) (*Store, error) {
	ch, err := initClickHouse(ctx, strings.TrimSpace(clickhouseURL))
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, fmt.Errorf("CLICKHOUSE_URL is required")
	}
	return &Store{
		ch:      ch,
		tradeAc: make(map[tradeBucketKey]*tradeBucketState),
		obAc:    make(map[orderbookBucketKey]*orderbookBucketState),
	}, nil
}

func (s *Store) Close() {
	if s.ch != nil {
		_ = s.ch.Close()
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func dec(v decimal.Decimal) float64 {
	f, _ := v.Float64()
	return f
}

func decPtr(v *decimal.Decimal) *float64 {
	if v == nil {
		return nil
	}
	f, _ := v.Float64()
	return &f
}

func ptrToAny[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

func chunk[T any](in []T, size int) [][]T {
	if len(in) == 0 {
		return nil
	}
	if size <= 0 {
		return [][]T{in}
	}
	out := make([][]T, 0, (len(in)+size-1)/size)
	for i := 0; i < len(in); i += size {
		j := i + size
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

// ---------------------------------------------------------------------------
// trade_buckets  (in-memory accumulation → ClickHouse)
// ---------------------------------------------------------------------------

type tradeBucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

type tradeBucketState struct {
	TakerBuyNotional  float64
	TakerSellNotional float64
	QuoteNotional     float64
	TradeCount        int64
	FirstTradeMS      *int64
	LastTradeMS       *int64
	OpenPrice         *float64
	ClosePrice        *float64
	HighPrice         *float64
	LowPrice          *float64
}

func (st *tradeBucketState) merge(d *ingest.BucketDelta) {
	st.TakerBuyNotional += dec(d.TakerBuyNotional)
	st.TakerSellNotional += dec(d.TakerSellNotional)
	st.QuoteNotional += dec(d.QuoteNotional)
	st.TradeCount += d.TradeCount

	if d.FirstTradeMS != nil {
		v := *d.FirstTradeMS
		if st.FirstTradeMS == nil || v < *st.FirstTradeMS {
			st.FirstTradeMS = &v
			st.OpenPrice = decPtr(d.OpenPrice)
		}
	}
	if d.LastTradeMS != nil {
		v := *d.LastTradeMS
		if st.LastTradeMS == nil || v > *st.LastTradeMS {
			st.LastTradeMS = &v
			st.ClosePrice = decPtr(d.ClosePrice)
		}
	}
	if d.HighPrice != nil {
		f := dec(*d.HighPrice)
		if st.HighPrice == nil || f > *st.HighPrice {
			st.HighPrice = &f
		}
	}
	if d.LowPrice != nil {
		f := dec(*d.LowPrice)
		if st.LowPrice == nil || f < *st.LowPrice {
			st.LowPrice = &f
		}
	}
}

type tradePushItem struct {
	key   tradeBucketKey
	state tradeBucketState
}

func (s *Store) UpsertTradeBuckets(ctx context.Context, rows []ingest.TradeDrainItem, batchSize int) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	nowMS := time.Now().UTC().UnixMilli()

	s.tradeMu.Lock()
	seen := make(map[tradeBucketKey]struct{}, len(rows))
	for _, r := range rows {
		k := tradeBucketKey{r.Key.Market, r.Key.Symbol, r.Key.Bucket, r.Key.BucketStartMS}
		st, ok := s.tradeAc[k]
		if !ok {
			st = &tradeBucketState{}
			s.tradeAc[k] = st
		}
		st.merge(r.Delta)
		seen[k] = struct{}{}
	}
	items := make([]tradePushItem, 0, len(seen))
	for k := range seen {
		items = append(items, tradePushItem{k, *s.tradeAc[k]})
	}
	for k := range s.tradeAc {
		dur, _ := ingest.BucketMS(k.Bucket)
		if dur > 0 && k.BucketStartMS+dur*2 < nowMS {
			delete(s.tradeAc, k)
		}
	}
	s.tradeMu.Unlock()

	version := uint64(nowMS)
	for _, part := range chunk(items, batchSize) {
		batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO trade_buckets (
			market, symbol, bucket, bucket_start_ms,
			taker_buy_notional, taker_sell_notional, quote_notional, trade_count,
			first_trade_ms, last_trade_ms, open_price, close_price, high_price, low_price, version
		)`)
		if err != nil {
			return 0, err
		}
		for _, it := range part {
			if err := batch.Append(
				it.key.Market, it.key.Symbol, it.key.Bucket, it.key.BucketStartMS,
				it.state.TakerBuyNotional, it.state.TakerSellNotional, it.state.QuoteNotional, it.state.TradeCount,
				ptrToAny(it.state.FirstTradeMS), ptrToAny(it.state.LastTradeMS),
				ptrToAny(it.state.OpenPrice), ptrToAny(it.state.ClosePrice),
				ptrToAny(it.state.HighPrice), ptrToAny(it.state.LowPrice),
				version,
			); err != nil {
				return 0, err
			}
		}
		if err := batch.Send(); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// ---------------------------------------------------------------------------
// orderbook_feature_buckets  (in-memory accumulation → ClickHouse)
// ---------------------------------------------------------------------------

type orderbookBucketKey struct {
	Market        string
	Symbol        string
	Bucket        string
	BucketStartMS int64
}

type orderbookBucketState struct {
	SpreadBPSSum          float64
	DepthImbalanceL5Sum   float64
	MicropriceShiftBPSSum float64
	WallPressureL5Sum     float64
	DepthImbalanceL20Sum  float64
	WallPressureL20Sum    float64
	SampleCount           int64
	TakerBuyNotional      float64
	TakerSellNotional     float64
	DepletionEvents       int64
	ReplenishmentEvents   int64
}

func (st *orderbookBucketState) merge(d *ingest.OrderbookBucketDelta) {
	st.SpreadBPSSum += dec(d.SpreadBPSSum)
	st.DepthImbalanceL5Sum += dec(d.DepthImbalanceL5Sum)
	st.MicropriceShiftBPSSum += dec(d.MicropriceShiftBPSSum)
	st.WallPressureL5Sum += dec(d.WallPressureL5Sum)
	st.DepthImbalanceL20Sum += dec(d.DepthImbalanceL20Sum)
	st.WallPressureL20Sum += dec(d.WallPressureL20Sum)
	st.SampleCount += d.SampleCount
	st.TakerBuyNotional += dec(d.TakerBuyNotional)
	st.TakerSellNotional += dec(d.TakerSellNotional)
	st.DepletionEvents += d.DepletionEvents
	st.ReplenishmentEvents += d.ReplenishmentEvents
}

type obPushItem struct {
	key   orderbookBucketKey
	state orderbookBucketState
}

func (s *Store) UpsertOrderbookBuckets(ctx context.Context, rows []ingest.OrderbookDrainItem, batchSize int) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	nowMS := time.Now().UTC().UnixMilli()

	s.obMu.Lock()
	seen := make(map[orderbookBucketKey]struct{}, len(rows))
	for _, r := range rows {
		k := orderbookBucketKey{r.Key.Market, r.Key.Symbol, r.Key.Bucket, r.Key.BucketStartMS}
		st, ok := s.obAc[k]
		if !ok {
			st = &orderbookBucketState{}
			s.obAc[k] = st
		}
		st.merge(r.Delta)
		seen[k] = struct{}{}
	}
	items := make([]obPushItem, 0, len(seen))
	for k := range seen {
		items = append(items, obPushItem{k, *s.obAc[k]})
	}
	for k := range s.obAc {
		dur, _ := ingest.BucketMS(k.Bucket)
		if dur > 0 && k.BucketStartMS+dur*2 < nowMS {
			delete(s.obAc, k)
		}
	}
	s.obMu.Unlock()

	version := uint64(nowMS)
	for _, part := range chunk(items, batchSize) {
		batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO orderbook_feature_buckets (
			market, symbol, bucket, bucket_start_ms,
			spread_bps_sum, depth_imbalance_l5_sum, microprice_shift_bps_sum, wall_pressure_l5_sum,
			depth_imbalance_l20_sum, wall_pressure_l20_sum, sample_count,
			taker_buy_notional, taker_sell_notional, depletion_events, replenishment_events, version
		)`)
		if err != nil {
			return 0, err
		}
		for _, it := range part {
			if err := batch.Append(
				it.key.Market, it.key.Symbol, it.key.Bucket, it.key.BucketStartMS,
				it.state.SpreadBPSSum, it.state.DepthImbalanceL5Sum, it.state.MicropriceShiftBPSSum, it.state.WallPressureL5Sum,
				it.state.DepthImbalanceL20Sum, it.state.WallPressureL20Sum, it.state.SampleCount,
				it.state.TakerBuyNotional, it.state.TakerSellNotional, it.state.DepletionEvents, it.state.ReplenishmentEvents,
				version,
			); err != nil {
				return 0, err
			}
		}
		if err := batch.Send(); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// ---------------------------------------------------------------------------
// snapshot tables  (direct ClickHouse push, no accumulation needed)
// ---------------------------------------------------------------------------

type MarketCapRow struct {
	Asset             string
	PriceUSD          decimal.Decimal
	CirculatingSupply decimal.Decimal
	MarketCapUSD      decimal.Decimal
	Source            string
	EventTimeMS       int64
}

func (s *Store) UpsertMarketCaps(ctx context.Context, rows []MarketCapRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO asset_market_caps (
		asset, price_usd, circulating_supply, market_cap_usd, source, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, r := range rows {
		if err := batch.Append(r.Asset, dec(r.PriceUSD), dec(r.CirculatingSupply), dec(r.MarketCapUSD), r.Source, r.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

type FundingSnapshotRow struct {
	Symbol          string
	LastFundingRate decimal.Decimal
	MarkPrice       decimal.Decimal
	EventTimeMS     int64
}

func (s *Store) UpsertFundingSnapshots(ctx context.Context, rows []FundingSnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO funding_rate_snapshots (
		symbol, last_funding_rate, mark_price, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, r := range rows {
		if err := batch.Append(r.Symbol, dec(r.LastFundingRate), dec(r.MarkPrice), r.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

type OISnapshotRow struct {
	Symbol        string
	OpenInterest  decimal.Decimal
	MarkPrice     decimal.Decimal
	OINotionalUSD decimal.Decimal
	EventTimeMS   int64
}

func (s *Store) UpsertOISnapshots(ctx context.Context, rows []OISnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.ch.PrepareBatch(ctx, `INSERT INTO open_interest_snapshots (
		symbol, open_interest, mark_price, oi_notional_usd, event_time_ms, version
	)`)
	if err != nil {
		return err
	}
	version := uint64(time.Now().UTC().UnixMilli())
	for _, r := range rows {
		if err := batch.Append(r.Symbol, dec(r.OpenInterest), dec(r.MarkPrice), dec(r.OINotionalUSD), r.EventTimeMS, version); err != nil {
			return err
		}
	}
	return batch.Send()
}

// ---------------------------------------------------------------------------
// ClickHouse connection & schema
// ---------------------------------------------------------------------------

func initClickHouse(ctx context.Context, raw string) (clickhouse.Conn, error) {
	if raw == "" {
		return nil, nil
	}
	conn, err := newClickHouseConn(raw)
	if err != nil {
		return nil, fmt.Errorf("connect clickhouse failed: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping clickhouse failed: %w", err)
	}
	if err := initClickHouseSchema(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("init clickhouse schema failed: %w", err)
	}
	return conn, nil
}

func newClickHouseConn(raw string) (clickhouse.Conn, error) {
	addr := strings.TrimSpace(raw)
	database := strings.TrimSpace(os.Getenv("CLICKHOUSE_DB"))
	if database == "" {
		database = "default"
	}
	username := strings.TrimSpace(os.Getenv("CLICKHOUSE_USER"))
	if username == "" {
		username = "default"
	}
	password := os.Getenv("CLICKHOUSE_PASSWORD")

	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		host := u.Host
		if strings.Contains(host, ":") {
			hostPort := strings.Split(host, ":")
			if len(hostPort) == 2 && hostPort[1] == "8123" {
				host = hostPort[0] + ":9000"
			}
		} else {
			host += ":9000"
		}
		addr = host
		if dbFromURL := strings.TrimPrefix(u.Path, "/"); dbFromURL != "" {
			database = dbFromURL
		}
		if u.User != nil {
			if uName := u.User.Username(); uName != "" {
				username = uName
			}
			if uPwd, ok := u.User.Password(); ok {
				password = uPwd
			}
		}
	} else {
		addr = strings.TrimPrefix(addr, "http://")
		addr = strings.TrimPrefix(addr, "https://")
		if strings.Contains(addr, "/") {
			addr = strings.Split(addr, "/")[0]
		}
		if strings.Contains(addr, ":") {
			hostPort := strings.Split(addr, ":")
			if len(hostPort) == 2 && hostPort[1] == "8123" {
				addr = hostPort[0] + ":9000"
			}
		} else {
			addr += ":9000"
		}
	}
	return clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: database, Username: username, Password: password},
		DialTimeout: 5 * time.Second,
	})
}

func initClickHouseSchema(ctx context.Context, conn clickhouse.Conn) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trade_buckets (
			market String,
			symbol String,
			bucket String,
			bucket_start_ms Int64,
			taker_buy_notional Float64,
			taker_sell_notional Float64,
			quote_notional Float64,
			trade_count Int64,
			first_trade_ms Nullable(Int64),
			last_trade_ms Nullable(Int64),
			open_price Nullable(Float64),
			close_price Nullable(Float64),
			high_price Nullable(Float64),
			low_price Nullable(Float64),
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (market, symbol, bucket, bucket_start_ms);`,
		`CREATE TABLE IF NOT EXISTS orderbook_feature_buckets (
			market String,
			symbol String,
			bucket String,
			bucket_start_ms Int64,
			spread_bps_sum Float64,
			depth_imbalance_l5_sum Float64,
			microprice_shift_bps_sum Float64,
			wall_pressure_l5_sum Float64,
			depth_imbalance_l20_sum Float64 DEFAULT 0,
			wall_pressure_l20_sum Float64 DEFAULT 0,
			sample_count Int64,
			taker_buy_notional Float64,
			taker_sell_notional Float64,
			depletion_events Int64,
			replenishment_events Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (market, symbol, bucket, bucket_start_ms);`,
		`ALTER TABLE orderbook_feature_buckets ADD COLUMN IF NOT EXISTS depth_imbalance_l20_sum Float64 DEFAULT 0;`,
		`ALTER TABLE orderbook_feature_buckets ADD COLUMN IF NOT EXISTS wall_pressure_l20_sum Float64 DEFAULT 0;`,
		`CREATE TABLE IF NOT EXISTS asset_market_caps (
			asset String,
			price_usd Float64,
			circulating_supply Float64,
			market_cap_usd Float64,
			source String,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (asset);`,
		`CREATE TABLE IF NOT EXISTS funding_rate_snapshots (
			symbol String,
			last_funding_rate Float64,
			mark_price Float64,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (symbol);`,
		`CREATE TABLE IF NOT EXISTS open_interest_snapshots (
			symbol String,
			open_interest Float64,
			mark_price Float64,
			oi_notional_usd Float64,
			event_time_ms Int64,
			version UInt64
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY (symbol);`,
	}
	for _, stmt := range stmts {
		if err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
