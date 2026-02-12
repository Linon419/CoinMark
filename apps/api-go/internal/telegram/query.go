package telegram

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/model"
	chrepo "coinmark/api-go/internal/repo/ch"
	redisrepo "coinmark/api-go/internal/repo/redis"
	"coinmark/api-go/internal/repo/sqlite"
	"coinmark/api-go/internal/service"
)

type QueryBot struct {
	bot    *tele.Bot
	store  *sqlite.Store
	ch     *chrepo.Client
	bn     *binance.Client
	redis  *redisrepo.Store
	prefix string
}

func NewQueryBot(token string, pollTimeout int, store *sqlite.Store, ch *chrepo.Client, bn *binance.Client, redis *redisrepo.Store, prefix string) (*QueryBot, error) {
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: time.Duration(pollTimeout) * time.Second},
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}
	qb := &QueryBot{bot: b, store: store, ch: ch, bn: bn, redis: redis, prefix: prefix}
	qb.register()
	return qb, nil
}

func (qb *QueryBot) Start() { qb.bot.Start() }
func (qb *QueryBot) Stop()  { qb.bot.Stop() }

func (qb *QueryBot) register() {
	qb.bot.Handle("/start", qb.startCmd)
	qb.bot.Handle("/help", qb.helpCmd)
	qb.bot.Handle("/overview", qb.overviewCmd)
	qb.bot.Handle("/price", qb.priceCmd)
	qb.bot.Handle("/fund", qb.fundCmd)
	qb.bot.Handle("/absorb", qb.absorbCmd)
	qb.bot.Handle("/anomaly", qb.anomalyCmd)
	qb.bot.Handle("/fi1d", qb.flowRankCmd("swap", "in"))
	qb.bot.Handle("/fo1d", qb.flowRankCmd("swap", "out"))
	qb.bot.Handle("/si1d", qb.flowRankCmd("spot", "in"))
	qb.bot.Handle("/so1d", qb.flowRankCmd("spot", "out"))
	qb.bot.Handle("/r15m", qb.returnRankCmd("15m"))
	qb.bot.Handle("/r1h", qb.returnRankCmd("1h"))
	qb.bot.Handle("/oicapratio", qb.oiCapRatioCmd)
	qb.bot.Handle(tele.OnText, qb.textCmd)
}

func (qb *QueryBot) startCmd(c tele.Context) error {
	return c.Send("CoinMark Bot 已启动。\n发送币种名称(如 BTC)查看详情，或输入 /help 查看命令列表。")
}

func (qb *QueryBot) helpCmd(c tele.Context) error {
	help := `/overview - 市场总览
/fi1d - 合约24h净流入TOP
/fo1d - 合约24h净流出TOP
/si1d - 现货24h净流入TOP
/so1d - 现货24h净流出TOP
/r15m [N] - 15m涨跌幅TOP
/r1h [N] - 1h涨跌幅TOP
/oicapratio [N] - OI/市值比TOP
/price SYMBOL - 最新价格
/fund SYMBOL - 资金快照
/absorb SYMBOL - 吸收信号
/anomaly [SYMBOL] - 异常事件
直接输入币种名称查看详情`
	return c.Send(help)
}

func (qb *QueryBot) overviewCmd(c tele.Context) error {
	ctx := context.Background()
	tickers, err := qb.bn.GetTicker24hAll(ctx, "swap")
	if err != nil {
		return c.Send("获取数据失败")
	}
	type symData struct {
		sym string
		pct float64
	}
	var items []symData
	for _, t := range tickers {
		sym, _ := t["symbol"].(string)
		if sym == "" || binance.IsExcludedSymbol(sym) {
			continue
		}
		pct := toF64(t["priceChangePercent"])
		items = append(items, symData{sym, pct})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].pct > items[j].pct })

	var b strings.Builder
	b.WriteString("市场总览 (合约24h)\n\n涨幅TOP:\n")
	for i := 0; i < 10 && i < len(items); i++ {
		b.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, items[i].sym, fmtSignedPct(items[i].pct)))
	}
	b.WriteString("\n跌幅TOP:\n")
	for i := len(items) - 1; i >= 0 && i >= len(items)-10; i-- {
		b.WriteString(fmt.Sprintf("%d. %s %s\n", len(items)-i, items[i].sym, fmtSignedPct(items[i].pct)))
	}
	return c.Send(b.String())
}

func (qb *QueryBot) priceCmd(c tele.Context) error {
	args := strings.Fields(c.Text())
	if len(args) < 2 {
		return c.Send("用法: /price SYMBOL")
	}
	sym := normalizeSymbol(args[1])
	ctx := context.Background()

	ticker, err := qb.bn.GetTicker24h(ctx, "swap", sym)
	if err != nil {
		return c.Send(fmt.Sprintf("%s: 获取价格失败", sym))
	}
	price := toF64(ticker["lastPrice"])
	pct := toF64(ticker["priceChangePercent"])
	qv := toF64(ticker["quoteVolume"])
	return c.Send(fmt.Sprintf("%s\n价格: %s\n24h: %s\n成交额: %s", sym, fmtCompact(price), fmtSignedPct(pct), fmtBigUSD(qv)))
}

