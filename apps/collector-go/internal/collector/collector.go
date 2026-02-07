package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"coinmark/collector-go/internal/binance"
	"coinmark/collector-go/internal/config"
	"coinmark/collector-go/internal/kafka"
)

type Collector struct {
	cfg config.Config

	tradeMsgCount         atomic.Int64
	tradeByteCount        atomic.Int64
	depthMsgCount         atomic.Int64
	depthByteCount        atomic.Int64
	wsReconnectCount      atomic.Int64
	tradeSendFailCount    atomic.Int64
	depthSendFailCount    atomic.Int64
	tradeProducer         *kafka.Producer
	depthProducer         *kafka.Producer
	depthUseTradeProducer bool
}

func New(cfg config.Config) (*Collector, error) {
	tradeProducer, err := kafka.NewProducer(cfg.KafkaBrokers, cfg.KafkaClientID, cfg.KafkaTopic)
	if err != nil {
		return nil, err
	}

	depthProducer := tradeProducer
	depthUseTradeProducer := true
	if cfg.EnableDepth && cfg.KafkaDepthTopic != cfg.KafkaTopic {
		depthProducer, err = kafka.NewProducer(cfg.KafkaBrokers, cfg.KafkaClientID+"-depth", cfg.KafkaDepthTopic)
		if err != nil {
			_ = tradeProducer.Close()
			return nil, err
		}
		depthUseTradeProducer = false
	}

	return &Collector{
		cfg:                   cfg,
		tradeProducer:         tradeProducer,
		depthProducer:         depthProducer,
		depthUseTradeProducer: depthUseTradeProducer,
	}, nil
}

func (c *Collector) Run(ctx context.Context) error {
	defer func() {
		if err := c.tradeProducer.Close(); err != nil {
			log.Printf("trade producer close failed: %v", err)
		}
		if !c.depthUseTradeProducer {
			if err := c.depthProducer.Close(); err != nil {
				log.Printf("depth producer close failed: %v", err)
			}
		}
	}()

	log.Printf(
		"collector starting market=%s ws=%s trade_topic=%s depth_enabled=%t depth_topic=%s brokers=%v",
		c.cfg.Market,
		c.cfg.BinanceWSBaseURL,
		c.cfg.KafkaTopic,
		c.cfg.EnableDepth,
		c.cfg.KafkaDepthTopic,
		c.cfg.KafkaBrokers,
	)

	symbols, err := c.resolveSymbols(ctx)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	reportErr := func(err error) {
		if err == nil {
			return
		}
		select {
		case errCh <- err:
		default:
		}
	}

	tradeChunks := buildStreamChunks(symbols, c.cfg.StreamsPerConn, "aggTrade")
	log.Printf("collector trade streams symbols=%d chunk_size=%d conn=%d", len(symbols), c.cfg.StreamsPerConn, len(tradeChunks))
	for _, chunk := range tradeChunks {
		streams := append([]string(nil), chunk...)
		go func(items []string) {
			reportErr(c.runTradeLoop(ctx, items))
		}(streams)
	}

	if c.cfg.EnableDepth {
		depthSuffix := fmt.Sprintf("depth5@%dms", c.cfg.DepthUpdateMs)
		depthChunks := buildStreamChunks(symbols, c.cfg.StreamsPerConn, depthSuffix)
		log.Printf(
			"collector depth streams symbols=%d chunk_size=%d conn=%d update=%dms",
			len(symbols),
			c.cfg.StreamsPerConn,
			len(depthChunks),
			c.cfg.DepthUpdateMs,
		)
		for _, chunk := range depthChunks {
			streams := append([]string(nil), chunk...)
			go func(items []string) {
				reportErr(c.runDepthLoop(ctx, items))
			}(streams)
		}
	}

	ticker := time.NewTicker(time.Duration(c.cfg.LogIntervalSec) * time.Second)
	defer ticker.Stop()

	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			d := time.Since(started).Round(time.Second)
			log.Printf(
				"collector stopping uptime=%s trade_msg=%d trade_bytes=%d depth_msg=%d depth_bytes=%d reconnect=%d trade_send_fail=%d depth_send_fail=%d",
				d,
				c.tradeMsgCount.Load(),
				c.tradeByteCount.Load(),
				c.depthMsgCount.Load(),
				c.depthByteCount.Load(),
				c.wsReconnectCount.Load(),
				c.tradeSendFailCount.Load(),
				c.depthSendFailCount.Load(),
			)
			return nil
		case err := <-errCh:
			if err != nil {
				return err
			}
		case <-ticker.C:
			log.Printf(
				"collector heartbeat trade_msg=%d trade_bytes=%d depth_msg=%d depth_bytes=%d reconnect=%d trade_send_fail=%d depth_send_fail=%d",
				c.tradeMsgCount.Load(),
				c.tradeByteCount.Load(),
				c.depthMsgCount.Load(),
				c.depthByteCount.Load(),
				c.wsReconnectCount.Load(),
				c.tradeSendFailCount.Load(),
				c.depthSendFailCount.Load(),
			)
		}
	}
}

