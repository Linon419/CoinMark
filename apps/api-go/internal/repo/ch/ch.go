package ch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"coinmark/api-go/internal/model"
)

type Client struct {
	baseURL  string
	database string
	user     string
	password string
	http     *http.Client
}

func New(rawURL, database, user, password string) (*Client, error) {
	base := normalizeBaseURL(rawURL)
	if base == "" {
		return nil, fmt.Errorf("ch: empty URL")
	}
	return &Client{
		baseURL:  base,
		database: database,
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func normalizeBaseURL(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return v
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "8123"
	}
	return fmt.Sprintf("%s://%s:%s", u.Scheme, host, port)
}

func (c *Client) QueryJSON(ctx context.Context, sql string) ([]map[string]json.RawMessage, error) {
	params := url.Values{}
	params.Set("query", sql+" FORMAT JSONEachRow")
	params.Set("database", c.database)

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ch: build request: %w", err)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ch: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ch: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ch: status %d: %s", resp.StatusCode, string(body))
	}

	var rows []map[string]json.RawMessage
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("ch: parse row: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (c *Client) Exec(ctx context.Context, sql string) error {
	params := url.Values{}
	params.Set("query", sql)
	params.Set("database", c.database)

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return fmt.Errorf("ch: build request: %w", err)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ch: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ch: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func esc(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return v
}

func inClause(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + esc(v) + "'"
	}
	return strings.Join(parts, ",")
}

func rawStr(r map[string]json.RawMessage, key string) string {
	v, ok := r[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return strings.Trim(string(v), `"`)
	}
	return s
}