func (qb *QueryBot) fundCmd(c tele.Context) error {
	args := strings.Fields(c.Text())
	if len(args) < 2 {
		return c.Send("用法: /fund SYMBOL")
	}
	sym := normalizeSymbol(args[1])
	ctx := context.Background()

	nowMs := time.Now().UnixMilli()
	start1h := nowMs - 60*60*1000
	start24h := nowMs - 24*60*60*1000

	rows1h, _ := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1m", start1h, 0, "asc", 0)
	rows24h, _ := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1h", start24h, 0, "asc", 0)

	net1h := 0.0
	for _, r := range rows1h {
		net1h += r.TakerBuyNotional - r.TakerSellNotional
	}
	net24h := 0.0
	for _, r := range rows24h {
		net24h += r.TakerBuyNotional - r.TakerSellNotional
	}
	return c.Send(fmt.Sprintf("%s 资金快照\n1h净流入: %s\n24h净流入: %s", sym, fmtBigUSD(net1h), fmtBigUSD(net24h)))
}

func (qb *QueryBot) absorbCmd(c tele.Context) error {
	args := strings.Fields(c.Text())
	if len(args) < 2 {
		return c.Send("用法: /absorb SYMBOL")
	}
	sym := normalizeSymbol(args[1])
	ctx := context.Background()

	sigs, _ := service.ListLatestAbsorptionSignals(ctx, qb.store, "swap", false, 10, 4320, "all")
	var matched []model.AbsorptionSignalSnapshot
	for _, s := range sigs {
		if s.Symbol == sym {
			matched = append(matched, s)
		}
	}
	if len(matched) == 0 {
		return c.Send(fmt.Sprintf("%s: 无吸收信号", sym))
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 吸收信号\n", sym))
	for _, s := range matched {
		b.WriteString(fmt.Sprintf("方向:%s 状态:%s 评分:%.1f\n", s.Direction, s.SignalState, s.Score))
	}
	return c.Send(b.String())
}

func (qb *QueryBot) anomalyCmd(c tele.Context) error {
	ctx := context.Background()
	args := strings.Fields(c.Text())
	sym := ""
	if len(args) >= 2 {
		sym = normalizeSymbol(args[1])
	}

	cutoffMs := time.Now().UnixMilli() - 6*60*60*1000
	var rows []model.AnomalyEvent
	if sym != "" {
		qb.store.SelectContext(ctx, &rows, `SELECT * FROM anomaly_events WHERE market = 'swap' AND symbol = ? AND event_time_ms >= ? ORDER BY event_time_ms DESC LIMIT 10`, sym, cutoffMs)
	} else {
		qb.store.SelectContext(ctx, &rows, `SELECT * FROM anomaly_events WHERE market = 'swap' AND event_time_ms >= ? ORDER BY event_time_ms DESC LIMIT 20`, cutoffMs)
	}

	if len(rows) == 0 {
		return c.Send("最近无异常事件")
	}

	loc := time.UTC
	var b strings.Builder
	b.WriteString("最近异常事件:\n")
	for i, r := range rows {
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		b.WriteString(fmt.Sprintf("%d. %s | %s | %s\n", i+1, r.Symbol, eventTypeLabel(r.EventType), fmtTs(r.EventTimeMs, loc)))
	}
	return c.Send(b.String())
}

func (qb *QueryBot) flowRankCmd(market, direction string) tele.HandlerFunc {
	return func(c tele.Context) error {
		ctx := context.Background()
		nowMs := time.Now().UnixMilli()
		startMs := nowMs - 24*60*60*1000
		limit := 15

		// get all symbols first
		tickers, err := qb.bn.GetTicker24hAll(ctx, market)
		if err != nil {
			return c.Send("获取数据失败")
		}
		var symbols []string
		for _, t := range tickers {
			sym, _ := t["symbol"].(string)
			if sym != "" && !binance.IsExcludedSymbol(sym) {
				symbols = append(symbols, sym)
			}
		}

		rows, err := qb.ch.QueryTradeFlowAgg(ctx, market, symbols, "1d", startMs)
		if err != nil {
			return c.Send("获取数据失败")
		}

		type item struct {
			sym string
			net float64
		}
		var items []item
		for _, r := range rows {
			if binance.IsExcludedSymbol(r.Symbol) {
				continue
			}
			net := r.BuySum - r.SellSum
			if direction == "in" && net > 0 {
				items = append(items, item{r.Symbol, net})
			} else if direction == "out" && net < 0 {
				items = append(items, item{r.Symbol, net})
			}
		}

		if direction == "in" {
			sort.Slice(items, func(i, j int) bool { return items[i].net > items[j].net })
		} else {
			sort.Slice(items, func(i, j int) bool { return items[i].net < items[j].net })
		}
		if len(items) > limit {
			items = items[:limit]
		}

		label := "净流入"
		if direction == "out" {
			label = "净流出"
		}
		marketLabel := "合约"
		if market == "spot" {
			marketLabel = "现货"
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s 24h %s TOP:\n", marketLabel, label))
		for i, it := range items {
			b.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, it.sym, fmtBigUSD(it.net)))
		}
		return c.Send(b.String())
	}
}