func (c *Collector) runTradeLoop(ctx context.Context, streams []string) error {
	base := strings.TrimSpace(c.cfg.BinanceWSBaseURL)
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("parse ws base url: %w", err)
	}

	q := u.Query()
	q.Set("streams", strings.Join(streams, "/"))
	u.RawQuery = q.Encode()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}

		err := c.consumeTradeConn(ctx, u.String())
		if err == nil || ctx.Err() != nil {
			return nil
		}
		c.wsReconnectCount.Add(1)
		log.Printf("trade ws disconnected: %v, reconnect in %s", err, backoff)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c *Collector) runDepthLoop(ctx context.Context, streams []string) error {
	base := strings.TrimSpace(c.cfg.BinanceWSBaseURL)
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("parse ws base url: %w", err)
	}

	q := u.Query()
	q.Set("streams", strings.Join(streams, "/"))
	u.RawQuery = q.Encode()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}

		err := c.consumeDepthConn(ctx, u.String())
		if err == nil || ctx.Err() != nil {
			return nil
		}
		c.wsReconnectCount.Add(1)
		log.Printf("depth ws disconnected: %v, reconnect in %s", err, backoff)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c *Collector) consumeTradeConn(ctx context.Context, wsURL string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}
	defer conn.Close()

	source := sourceNameForMarket(c.cfg.Market)
	for {
		if ctx.Err() != nil {
			return nil
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read ws: %w", err)
		}

		var env binance.StreamEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}

		if env.Data.Symbol == "" || env.Data.EventType == "" {
			continue
		}

		symbol := strings.ToUpper(env.Data.Symbol)
		if !strings.HasSuffix(symbol, "USDT") {
			continue
		}

		out := map[string]any{
			"market":          c.cfg.Market,
			"symbol":          symbol,
			"event_time_ms":   env.Data.EventTimeMs,
			"trade_time_ms":   env.Data.TradeTimeMs,
			"agg_trade_id":    env.Data.AggTradeID,
			"price":           env.Data.Price,
			"qty":             env.Data.Quantity,
			"is_buyer_maker":  env.Data.IsBuyerMaker,
			"source":          source,
			"collector_ts_ms": time.Now().UnixMilli(),
		}
		b, err := json.Marshal(out)
		if err != nil {
			continue
		}
		if err := c.tradeProducer.Send([]byte(symbol), b); err != nil {
			c.tradeSendFailCount.Add(1)
			log.Printf("trade kafka send failed: %v", err)
			continue
		}
		c.tradeMsgCount.Add(1)
		c.tradeByteCount.Add(int64(len(b)))
	}
}