func rawFloat(r map[string]json.RawMessage, key string) float64 {
	v, ok := r[key]
	if !ok {
		return 0
	}
	s := strings.Trim(string(v), `"`)
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func rawInt64(r map[string]json.RawMessage, key string) int64 {
	v, ok := r[key]
	if !ok {
		return 0
	}
	s := strings.Trim(string(v), `"`)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func rawInt(r map[string]json.RawMessage, key string) int {
	return int(rawInt64(r, key))
}

func rawFloatPtr(r map[string]json.RawMessage, key string) *float64 {
	v, ok := r[key]
	if !ok || string(v) == "null" {
		return nil
	}
	s := strings.Trim(string(v), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

func rawInt64Ptr(r map[string]json.RawMessage, key string) *int64 {
	v, ok := r[key]
	if !ok || string(v) == "null" || string(v) == "0" {
		return nil
	}
	s := strings.Trim(string(v), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// ---------------------------------------------------------------------------
// Row builders
// ---------------------------------------------------------------------------

func tradeRow(r map[string]json.RawMessage) model.CHTradeRow {
	return model.CHTradeRow{
		Market:            rawStr(r, "market"),
		Symbol:            rawStr(r, "symbol"),
		Bucket:            rawStr(r, "bucket"),
		BucketStartMs:     rawInt64(r, "bucket_start_ms"),
		TakerBuyNotional:  rawFloat(r, "taker_buy_notional"),
		TakerSellNotional: rawFloat(r, "taker_sell_notional"),
		QuoteNotional:     rawFloat(r, "quote_notional"),
		TradeCount:        rawInt64(r, "trade_count"),
		FirstTradeMs:      rawInt64Ptr(r, "first_trade_ms"),
		LastTradeMs:       rawInt64Ptr(r, "last_trade_ms"),
		OpenPrice:         rawFloatPtr(r, "open_price"),
		ClosePrice:        rawFloatPtr(r, "close_price"),
		HighPrice:         rawFloatPtr(r, "high_price"),
		LowPrice:          rawFloatPtr(r, "low_price"),
	}
}

func obRow(r map[string]json.RawMessage) model.CHOBFeatureRow {
	return model.CHOBFeatureRow{
		Market:                rawStr(r, "market"),
		Symbol:                rawStr(r, "symbol"),
		Bucket:                rawStr(r, "bucket"),
		BucketStartMs:         rawInt64(r, "bucket_start_ms"),
		SpreadBpsSum:          rawFloat(r, "spread_bps_sum"),
		MicropriceShiftBpsSum: rawFloat(r, "microprice_shift_bps_sum"),
		DepthImbalanceL20Sum:  rawFloat(r, "depth_imbalance_l20_sum"),
		WallPressureL20Sum:    rawFloat(r, "wall_pressure_l20_sum"),
		SampleCount:           rawInt(r, "sample_count"),
		TakerBuyNotional:      rawFloat(r, "taker_buy_notional"),
		TakerSellNotional:     rawFloat(r, "taker_sell_notional"),
		DepletionEvents:       rawInt(r, "depletion_events"),
		ReplenishmentEvents:   rawInt(r, "replenishment_events"),
	}
}

// ---------------------------------------------------------------------------
// Query functions
// ---------------------------------------------------------------------------

func (c *Client) QueryTradeBuckets(ctx context.Context, market, symbol string, symbols []string, bucket string, startMs, endMs int64, order string, limit int) ([]model.CHTradeRow, error) {
	where := []string{"bucket = '1m'", fmt.Sprintf("bucket_start_ms >= %d", startMs)}
	if market != "" {
		where = append(where, fmt.Sprintf("market = '%s'", esc(market)))
	}
	if symbol != "" {
		where = append(where, fmt.Sprintf("symbol = '%s'", esc(symbol)))
	}
	if len(symbols) > 0 {
		where = append(where, fmt.Sprintf("symbol IN (%s)", inClause(symbols)))
	}
	if endMs > 0 {
		where = append(where, fmt.Sprintf("bucket_start_ms <= %d", endMs))
	}
	dir := "ASC"
	if order == "desc" {
		dir = "DESC"
	}
	w := strings.Join(where, " AND ")

	var sql string
	if bucket == "1m" {
		sql = fmt.Sprintf("SELECT * FROM trade_buckets FINAL WHERE %s ORDER BY symbol ASC, bucket_start_ms %s", w, dir)
	} else {
		bms, ok := model.BucketMs[bucket]
		if !ok {
			bms = 60_000
		}
		agg := fmt.Sprintf("intDiv(bucket_start_ms, %d) * %d", bms, bms)
		sql = fmt.Sprintf(
			"SELECT *, agg_start AS bucket_start_ms FROM ("+
				"SELECT market, symbol, '%s' AS bucket, "+
				"%s AS agg_start, "+
				"sum(taker_buy_notional) AS taker_buy_notional, "+
				"sum(taker_sell_notional) AS taker_sell_notional, "+
				"sum(quote_notional) AS quote_notional, "+
				"toInt64(sum(trade_count)) AS trade_count, "+
				"min(first_trade_ms) AS first_trade_ms, "+
				"max(last_trade_ms) AS last_trade_ms, "+
				"argMin(open_price, bucket_start_ms) AS open_price, "+
				"argMax(close_price, bucket_start_ms) AS close_price, "+
				"max(high_price) AS high_price, "+
				"min(low_price) AS low_price "+
				"FROM (SELECT * FROM trade_buckets FINAL WHERE %s) "+
				"GROUP BY market, symbol, agg_start) "+
				"ORDER BY symbol ASC, bucket_start_ms %s",
			esc(bucket), agg, w, dir,
		)
	}
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]model.CHTradeRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, tradeRow(r))
	}
	return result, nil
}

func (c *Client) QueryTradeAggVolume(ctx context.Context, market, bucket string, startMs int64, limit int) ([]struct {
	Symbol string
	QV     float64
}, error) {
	sql := fmt.Sprintf(
		"SELECT symbol, sum(quote_notional) AS qv FROM trade_buckets FINAL "+
			"WHERE market = '%s' AND bucket = '1m' AND bucket_start_ms >= %d "+
			"GROUP BY symbol ORDER BY qv DESC LIMIT %d",
		esc(market), startMs, limit,
	)
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]struct {
		Symbol string
		QV     float64
	}, 0, len(rows))
	for _, r := range rows {
		result = append(result, struct {
			Symbol string
			QV     float64
		}{Symbol: rawStr(r, "symbol"), QV: rawFloat(r, "qv")})
	}
	return result, nil
}

func (c *Client) QueryTradeFlowAgg(ctx context.Context, market string, symbols []string, bucket string, startMs int64) ([]struct {
	Symbol  string
	BuySum  float64
	SellSum float64
}, error) {
	return c.QueryTradeFlowAggRange(ctx, market, symbols, bucket, startMs, 0)
}

func (c *Client) QueryTradeFlowAggRange(ctx context.Context, market string, symbols []string, bucket string, startMs, endMs int64) ([]struct {
	Symbol  string
	BuySum  float64
	SellSum float64
}, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	where := []string{
		fmt.Sprintf("market = '%s'", esc(market)),
		fmt.Sprintf("bucket = '%s'", esc(bucket)),
		fmt.Sprintf("symbol IN (%s)", inClause(symbols)),
		fmt.Sprintf("bucket_start_ms >= %d", startMs),
	}
	if endMs > 0 {
		where = append(where, fmt.Sprintf("bucket_start_ms <= %d", endMs))
	}
	sql := fmt.Sprintf(
		"SELECT symbol, sum(taker_buy_notional) AS buy_sum, sum(taker_sell_notional) AS sell_sum "+
			"FROM (SELECT * FROM trade_buckets FINAL WHERE %s) GROUP BY symbol",
		strings.Join(where, " AND "),
	)
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]struct {
		Symbol  string
		BuySum  float64
		SellSum float64
	}, 0, len(rows))
	for _, r := range rows {
		result = append(result, struct {
			Symbol  string
			BuySum  float64
			SellSum float64
		}{Symbol: rawStr(r, "symbol"), BuySum: rawFloat(r, "buy_sum"), SellSum: rawFloat(r, "sell_sum")})
	}
	return result, nil
}

