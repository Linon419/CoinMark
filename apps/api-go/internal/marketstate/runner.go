package marketstate

import (
	"context"
	"fmt"
	"log"
	"time"

	"coinmark/api-go/internal/model"
	"github.com/nats-io/nats.go"
)

// Loader 读取某个市场 [fromMs, toMs] 内所有币的 1m 桶（ClickHouse）。
type Loader func(ctx context.Context, market string, fromMs, toMs int64) ([]model.CHTradeRow, error)

type Config struct {
	NATSURL string
	Stream  string
	Subject string
	Markets []string
}

// Run 加载切点之前的历史，再从切点前 1 分钟开始重放 NATS 成交，直到 ctx 结束。
// 切点取上一分钟的开始：在它之前的 1m 桶早已由 ingest 落库；之后的成交全部来自 NATS。
func (s *State) Run(ctx context.Context, cfg Config, load Loader) error {
	now := time.Now().UnixMilli()
	cut := (now/minuteMs)*minuteMs - minuteMs
	from := cut - int64(s.ringSize)*minuteMs
	for _, m := range cfg.Markets {
		rows, err := load(ctx, m, from, cut-1)
		if err != nil {
			return fmt.Errorf("marketstate load %s: %w", m, err)
		}
		n := s.loadRows(m, rows)
		log.Printf("marketstate: loaded market=%s bars=%d", m, n)
	}
	s.SetCut(cut)

	nc, err := nats.Connect(cfg.NATSURL, nats.Name("coinmark-api-marketstate"), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("marketstate nats connect: %w", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("marketstate jetstream: %w", err)
	}
	// 有序临时消费者：不需要 ack、不会重复投递，断线后自动从断点重建
	sub, err := js.Subscribe(cfg.Subject, func(msg *nats.Msg) {
		if t, ok := parseTrade(msg.Data); ok {
			s.Apply(t)
		}
		s.Touch(time.Now().UnixMilli())
	}, nats.BindStream(cfg.Stream), nats.OrderedConsumer(), nats.StartTime(time.UnixMilli(cut-minuteMs)))
	if err != nil {
		return fmt.Errorf("marketstate subscribe: %w", err)
	}
	defer sub.Unsubscribe()
	s.MarkLoaded()
	log.Printf("marketstate: live cut=%d subject=%s", cut, cfg.Subject)
	<-ctx.Done()
	return nil
}

func (s *State) loadRows(market string, rows []model.CHTradeRow) int {
	n := 0
	for _, r := range rows {
		if r.OpenPrice == nil || r.HighPrice == nil || r.LowPrice == nil || r.ClosePrice == nil || r.TradeCount <= 0 {
			continue
		}
		s.LoadBar(market, r.Symbol, Bar{
			StartMs: r.BucketStartMs, Open: *r.OpenPrice, High: *r.HighPrice, Low: *r.LowPrice, Close: *r.ClosePrice,
			BuyNotional: r.TakerBuyNotional, SellNotional: r.TakerSellNotional, QuoteNotional: r.QuoteNotional, Trades: r.TradeCount,
		})
		n++
	}
	return n
}