func (qb *QueryBot) returnRankCmd(bucket string) tele.HandlerFunc {
	return func(c tele.Context) error {
		ctx := context.Background()
		limit := 15
		args := strings.Fields(c.Text())
		if len(args) >= 2 {
			if n, err := fmt.Sscanf(args[1], "%d", &limit); err == nil && n == 1 {
				if limit < 1 {
					limit = 1
				}
				if limit > 60 {
					limit = 60
				}
			}
		}

		bMs := bucketToMs(bucket)
		nowMs := time.Now().UnixMilli()
		bucketEnd := (nowMs / bMs) * bMs
		bucketStart := bucketEnd - bMs

		rows, err := qb.ch.QueryTradeBuckets(ctx, "swap", "", nil, bucket, bucketStart, bucketEnd, "asc", 0)
		if err != nil {
			return c.Send("获取数据失败")
		}

		type item struct {
			sym string
			ret float64
		}
		var items []item
		for _, r := range rows {
			if binance.IsExcludedSymbol(r.Symbol) || r.OpenPrice == nil || r.ClosePrice == nil || *r.OpenPrice <= 0 {
				continue
			}
			ret := (*r.ClosePrice / *r.OpenPrice - 1) * 100
			items = append(items, item{r.Symbol, ret})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ret > items[j].ret })

		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s 涨幅TOP:\n", bucket))
		top := items
		if len(top) > limit {
			top = top[:limit]
		}
		for i, it := range top {
			b.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, it.sym, fmtSignedPct(it.ret)))
		}

		b.WriteString(fmt.Sprintf("\n%s 跌幅TOP:\n", bucket))
		sort.Slice(items, func(i, j int) bool { return items[i].ret < items[j].ret })
		bot := items
		if len(bot) > limit {
			bot = bot[:limit]
		}
		for i, it := range bot {
			b.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, it.sym, fmtSignedPct(it.ret)))
		}
		return c.Send(b.String())
	}
}

func (qb *QueryBot) oiCapRatioCmd(c tele.Context) error {
	ctx := context.Background()
	limit := 15
	items, err := service.GetOIMarketCapRank(ctx, qb.ch, limit)
	if err != nil {
		return c.Send("获取数据失败")
	}
	var b strings.Builder
	b.WriteString("OI/市值 TOP:\n")
	for i, it := range items {
		b.WriteString(fmt.Sprintf("%d. %s ratio:%.4f oi:%s mcap:%s\n",
			i+1, it.Symbol, it.Ratio, fmtBigUSD(it.OINotionalUSD), fmtBigUSD(it.MarketCapUSD)))
	}
	return c.Send(b.String())
}

func (qb *QueryBot) textCmd(c tele.Context) error {
	text := strings.TrimSpace(c.Text())
	if text == "" || strings.HasPrefix(text, "/") {
		return nil
	}
	sym := normalizeSymbol(text)
	if sym == "" {
		return nil
	}
	ctx := context.Background()

	ticker, err := qb.bn.GetTicker24h(ctx, "swap", sym)
	if err != nil {
		return c.Send(fmt.Sprintf("%s: 未找到该币种", sym))
	}

	price := toF64(ticker["lastPrice"])
	pct24h := toF64(ticker["priceChangePercent"])
	qv := toF64(ticker["quoteVolume"])

	nowMs := time.Now().UnixMilli()
	start1h := nowMs - 60*60*1000
	rows1h, _ := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1m", start1h, 0, "asc", 0)
	net1h := 0.0
	for _, r := range rows1h {
		net1h += r.TakerBuyNotional - r.TakerSellNotional
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s\n", sym))
	sb.WriteString(fmt.Sprintf("价格: %s  24h: %s\n", fmtCompact(price), fmtSignedPct(pct24h)))
	sb.WriteString(fmt.Sprintf("24h成交额: %s\n", fmtBigUSD(qv)))
	sb.WriteString(fmt.Sprintf("1h净流入: %s\n", fmtBigUSD(net1h)))

	sigs, _ := service.ListLatestAbsorptionSignals(ctx, qb.store, "swap", true, 5, 4320, "all")
	for _, s := range sigs {
		if s.Symbol == sym && s.SignalState != "NONE" {
			sb.WriteString(fmt.Sprintf("吸收: %s %s %.1f分\n", s.Direction, s.SignalState, s.Score))
		}
	}

	return c.Send(sb.String())
}

func toF64(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f := 0.0
		fmt.Sscanf(t, "%f", &f)
		return f
	default:
		return 0
	}
}

func bucketToMs(b string) int64 {
	switch b {
	case "15m":
		return 15 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	default:
		return 60 * 60 * 1000
	}
}

var _ = math.Round