func (c *Client) QueryOrderbookFeatures(ctx context.Context, market, symbol string, symbols []string, bucket string, startMs, endMs int64, order string) ([]model.CHOBFeatureRow, error) {
	where := []string{fmt.Sprintf("bucket = '%s'", esc(bucket)), fmt.Sprintf("bucket_start_ms >= %d", startMs)}
	if market != "" {
		where = append(where, fmt.Sprintf("market = '%s'", esc(market)))
	}
	if symbol != "" {
		where = append(where, fmt.Sprintf("symbol = '%s'", esc(symbol)))
	}
	if len(symbols) > 0 {
		where = append(where, fmt.Sprintf("symbol IN (%s)", inClause(symbols)))
	}
	if endMs > 0 {
		where = append(where, fmt.Sprintf("bucket_start_ms <= %d", endMs))
	}
	dir := "ASC"
	if order == "desc" {
		dir = "DESC"
	}
	sql := fmt.Sprintf("SELECT * FROM orderbook_feature_buckets FINAL WHERE %s ORDER BY symbol ASC, bucket_start_ms %s",
		strings.Join(where, " AND "), dir)

	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]model.CHOBFeatureRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, obRow(r))
	}
	return result, nil
}

func (c *Client) QueryFundingSnapshots(ctx context.Context) ([]model.CHFundingRow, error) {
	sql := "SELECT symbol, argMax(last_funding_rate, version) AS last_funding_rate, " +
		"argMax(mark_price, version) AS mark_price, max(event_time_ms) AS event_time_ms " +
		"FROM funding_rate_snapshots GROUP BY symbol"
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]model.CHFundingRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, model.CHFundingRow{
			Symbol:          rawStr(r, "symbol"),
			LastFundingRate: rawFloat(r, "last_funding_rate"),
			MarkPrice:       rawFloat(r, "mark_price"),
			EventTimeMs:     rawInt64(r, "event_time_ms"),
		})
	}
	return result, nil
}

func (c *Client) QueryFundingBySymbol(ctx context.Context, symbol string) (*model.CHFundingRow, error) {
	sql := fmt.Sprintf("SELECT * FROM funding_rate_snapshots WHERE symbol = '%s' ORDER BY version DESC LIMIT 1", esc(symbol))
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &model.CHFundingRow{
		Symbol:          rawStr(r, "symbol"),
		LastFundingRate: rawFloat(r, "last_funding_rate"),
		MarkPrice:       rawFloat(r, "mark_price"),
		EventTimeMs:     rawInt64(r, "event_time_ms"),
	}, nil
}

func (c *Client) QueryOISnapshots(ctx context.Context) ([]model.CHOIRow, error) {
	sql := "SELECT symbol, argMax(open_interest, version) AS open_interest, " +
		"argMax(mark_price, version) AS mark_price, argMax(oi_notional_usd, version) AS oi_notional_usd, " +
		"max(event_time_ms) AS event_time_ms FROM open_interest_snapshots GROUP BY symbol"
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]model.CHOIRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, model.CHOIRow{
			Symbol:        rawStr(r, "symbol"),
			OpenInterest:  rawFloat(r, "open_interest"),
			MarkPrice:     rawFloat(r, "mark_price"),
			OINotionalUSD: rawFloat(r, "oi_notional_usd"),
			EventTimeMs:   rawInt64(r, "event_time_ms"),
		})
	}
	return result, nil
}