func (c *Collector) consumeDepthConn(ctx context.Context, wsURL string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}
	defer conn.Close()

	source := sourceDepthNameForMarket(c.cfg.Market)
	for {
		if ctx.Err() != nil {
			return nil
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read ws: %w", err)
		}

		var env binance.DepthStreamEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}

		symbol := strings.ToUpper(strings.TrimSpace(env.Data.Symbol))
		if symbol == "" {
			symbol = streamSymbol(env.Stream)
		}
		if !strings.HasSuffix(symbol, "USDT") {
			continue
		}

		eventTimeMs := env.Data.EventTimeMs
		if eventTimeMs <= 0 {
			eventTimeMs = time.Now().UnixMilli()
		}

		bids := env.Data.Bids
		asks := env.Data.Asks
		if len(bids) == 0 {
			bids = env.Data.BidsAlt
		}
		if len(asks) == 0 {
			asks = env.Data.AsksAlt
		}
		if len(bids) == 0 || len(asks) == 0 {
			continue
		}

		out := map[string]any{
			"market":          c.cfg.Market,
			"symbol":          symbol,
			"event_time_ms":   eventTimeMs,
			"bids":            bids,
			"asks":            asks,
			"source":          source,
			"collector_ts_ms": time.Now().UnixMilli(),
		}
		b, err := json.Marshal(out)
		if err != nil {
			continue
		}
		if err := c.depthProducer.Send([]byte(symbol), b); err != nil {
			c.depthSendFailCount.Add(1)
			log.Printf("depth kafka send failed: %v", err)
			continue
		}
		c.depthMsgCount.Add(1)
		c.depthByteCount.Add(int64(len(b)))
	}
}

func (c *Collector) resolveSymbols(ctx context.Context) ([]string, error) {
	if len(c.cfg.Symbols) > 0 {
		items := dedupeSymbols(c.cfg.Symbols)
		if c.cfg.SymbolLimit > 0 && len(items) > c.cfg.SymbolLimit {
			items = items[:c.cfg.SymbolLimit]
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("COLLECTOR_SYMBOLS is set but no valid symbol")
		}
		log.Printf("collector symbols loaded from env count=%d", len(items))
		return items, nil
	}

	var (
		items []string
		err   error
	)
	if c.cfg.Market == "spot" {
		items, err = binance.FetchSpotUSDTTradingSymbols(ctx, c.cfg.BinanceRESTBase, 15*time.Second)
	} else {
		items, err = binance.FetchSwapUSDTPerpetualSymbols(ctx, c.cfg.BinanceRESTBase, 15*time.Second)
	}
	if err != nil {
		return nil, err
	}

	items = dedupeSymbols(items)
	if c.cfg.SymbolLimit > 0 && len(items) > c.cfg.SymbolLimit {
		items = items[:c.cfg.SymbolLimit]
	}
	log.Printf("collector symbols fetched from exchangeInfo count=%d", len(items))
	return items, nil
}

func sourceNameForMarket(market string) string {
	if strings.EqualFold(market, "spot") {
		return "binance_spot_aggtrade"
	}
	return "binance_futures_aggtrade"
}

func sourceDepthNameForMarket(market string) string {
	if strings.EqualFold(market, "spot") {
		return "binance_spot_depth5"
	}
	return "binance_futures_depth5"
}

func streamSymbol(stream string) string {
	part := strings.TrimSpace(stream)
	if part == "" {
		return ""
	}
	at := strings.Index(part, "@")
	if at > 0 {
		part = part[:at]
	}
	return strings.ToUpper(strings.TrimSpace(part))
}

func buildStreamChunks(symbols []string, chunkSize int, streamSuffix string) [][]string {
	if chunkSize <= 0 {
		chunkSize = 200
	}
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@"+streamSuffix)
	}
	out := make([][]string, 0, (len(streams)+chunkSize-1)/chunkSize)
	for i := 0; i < len(streams); i += chunkSize {
		j := i + chunkSize
		if j > len(streams) {
			j = len(streams)
		}
		out = append(out, streams[i:j])
	}
	return out
}

func dedupeSymbols(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, item := range symbols {
		s := strings.ToUpper(strings.TrimSpace(item))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
