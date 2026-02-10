package natsconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"coinmark/ingest-go/internal/config"
	"coinmark/ingest-go/internal/ingest"
	"coinmark/ingest-go/internal/runtime"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
)

type Consumer struct {
	cfg   *config.Config
	stats *runtime.Stats
}

func New(cfg *config.Config, stats *runtime.Stats) *Consumer {
	return &Consumer{cfg: cfg, stats: stats}
}

type tradePayload struct {
	Market       string `json:"market"`
	Symbol       string `json:"symbol"`
	TradeTimeMS  int64  `json:"trade_time_ms"`
	EventTimeMS  int64  `json:"event_time_ms"`
	Price        any    `json:"price"`
	Qty          any    `json:"qty"`
	IsBuyerMaker bool   `json:"is_buyer_maker"`
}

type depthPayload struct {
	Market      string          `json:"market"`
	Symbol      string          `json:"symbol"`
	EventTimeMS int64           `json:"event_time_ms"`
	Bids        [][]interface{} `json:"bids"`
	Asks        [][]interface{} `json:"asks"`
}

func parseDecimal(v any) (decimal.Decimal, error) {
	switch t := v.(type) {
	case string:
		return decimal.NewFromString(t)
	case float64:
		return decimal.NewFromString(strconv.FormatFloat(t, 'f', -1, 64))
	case int64:
		return decimal.NewFromInt(t), nil
	case int:
		return decimal.NewFromInt(int64(t)), nil
	default:
		return decimal.Zero, fmt.Errorf("unsupported decimal type")
	}
}

func (c *Consumer) consumeTrade(ctx context.Context, market string, nc *nats.Conn, tradeAgg *ingest.TradeAggregator, obAgg *ingest.OrderbookAggregator) error {
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	consumerName := fmt.Sprintf("%s-%s-trade", c.cfg.NATSConsumerPrefix, market)
	if c.cfg.NATSURL == "" || c.cfg.NATSStreamRaw == "" || c.cfg.NATSSubjectTrade == "" {
		return fmt.Errorf("nats trade config empty")
	}

	sub, err := js.PullSubscribe(
		c.cfg.NATSSubjectTrade,
		consumerName,
		nats.BindStream(c.cfg.NATSStreamRaw),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(10),
	)
	if err != nil {
		return err
	}
	log.Printf("TradeNATS(%s) started url=%s stream=%s subject=%s consumer=%s", market, c.cfg.NATSURL, c.cfg.NATSStreamRaw, c.cfg.NATSSubjectTrade, consumerName)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		msgs, err := sub.Fetch(200, nats.MaxWait(1*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			return err
		}
		for _, msg := range msgs {
			func() {
				defer func() {
					_ = msg.Ack()
				}()

				var p tradePayload
				if err := json.Unmarshal(msg.Data, &p); err != nil {
					return
				}
				if strings.ToLower(p.Market) != market {
					return
				}
				symbol := strings.ToUpper(strings.TrimSpace(p.Symbol))
				if symbol == "" {
					return
				}
				tsMS := p.TradeTimeMS
				if tsMS <= 0 {
					tsMS = p.EventTimeMS
				}
				if tsMS <= 0 {
					return
				}
				price, err := parseDecimal(p.Price)
				if err != nil || !price.GreaterThan(decimal.Zero) {
					return
				}
				qty, err := parseDecimal(p.Qty)
				if err != nil || !qty.GreaterThan(decimal.Zero) {
					return
				}
				notional := price.Mul(qty)
				takerBuyNotional := notional
				takerSellNotional := decimal.Zero
				if p.IsBuyerMaker {
					takerBuyNotional = decimal.Zero
					takerSellNotional = notional
				}

				tradeAgg.AddTrade(market, symbol, tsMS, price, takerBuyNotional, takerSellNotional, notional)
				obAgg.AddTrade(market, symbol, tsMS, takerBuyNotional, takerSellNotional)
				c.stats.NATSTradeMsg.Add(1)
			}()
		}
	}
}

func parseDepthLevels(in [][]interface{}) []ingest.DepthLevel {
	out := make([]ingest.DepthLevel, 0, len(in))
	for _, row := range in {
		if len(row) < 2 {
			continue
		}
		p, errP := parseDecimal(row[0])
		q, errQ := parseDecimal(row[1])
		if errP != nil || errQ != nil {
			continue
		}
		out = append(out, ingest.DepthLevel{Price: p, Qty: q})
	}
	return out
}

func (c *Consumer) consumeDepth(ctx context.Context, market string, nc *nats.Conn, obAgg *ingest.OrderbookAggregator) error {
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	consumerName := fmt.Sprintf("%s-%s-depth", c.cfg.NATSConsumerPrefix, market)
	if c.cfg.NATSURL == "" || c.cfg.NATSStreamRaw == "" || c.cfg.NATSSubjectDepth == "" {
		return fmt.Errorf("nats depth config empty")
	}

	sub, err := js.PullSubscribe(
		c.cfg.NATSSubjectDepth,
		consumerName,
		nats.BindStream(c.cfg.NATSStreamRaw),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(10),
	)
	if err != nil {
		return err
	}
	log.Printf("DepthNATS(%s) started url=%s stream=%s subject=%s consumer=%s", market, c.cfg.NATSURL, c.cfg.NATSStreamRaw, c.cfg.NATSSubjectDepth, consumerName)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		msgs, err := sub.Fetch(200, nats.MaxWait(1*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			return err
		}
		for _, msg := range msgs {
			func() {
				defer func() {
					_ = msg.Ack()
				}()
				var p depthPayload
				if err := json.Unmarshal(msg.Data, &p); err != nil {
					return
				}
				if strings.ToLower(p.Market) != market {
					return
				}
				symbol := strings.ToUpper(strings.TrimSpace(p.Symbol))
				if symbol == "" || p.EventTimeMS <= 0 {
					return
				}
				bids := parseDepthLevels(p.Bids)
				asks := parseDepthLevels(p.Asks)
				f, ok := ingest.BuildDepthFeatures(bids, asks)
				if !ok {
					return
				}
				obAgg.AddOrderbookSample(market, symbol, p.EventTimeMS, f.SpreadBPS, f.MicropriceShiftBPS, f.L1DepthNotional, f.DepthImbalanceL20, f.WallPressureL20)
				c.stats.NATSDepthMsg.Add(1)
			}()
		}
	}
}

func (c *Consumer) RunMarket(ctx context.Context, market string, tradeAgg *ingest.TradeAggregator, obAgg *ingest.OrderbookAggregator) error {
	ncTrade, err := nats.Connect(c.cfg.NATSURL, nats.Name(fmt.Sprintf("coinmark-ingest-go-%s-trade", market)))
	if err != nil {
		return err
	}
	defer ncTrade.Close()

	ncDepth, err := nats.Connect(c.cfg.NATSURL, nats.Name(fmt.Sprintf("coinmark-ingest-go-%s-depth", market)))
	if err != nil {
		return err
	}
	defer ncDepth.Close()

	errCh := make(chan error, 2)
	go func() { errCh <- c.consumeTrade(ctx, market, ncTrade, tradeAgg, obAgg) }()
	if c.cfg.IngestEnableDepth {
		go func() { errCh <- c.consumeDepth(ctx, market, ncDepth, obAgg) }()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}