func (c *Client) QueryOIBySymbol(ctx context.Context, symbol string) (*model.CHOIRow, error) {
	sql := fmt.Sprintf("SELECT * FROM open_interest_snapshots WHERE symbol = '%s' ORDER BY version DESC LIMIT 1", esc(symbol))
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &model.CHOIRow{
		Symbol:        rawStr(r, "symbol"),
		OpenInterest:  rawFloat(r, "open_interest"),
		MarkPrice:     rawFloat(r, "mark_price"),
		OINotionalUSD: rawFloat(r, "oi_notional_usd"),
		EventTimeMs:   rawInt64(r, "event_time_ms"),
	}, nil
}

func (c *Client) QueryMarketCaps(ctx context.Context, assets []string) ([]model.CHMarketCapRow, error) {
	where := ""
	if len(assets) > 0 {
		where = fmt.Sprintf(" WHERE asset IN (%s)", inClause(assets))
	}
	sql := "SELECT asset, argMax(price_usd, version) AS price_usd, " +
		"argMax(circulating_supply, version) AS circulating_supply, " +
		"argMax(market_cap_usd, version) AS market_cap_usd, " +
		"argMax(source, version) AS source, max(event_time_ms) AS event_time_ms " +
		"FROM asset_market_caps" + where + " GROUP BY asset"
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	result := make([]model.CHMarketCapRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, model.CHMarketCapRow{
			Asset:             rawStr(r, "asset"),
			PriceUSD:          rawFloat(r, "price_usd"),
			CirculatingSupply: rawFloat(r, "circulating_supply"),
			MarketCapUSD:      rawFloat(r, "market_cap_usd"),
			Source:            rawStr(r, "source"),
			EventTimeMs:       rawInt64(r, "event_time_ms"),
		})
	}
	return result, nil
}

func (c *Client) QueryMarketCapByAsset(ctx context.Context, asset string) (*model.CHMarketCapRow, error) {
	sql := fmt.Sprintf("SELECT * FROM asset_market_caps WHERE asset = '%s' ORDER BY version DESC LIMIT 1", esc(asset))
	rows, err := c.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &model.CHMarketCapRow{
		Asset:             rawStr(r, "asset"),
		PriceUSD:          rawFloat(r, "price_usd"),
		CirculatingSupply: rawFloat(r, "circulating_supply"),
		MarketCapUSD:      rawFloat(r, "market_cap_usd"),
		Source:            rawStr(r, "source"),
		EventTimeMs:       rawInt64(r, "event_time_ms"),
	}, nil
}

func (c *Client) InsertTradeBuckets(ctx context.Context, rows []map[string]interface{}) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	version := time.Now().UnixMilli()
	var parts []string
	for _, r := range rows {
		part := fmt.Sprintf("('%s','%s','%s',%d,%f,%f,%f,%d,%d,%d,%f,%f,%f,%f,%d)",
			esc(fmt.Sprint(r["market"])),
			esc(fmt.Sprint(r["symbol"])),
			esc(fmt.Sprint(r["bucket"])),
			toInt64(r["bucket_start_ms"]),
			toFloat(r["taker_buy_notional"]),
			toFloat(r["taker_sell_notional"]),
			toFloat(r["quote_notional"]),
			toInt64(r["trade_count"]),
			toInt64(r["first_trade_ms"]),
			toInt64(r["last_trade_ms"]),
			toFloat(r["open_price"]),
			toFloat(r["close_price"]),
			toFloat(r["high_price"]),
			toFloat(r["low_price"]),
			version,
		)
		parts = append(parts, part)
	}
	sql := "INSERT INTO trade_buckets " +
		"(market,symbol,bucket,bucket_start_ms," +
		"taker_buy_notional,taker_sell_notional,quote_notional,trade_count," +
		"first_trade_ms,last_trade_ms,open_price,close_price,high_price,low_price,version) VALUES " +
		strings.Join(parts, ",")

	if err := c.Exec(ctx, sql); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func toFloat(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func toInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}
