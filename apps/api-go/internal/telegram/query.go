package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	tzMu   sync.RWMutex
	tzMap  map[int64]string
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
	qb := &QueryBot{bot: b, store: store, ch: ch, bn: bn, redis: redis, prefix: prefix, tzMap: map[int64]string{}}
	qb.register()
	if err := qb.initMenu(); err != nil {
		log.Printf("tg query: menu init failed: %v", err)
	}
	return qb, nil
}

func (qb *QueryBot) Start() { qb.bot.Start() }
func (qb *QueryBot) Stop()  { qb.bot.Stop() }

func (qb *QueryBot) requireCH(c tele.Context) bool {
	if qb.ch != nil {
		return true
	}
	_ = c.Send("ClickHouse 未配置，命令暂不可用")
	return false
}

func (qb *QueryBot) register() {
	qb.bot.Handle("/start", qb.startCmd)
	qb.bot.Handle("/help", qb.helpCmd)
	qb.bot.Handle("/menu", qb.menuCmd)
	qb.bot.Handle("/overview", qb.overviewCmd)
	qb.bot.Handle("/bullindex", qb.bullIndexCmd)
	qb.bot.Handle("/openinterest", qb.openInterestCmd)
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
	qb.bot.Handle("/nch", qb.nchCmd)
	qb.bot.Handle("/ncd", qb.ncdCmd)
	qb.bot.Handle("/oih", qb.oihCmd)
	qb.bot.Handle("/oid", qb.oidCmd)
	qb.bot.Handle("/tz", qb.tzCmd)
	qb.bot.Handle("/hotmarket", qb.stubCmd("/hotmarket"))
	qb.bot.Handle("/suit", qb.stubCmd("/suit"))
	qb.bot.Handle("/fundrate", qb.stubCmd("/fundrate"))
	qb.bot.Handle("/showtime", qb.stubCmd("/showtime"))
	qb.bot.Handle("/awake", qb.stubCmd("/awake"))
	qb.bot.Handle("/myfav", qb.stubCmd("/myfav"))
	qb.bot.Handle("/settings", qb.stubCmd("/settings"))
	qb.bot.Handle(tele.OnText, qb.textCmd)
}

func (qb *QueryBot) startCmd(c tele.Context) error {
	return qb.sendMenu(c, "你好，我是 CoinMark 查询 Bot。")
}

func (qb *QueryBot) helpCmd(c tele.Context) error {
	return qb.sendMenu(c, "可用命令如下：")
}

func (qb *QueryBot) menuCmd(c tele.Context) error {
	return qb.sendMenu(c, "菜单：")
}

func (qb *QueryBot) initMenu() error {
	cmds := qb.queryMenuCommands()
	if err := qb.bot.SetCommands(cmds, tele.CommandScope{Type: tele.CommandScopeAllPrivateChats}); err != nil {
		return err
	}
	_, err := qb.bot.Raw("setChatMenuButton", map[string]interface{}{
		"menu_button": tele.MenuButton{Type: tele.MenuButtonCommands},
	})
	return err
}

func (qb *QueryBot) queryMenuCommands() []tele.Command {
	return []tele.Command{
		{Text: "menu", Description: "打开菜单"},
		{Text: "overview", Description: "市场概览"},
		{Text: "fi1d", Description: "合约当日净流入(最大30)"},
		{Text: "fo1d", Description: "合约24h净流出"},
		{Text: "si1d", Description: "现货当日净流入(最大30)"},
		{Text: "so1d", Description: "现货24h净流出"},
		{Text: "r15m", Description: "近15分钟涨跌幅(1-60)"},
		{Text: "r1h", Description: "近1小时涨跌幅(1-120)"},
		{Text: "bullindex", Description: "多头指数排行"},
		{Text: "openinterest", Description: "持仓增幅排行"},
		{Text: "oicapratio", Description: "持仓与市值比例排行"},
		{Text: "hotmarket", Description: "近期多空热点"},
		{Text: "suit", Description: "多空量能排行"},
		{Text: "fundrate", Description: "资金费排行"},
		{Text: "showtime", Description: "波动次数排行"},
		{Text: "awake", Description: "冬眠苏醒"},
		{Text: "myfav", Description: "收藏"},
		{Text: "settings", Description: "黑名单"},
		{Text: "nch", Description: "资金盘间快照(示例:/nchBTC)"},
		{Text: "ncd", Description: "资金每日流向(示例:/ncdBTC)"},
		{Text: "oih", Description: "持仓盘间快照(示例:/oihBTC)"},
		{Text: "oid", Description: "持仓近日变化(示例:/oidBTC)"},
		{Text: "tz", Description: "设置时区 /tz set Australia/Sydney"},
		{Text: "price", Description: "兼容: 价格快照"},
		{Text: "fund", Description: "兼容: 资金快照"},
		{Text: "absorb", Description: "兼容: 吸收信号"},
		{Text: "anomaly", Description: "兼容: 异常事件"},
		{Text: "help", Description: "使用帮助"},
	}
}

func (qb *QueryBot) queryMenuMarkup() *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{ResizeKeyboard: true}
	menu.Reply(
		menu.Row(menu.Text("/overview"), menu.Text("/fi1d"), menu.Text("/fo1d")),
		menu.Row(menu.Text("/si1d"), menu.Text("/so1d")),
		menu.Row(menu.Text("/r15m 20"), menu.Text("/r1h 30"), menu.Text("/oicapratio 30")),
		menu.Row(menu.Text("/price BTC"), menu.Text("/fund BTC"), menu.Text("/absorb BTC")),
		menu.Row(menu.Text("/anomaly BTC"), menu.Text("/help")),
	)
	return menu
}

func (qb *QueryBot) sendMenu(c tele.Context, title string) error {
	help := strings.Join([]string{
		title,
		"",
		"/overview - 市场概览",
		"/fi1d /fo1d /si1d /so1d - 24h 资金流向",
		"/r15m 20 /r1h 30 - 近15m/1h涨跌榜",
		"/bullindex 30 /openinterest 30 - 排名",
		"/oicapratio 30 - 持仓/市值比",
		"/hotmarket /suit /fundrate /showtime /awake /myfav /settings - 预留命令",
		"/nchBTC /ncdBTC /oihBTC /oidBTC - 扩展命令",
		"/tz - 查看时区，/tz set Australia/Sydney - 设置时区",
		"兼容: /price /fund /absorb /anomaly",
		"直接输入币种（如 BTC）可看详情。",
	}, "\n")

	chat := c.Chat()
	if chat != nil && chat.Type == tele.ChatPrivate {
		return c.Send(help, &tele.SendOptions{ReplyMarkup: qb.queryMenuMarkup()})
	}
	return c.Send(help)
}

func (qb *QueryBot) stubCmd(name string) tele.HandlerFunc {
	return func(c tele.Context) error {
		if name == "/hotmarket" {
			return c.Send(fmt.Sprintf("%s 功能正在接入中，先用 /overview", name))
		}
		return c.Send(fmt.Sprintf("%s 功能正在接入中", name))
	}
}

func parseLimitArg(args []string, defaultValue, minValue, maxValue int) (int, bool) {
	if len(args) == 0 {
		return defaultValue, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || n < minValue || n > maxValue {
		return 0, false
	}
	return n, true
}

func (qb *QueryBot) bullIndexCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	limit, ok := parseLimitArg(c.Args(), 30, 1, 120)
	if !ok {
		return c.Send("用法: /bullindex 30 (范围 1-120)")
	}
	ts, items := qb.bullIndexRank(context.Background(), limit)
	if len(items) == 0 {
		return c.Send("/bullindex 暂无数据")
	}
	loc := qb.userLoc(c)
	lines := []string{fmt.Sprintf("多头指数前%d (%s)", limit, fmtTs(ts, loc))}
	for i, it := range items {
		lines = append(lines, fmt.Sprintf("%d.%s %.1f分 | %s | %s", i+1, stripUSDT(it.Symbol), it.Score, fmtSignedPct(it.RetPct), fmtSignedPct(it.FlowBiasPct)))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) openInterestCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	limit, ok := parseLimitArg(c.Args(), 30, 1, 120)
	if !ok {
		return c.Send("用法: /openinterest 30 (范围 1-120)")
	}
	items := qb.openInterestGrowthRank(context.Background(), limit)
	if len(items) == 0 {
		return c.Send("/openinterest 暂无数据")
	}
	lines := []string{fmt.Sprintf("持仓增幅前%d (1d)", limit)}
	for i, it := range items {
		lines = append(lines, fmt.Sprintf("%d.%s %s | %sU", i+1, stripUSDT(it.Symbol), fmtSignedPct(it.ChangePct), fmtBigUSD(it.OINotionalUSD)))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) tzCmd(c tele.Context) error {
	userID := qb.userID(c)
	tz := qb.getUserTZ(context.Background(), userID)
	args := c.Args()
	if len(args) == 0 {
		return c.Send(fmt.Sprintf("当前时区: %s\n设置示例: /tz set Australia/Sydney\n也支持: /tz set UTC 或 /tz set Asia/Shanghai", tz))
	}
	if strings.ToLower(args[0]) != "set" || len(args) < 2 {
		return c.Send("用法: /tz 或 /tz set Australia/Sydney")
	}
	target := normalizeTZName(strings.Join(args[1:], " "))
	if target == "" {
		return c.Send("时区无效，请使用 IANA 名称，例如:\nAustralia/Sydney\nAsia/Shanghai\nUTC")
	}
	qb.setUserTZ(context.Background(), userID, target)
	return c.Send(fmt.Sprintf("已设置时区: %s\n当前本地时间: %s", target, fmtTs(time.Now().UnixMilli(), qb.locOrUTC(target))))
}

func (qb *QueryBot) overviewCmd(c tele.Context) error {
	ctx := context.Background()
	tickers, err := qb.bn.GetTicker24hAll(ctx, "swap")
	if err != nil {
		return c.Send("获取数据失败")
	}
	validPairs := map[string]struct{}{}
	if pairs, err := qb.bn.GetPairs(ctx, "swap"); err == nil {
		for _, s := range pairs {
			validPairs[s] = struct{}{}
		}
	}

	type tickerItem struct {
		symbol      string
		pct         float64
		quoteVolume float64
	}
	items := make([]tickerItem, 0, len(tickers))
	for _, t := range tickers {
		sym, _ := t["symbol"].(string)
		if sym == "" || !strings.HasSuffix(sym, "USDT") || binance.IsExcludedSymbol(sym) {
			continue
		}
		if len(validPairs) > 0 {
			if _, ok := validPairs[sym]; !ok {
				continue
			}
		}
		pct := toF64(t["priceChangePercent"])
		qv := toF64(t["quoteVolume"])
		items = append(items, tickerItem{symbol: sym, pct: pct, quoteVolume: qv})
	}

	gainersCount := 0
	losersCount := 0
	for _, it := range items {
		if it.pct > 0 {
			gainersCount++
		}
		if it.pct < 0 {
			losersCount++
		}
	}

	activeRank := append([]tickerItem(nil), items...)
	sort.Slice(activeRank, func(i, j int) bool { return activeRank[i].quoteVolume > activeRank[j].quoteVolume })
	topActive := activeRank
	if len(topActive) > 10 {
		topActive = append([]tickerItem(nil), topActive[:10]...)
	}

	topGainers := append([]tickerItem(nil), items...)
	sort.Slice(topGainers, func(i, j int) bool { return topGainers[i].pct > topGainers[j].pct })
	if len(topGainers) > 10 {
		topGainers = topGainers[:10]
	}

	topLosers := append([]tickerItem(nil), items...)
	sort.Slice(topLosers, func(i, j int) bool { return topLosers[i].pct < topLosers[j].pct })
	if len(topLosers) > 10 {
		topLosers = topLosers[:10]
	}

	majorSymbols := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	swapNetMap := qb.oneDayNetMap(ctx, "swap", majorSymbols)
	spotNetMap := qb.oneDayNetMap(ctx, "spot", majorSymbols)

	oiCandidates := make([]string, 0, 40)
	for i := 0; i < len(activeRank) && i < 40; i++ {
		oiCandidates = append(oiCandidates, activeRank[i].symbol)
	}
	oiGrowthTop := qb.overviewTopOIGrowth(ctx, oiCandidates, 10)

	fmtSymbolItems := func(tickers []tickerItem, limit int) string {
		if len(tickers) == 0 {
			return "-"
		}
		parts := make([]string, 0, limit)
		for i := 0; i < len(tickers) && i < limit; i++ {
			parts = append(parts, stripUSDT(tickers[i].symbol))
		}
		return strings.Join(parts, ",")
	}
	fmtPctItems := func(tickers []tickerItem, limit int) string {
		if len(tickers) == 0 {
			return "-"
		}
		parts := make([]string, 0, limit)
		for i := 0; i < len(tickers) && i < limit; i++ {
			parts = append(parts, fmt.Sprintf("%s(%.2f%%)", stripUSDT(tickers[i].symbol), tickers[i].pct))
		}
		return strings.Join(parts, ",")
	}
	fmtOIGrowthItems := func(items []overviewOIGrowthItem, limit int) string {
		if len(items) == 0 {
			return "-"
		}
		parts := make([]string, 0, limit)
		for i := 0; i < len(items) && i < limit; i++ {
			parts = append(parts, fmt.Sprintf("%s(%.2f%%)", stripUSDT(items[i].Symbol), items[i].Pct))
		}
		return strings.Join(parts, ",")
	}

	lines := []string{
		fmt.Sprintf("市场概览 (%d涨,%d跌)", gainersCount, losersCount),
		"",
		"#情绪指数 | VIX | DXY | US10Y",
		"- | - | - | -",
		"#大盘净资金",
		fmt.Sprintf("BTC %s | %s", fmtCompact(swapNetMap["BTCUSDT"]), fmtCompact(spotNetMap["BTCUSDT"])),
		fmt.Sprintf("ETH %s | %s", fmtCompact(swapNetMap["ETHUSDT"]), fmtCompact(spotNetMap["ETHUSDT"])),
		fmt.Sprintf("SOL %s | %s", fmtCompact(swapNetMap["SOLUSDT"]), fmtCompact(spotNetMap["SOLUSDT"])),
		"#今日最活跃前10",
		fmtSymbolItems(topActive, 10),
		"#今日持仓增幅前10",
		fmtOIGrowthItems(oiGrowthTop, 10),
		"#今日涨幅前10",
		fmtPctItems(topGainers, 10),
		"#今日跌幅前10",
		fmtPctItems(topLosers, 10),
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) priceCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	args := c.Args()
	if len(args) < 1 {
		return c.Send("用法: /price BTCUSDT")
	}
	sym := normalizeSymbol(args[0])
	ctx := context.Background()

	nowMs := time.Now().UnixMilli()
	rows, err := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1m", nowMs-5*60*1000, 0, "desc", 1)
	if err != nil || len(rows) == 0 {
		return c.Send(fmt.Sprintf("没找到 %s 的最新价格数据", sym))
	}
	row := rows[0]
	loc := qb.userLoc(c)
	closePrice := math.NaN()
	if row.ClosePrice != nil {
		closePrice = *row.ClosePrice
	}
	netFlow := row.TakerBuyNotional - row.TakerSellNotional
	return c.Send(
		fmt.Sprintf("【价格】%s\n最新价：%s\n1m净流：%s\n成交额：%s\n时间：%s",
			sym, fmtCompact(closePrice), fmtCompact(netFlow), fmtCompact(row.QuoteNotional), fmtTs(row.BucketStartMs, loc)),
	)
}

func (qb *QueryBot) fundCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	args := c.Args()
	if len(args) < 1 {
		return c.Send("用法: /fund BTCUSDT")
	}
	sym := normalizeSymbol(args[0])
	ctx := context.Background()

	nowMs := time.Now().UnixMilli()
	rows, err := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1h", nowMs-25*60*60*1000, 0, "desc", 24)
	if err != nil || len(rows) == 0 {
		return c.Send(fmt.Sprintf("没找到 %s 的资金快照", sym))
	}
	latest := rows[0]
	net1h := latest.TakerBuyNotional - latest.TakerSellNotional
	net24h := 0.0
	for _, r := range rows {
		net24h += r.TakerBuyNotional - r.TakerSellNotional
	}
	loc := qb.userLoc(c)
	return c.Send(
		fmt.Sprintf("【资金】%s\n近1h净流：%s\n近24h累计：%s\n时间：%s",
			sym, fmtCompact(net1h), fmtCompact(net24h), fmtTs(latest.BucketStartMs, loc)),
	)
}

func (qb *QueryBot) absorbCmd(c tele.Context) error {
	args := c.Args()
	if len(args) < 1 {
		return c.Send("用法: /absorb BTCUSDT")
	}
	sym := normalizeSymbol(args[0])
	ctx := context.Background()

	var rows []model.AbsorptionSignalSnapshot
	_ = qb.store.SelectContext(ctx, &rows,
		`SELECT * FROM absorption_signal_snapshots
WHERE market = 'swap' AND symbol = ? AND direction = 'LONG_BIAS'
ORDER BY bucket_start_ms DESC LIMIT 1`, sym)
	if len(rows) == 0 {
		return c.Send(fmt.Sprintf("没找到 %s 的吸筹信号", sym))
	}
	row := rows[0]
	reasonsText := "-"
	var reasonsRaw []interface{}
	if err := json.Unmarshal(row.Reasons, &reasonsRaw); err == nil && len(reasonsRaw) > 0 {
		parts := make([]string, 0, 3)
		for i := 0; i < len(reasonsRaw) && i < 3; i++ {
			parts = append(parts, fmt.Sprint(reasonsRaw[i]))
		}
		if len(parts) > 0 {
			reasonsText = strings.Join(parts, "；")
		}
	}
	loc := qb.userLoc(c)
	return c.Send(
		fmt.Sprintf("【吸筹】%s\n状态：%s\n分数：%d\n依据：%s\n时间：%s",
			sym, row.SignalState, int(math.Round(row.Score)), reasonsText, fmtTs(row.BucketStartMs, loc)),
	)
}

func (qb *QueryBot) anomalyCmd(c tele.Context) error {
	ctx := context.Background()
	args := c.Args()
	sym := ""
	if len(args) >= 1 {
		sym = normalizeSymbol(args[0])
	}

	var rows []model.AnomalyEvent
	if sym != "" {
		_ = qb.store.SelectContext(ctx, &rows, `SELECT * FROM anomaly_events
WHERE market = 'swap' AND symbol = ? ORDER BY event_time_ms DESC LIMIT 5`, sym)
	} else {
		_ = qb.store.SelectContext(ctx, &rows, `SELECT * FROM anomaly_events
WHERE market = 'swap' ORDER BY event_time_ms DESC LIMIT 5`)
	}

	if len(rows) == 0 {
		return c.Send("最近没有异动事件")
	}

	loc := qb.userLoc(c)
	header := "SWAP"
	if sym != "" {
		header = sym
	}
	lines := []string{fmt.Sprintf("【异动】%s 最近5条", header)}
	for _, r := range rows {
		if binance.IsExcludedSymbol(r.Symbol) {
			continue
		}
		var details map[string]interface{}
		_ = json.Unmarshal(r.Details, &details)
		score := eventSeverityScore(r.EventType, details)
		lines = append(lines,
			fmt.Sprintf("- %s %s | %s %.1f | %s",
				r.Symbol, eventTypeLabel(r.EventType), eventLevel(score), score, fmtTs(r.EventTimeMs, loc)),
		)
	}
	return c.Send(strings.Join(lines, "\n"))
}

type overviewOIGrowthItem struct {
	Symbol string
	Pct    float64
}

func (qb *QueryBot) oneDayNetMap(ctx context.Context, market string, symbols []string) map[string]float64 {
	out := make(map[string]float64, len(symbols))
	if len(symbols) == 0 || qb.ch == nil {
		return out
	}
	rows, err := qb.ch.QueryTradeFlowAgg(ctx, market, symbols, "1m", time.Now().UnixMilli()-24*60*60*1000)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.Symbol] = r.BuySum - r.SellSum
	}
	return out
}

func (qb *QueryBot) overviewTopOIGrowth(ctx context.Context, symbols []string, limit int) []overviewOIGrowthItem {
	if len(symbols) == 0 || limit <= 0 {
		return nil
	}
	type result struct {
		item overviewOIGrowthItem
		ok   bool
	}
	sem := make(chan struct{}, 8)
	outCh := make(chan result, len(symbols))
	var wg sync.WaitGroup
	for _, symbol := range symbols {
		s := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hist, err := qb.bn.GetOpenInterestHist(ctx, s, "1d", 2)
			if err != nil || len(hist) < 2 {
				outCh <- result{ok: false}
				return
			}
			sort.Slice(hist, func(i, j int) bool {
				return toI64(hist[i]["timestamp"]) < toI64(hist[j]["timestamp"])
			})
			prev := toF64(hist[len(hist)-2]["sumOpenInterestValue"])
			curr := toF64(hist[len(hist)-1]["sumOpenInterestValue"])
			if prev <= 0 || curr <= 0 {
				outCh <- result{ok: false}
				return
			}
			pct := (curr - prev) / prev * 100
			if pct <= 0 {
				outCh <- result{ok: false}
				return
			}
			outCh <- result{item: overviewOIGrowthItem{Symbol: s, Pct: pct}, ok: true}
		}()
	}
	wg.Wait()
	close(outCh)

	items := make([]overviewOIGrowthItem, 0, len(symbols))
	for r := range outCh {
		if r.ok {
			items = append(items, r.item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Pct > items[j].Pct })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (qb *QueryBot) flowRankCmd(market, direction string) tele.HandlerFunc {
	return func(c tele.Context) error {
		if !qb.requireCH(c) {
			return nil
		}
		ctx := context.Background()
		nowMs := time.Now().UnixMilli()
		startMs := nowMs - 24*60*60*1000
		limit := 30

		tickers, err := qb.bn.GetTicker24hAll(ctx, market)
		if err != nil {
			return c.Send("获取数据失败")
		}
		symbols := make([]string, 0, len(tickers))
		for _, t := range tickers {
			sym, _ := t["symbol"].(string)
			if sym == "" || binance.IsExcludedSymbol(sym) {
				continue
			}
			symbols = append(symbols, sym)
		}

		rows, err := qb.ch.QueryTradeFlowAgg(ctx, market, symbols, "1m", startMs)
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
		totalCount := len(items)
		label := "净流入"
		if direction == "out" {
			label = "净流出"
		}
		marketLabel := "合约"
		if market == "spot" {
			marketLabel = "现货"
		}

		lines := []string{fmt.Sprintf("%d个%s1日%s排行", totalCount, marketLabel, label), ""}
		if len(items) > limit {
			items = items[:limit]
		}
		for i, it := range items {
			lines = append(lines, fmt.Sprintf("%d.%s %s", i+1, stripUSDT(it.sym), fmtCompact(it.net)))
		}
		return c.Send(strings.Join(lines, "\n"))
	}
}

func (qb *QueryBot) returnRankCmd(bucket string) tele.HandlerFunc {
	return func(c tele.Context) error {
		if !qb.requireCH(c) {
			return nil
		}
		defaultLimit := 20
		maxLimit := 60
		label := "近15分钟"
		if bucket == "1h" {
			defaultLimit = 30
			maxLimit = 120
			label = "近1小时"
		}
		limit, ok := parseLimitArg(c.Args(), defaultLimit, 1, maxLimit)
		if !ok {
			if bucket == "1h" {
				return c.Send("用法: /r1h 30 (范围 1-120)")
			}
			return c.Send("用法: /r15m 20 (范围 1-60)")
		}

		ctx := context.Background()
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
		loc := qb.userLoc(c)
		top := append([]item(nil), items...)
		if len(top) > limit {
			top = top[:limit]
		}
		upLines := []string{fmt.Sprintf("%s涨幅前%d（%s）", label, limit, fmtTs(bucketStart, loc))}
		for i, it := range top {
			upLines = append(upLines, fmt.Sprintf("%d.%s %s", i+1, stripUSDT(it.sym), fmtSignedPct(it.ret)))
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ret < items[j].ret })
		down := items
		downLines := []string{fmt.Sprintf("%s跌幅前%d（%s）", label, limit, fmtTs(bucketStart, loc))}
		bot := down
		if len(bot) > limit {
			bot = bot[:limit]
		}
		for i, it := range bot {
			downLines = append(downLines, fmt.Sprintf("%d.%s %s", i+1, stripUSDT(it.sym), fmtSignedPct(it.ret)))
		}
		if err := c.Send(strings.Join(upLines, "\n")); err != nil {
			return err
		}
		return c.Send(strings.Join(downLines, "\n"))
	}
}

func (qb *QueryBot) oiCapRatioCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	ctx := context.Background()
	limit, ok := parseLimitArg(c.Args(), 30, 1, 120)
	if !ok {
		return c.Send("用法: /oicapratio 30 (范围 1-120)")
	}
	items, err := service.GetOIMarketCapRank(ctx, qb.ch, limit)
	if err != nil {
		return c.Send("获取数据失败")
	}
	if len(items) == 0 {
		return c.Send("/oicapratio 暂无数据")
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("持仓/市值比例前%d:\n", limit))
	for i, it := range items {
		b.WriteString(fmt.Sprintf("%d.%s %s | %sU / %sU\n",
			i+1, stripUSDT(it.Symbol), fmtSignedPctKeepNaN(it.Ratio*100, 2, false), fmtBigUSD(it.OINotionalUSD), fmtBigUSD(it.MarketCapUSD)))
	}
	return c.Send(b.String())
}

func (qb *QueryBot) nchCmd(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("用法: /nchBTC (计算按UTC，展示按个人时区)")
	}
	return qb.sendNCH(c, normalizeSymbol(args[0]))
}

func (qb *QueryBot) ncdCmd(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("用法: /ncdBTC (计算按UTC，展示按个人时区)")
	}
	return qb.sendNCD(c, normalizeSymbol(args[0]))
}

func (qb *QueryBot) oihCmd(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("用法: /oihBTC (计算按UTC，展示按个人时区)")
	}
	return qb.sendOIH(c, normalizeSymbol(args[0]))
}

func (qb *QueryBot) oidCmd(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("用法: /oidBTC (计算按UTC，展示按个人时区)")
	}
	return qb.sendOID(c, normalizeSymbol(args[0]))
}

func (qb *QueryBot) textCmd(c tele.Context) error {
	if !qb.requireCH(c) {
		return nil
	}
	text := strings.TrimSpace(c.Text())
	if cmd, sym, ok := parseInlineSymbolCommand(text); ok {
		return qb.inlineSymbolCmd(c, cmd, sym)
	}
	if !isSymbolText(text) {
		return nil
	}
	sym := normalizeSymbol(text)
	if sym == "" {
		return nil
	}
	ctx := context.Background()
	pairs, err := qb.bn.GetPairs(ctx, "swap")
	if err == nil && !containsSymbol(pairs, sym) {
		return nil
	}

	nowMs := time.Now().UnixMilli()
	dayStartMs := (nowMs / 86_400_000) * 86_400_000
	asset := stripUSDT(sym)

	ticker, err := qb.bn.GetTicker24h(ctx, "swap", sym)
	if err != nil {
		return c.Send(fmt.Sprintf("%s: 未找到该币种", sym))
	}
	h1Rows, _ := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1h", nowMs-8*60*60*1000, 0, "desc", 8)
	d1Rows, _ := qb.ch.QueryTradeBuckets(ctx, "swap", sym, nil, "1d", nowMs-120*24*60*60*1000, 0, "desc", 120)
	oiSnapshot, _ := qb.ch.QueryOIBySymbol(ctx, sym)
	fundingSnapshot, _ := qb.ch.QueryFundingBySymbol(ctx, sym)
	capRow, _ := qb.ch.QueryMarketCapByAsset(ctx, asset)
	allCapRows, _ := qb.ch.QueryMarketCaps(ctx, nil)
	premium, _ := qb.bn.GetFuturesPremiumIndex(ctx, sym)
	oiHist, _ := qb.bn.GetOpenInterestHist(ctx, sym, "1d", 2)
	lsrRows := qb.buildLSRRows(ctx, sym, 5)
	fundWindows := qb.buildFundWindows(ctx, sym, []int{1, 2, 3, 5, 7, 10, 15, 20, 25, 30})

	price := toF64(ticker["lastPrice"])
	priceChangePct := toF64(ticker["priceChangePercent"])

	rangeLabel := "-"
	amp6hPct := math.NaN()
	volumeFactor := math.NaN()
	if len(h1Rows) > 0 {
		high6h := math.NaN()
		low6h := math.NaN()
		quoteValues := make([]float64, 0, len(h1Rows))
		for _, row := range h1Rows {
			if row.HighPrice != nil && (math.IsNaN(high6h) || *row.HighPrice > high6h) {
				high6h = *row.HighPrice
			}
			if row.LowPrice != nil && (math.IsNaN(low6h) || *row.LowPrice < low6h) {
				low6h = *row.LowPrice
			}
			if row.QuoteNotional > 0 {
				quoteValues = append(quoteValues, row.QuoteNotional)
			}
		}
		if !math.IsNaN(high6h) && !math.IsNaN(low6h) && high6h > low6h && price > 0 {
			pos := (price - low6h) / (high6h - low6h)
			if pos <= 0.33 {
				rangeLabel = "底部"
			} else if pos >= 0.67 {
				rangeLabel = "顶部"
			} else {
				rangeLabel = "中部"
			}
			amp6hPct = (high6h - low6h) / price * 100
		}
		if len(quoteValues) >= 2 {
			latest := quoteValues[0]
			sum := 0.0
			for i := 1; i < len(quoteValues); i++ {
				sum += quoteValues[i]
			}
			avg := sum / float64(len(quoteValues)-1)
			if avg > 0 {
				volumeFactor = latest / avg
			}
		}
	}

	waveCount := 0
	_ = qb.store.GetContext(ctx, &waveCount, `SELECT count(*) FROM anomaly_events
WHERE market = 'swap' AND symbol = ? AND event_time_ms >= ?
AND event_type IN ('breakout_up','breakout_down','amplitude_spike')`, sym, dayStartMs)

	closedD1Rows := make([]model.CHTradeRow, 0, len(d1Rows))
	for _, row := range d1Rows {
		if row.BucketStartMs < dayStartMs {
			closedD1Rows = append(closedD1Rows, row)
		}
	}
	if len(closedD1Rows) == 0 {
		closedD1Rows = d1Rows
	}

	yesterdayPct := dailyReturnPct(closedD1Rows, 0)
	lastWeekPct := windowReturnPct(closedD1Rows, 7, 7)
	thisWeekPct := windowReturnPct(closedD1Rows, 7, 0)
	trend6 := scoreTrend(closedD1Rows, 6)
	trend60 := scoreTrend(closedD1Rows, 60)

	fundingRate := math.NaN()
	if v, ok := premium["lastFundingRate"]; ok {
		fundingRate = toF64(v)
	}
	if math.IsNaN(fundingRate) && fundingSnapshot != nil {
		fundingRate = fundingSnapshot.LastFundingRate
	}

	oiQty := math.NaN()
	oiValue := math.NaN()
	if oiSnapshot != nil {
		oiQty = oiSnapshot.OpenInterest
		oiValue = oiSnapshot.OINotionalUSD
	}
	oiQtyChangePct := math.NaN()
	oiValueChangePct := math.NaN()
	if len(oiHist) >= 2 {
		sort.Slice(oiHist, func(i, j int) bool { return toI64(oiHist[i]["timestamp"]) < toI64(oiHist[j]["timestamp"]) })
		prev := oiHist[len(oiHist)-2]
		curr := oiHist[len(oiHist)-1]
		prevQty := toF64(prev["sumOpenInterest"])
		currQty := toF64(curr["sumOpenInterest"])
		prevVal := toF64(prev["sumOpenInterestValue"])
		currVal := toF64(curr["sumOpenInterestValue"])
		if math.IsNaN(oiQty) && currQty > 0 {
			oiQty = currQty
		}
		if math.IsNaN(oiValue) && currVal > 0 {
			oiValue = currVal
		}
		if prevQty > 0 && currQty > 0 {
			oiQtyChangePct = (currQty - prevQty) / prevQty * 100
		}
		if prevVal > 0 && currVal > 0 {
			oiValueChangePct = (currVal - prevVal) / prevVal * 100
		}
	}

	marketCap := math.NaN()
	if capRow != nil {
		marketCap = capRow.MarketCapUSD
	}
	totalCap := 0.0
	for _, row := range allCapRows {
		totalCap += row.MarketCapUSD
	}
	capDominancePct := math.NaN()
	if !math.IsNaN(marketCap) && totalCap > 0 {
		capDominancePct = marketCap / totalCap * 100
	}
	positionScalePct := math.NaN()
	if !math.IsNaN(oiValue) && !math.IsNaN(marketCap) && marketCap > 0 {
		positionScalePct = oiValue / marketCap * 100
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("#%s\n\n", asset))
	sb.WriteString(fmt.Sprintf("当前价格 %s (%s)\n", fmtCompact(price), fmtSignedPctKeepNaN(priceChangePct, 2, false)))
	sb.WriteString(fmt.Sprintf("价格区间 %s (6小时)\n", rangeLabel))
	sb.WriteString(fmt.Sprintf("振幅量能 %s , %s\n", fmtSignedPctKeepNaN(amp6hPct, 2, true), fmtFactor(volumeFactor)))
	sb.WriteString(fmt.Sprintf("今日波动 %d次\n", waveCount))
	sb.WriteString("摸顶探底 -\n")
	sb.WriteString(fmt.Sprintf("昨日涨幅 %s\n", fmtSignedPctKeepNaN(yesterdayPct, 2, false)))
	sb.WriteString(fmt.Sprintf("上周涨幅 %s\n", fmtSignedPctKeepNaN(lastWeekPct, 2, false)))
	sb.WriteString(fmt.Sprintf("本周涨幅 %s\n", fmtSignedPctKeepNaN(thisWeekPct, 2, false)))
	sb.WriteString(fmt.Sprintf("六日趋势 %s (%s)\n", fmtTrendScore(trend6), trendLabelV2(trend6)))
	sb.WriteString(fmt.Sprintf("六十趋势 %s (%s)\n", fmtTrendScore(trend60), trendLabelV2(trend60)))
	sb.WriteString(fmt.Sprintf("持仓数量 %s币 (%s)\n", fmtCompact(oiQty), fmtSignedPctKeepNaN(oiQtyChangePct, 2, false)))
	sb.WriteString(fmt.Sprintf("持仓价值 %sU (%s)\n", fmtBigUSD(oiValue), fmtSignedPctKeepNaN(oiValueChangePct, 2, false)))
	sb.WriteString(fmt.Sprintf("头寸规模 %s (持仓:市值)\n", fmtSignedPctKeepNaN(positionScalePct, 2, true)))
	if math.IsNaN(fundingRate) {
		sb.WriteString("资金费率 -\n")
	} else {
		sb.WriteString(fmt.Sprintf("资金费率 %s\n", fmtSignedPctKeepNaN(fundingRate*100, 4, false)))
	}
	sb.WriteString(fmt.Sprintf("流通市值 %s (%s)\n", fmtBigUSD(marketCap), fmtSignedPctKeepNaN(capDominancePct, 2, true)))
	sb.WriteString("稀释市值 -\n\n")
	sb.WriteString(fmt.Sprintf("#%s多空比|大户数|大户持仓\n", asset))
	if len(lsrRows) == 0 {
		sb.WriteString("-\n")
	} else {
		for _, row := range lsrRows {
			sb.WriteString(fmt.Sprintf("%s: %.2f | %.2f | %.2f\n",
				fmtLSRHour(toI64(row["timestamp"])),
				toF64(row["globalRatio"]),
				toF64(row["topAccountRatio"]),
				toF64(row["topPositionRatio"]),
			))
		}
	}
	sb.WriteString(fmt.Sprintf("\n/%s 合约资金 | 现货资金\n", asset))
	for _, item := range fundWindows {
		sb.WriteString(fmt.Sprintf("%02dD %s | %s\n", item.Day, fmtCompact(item.SwapNet), fmtCompact(item.SpotNet)))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("/nch%s 资金盘间快照\n", asset))
	sb.WriteString(fmt.Sprintf("资金每日流向 /ncd%s\n", asset))
	sb.WriteString(fmt.Sprintf("/oih%s 持仓盘间快照\n", asset))
	sb.WriteString(fmt.Sprintf("持仓近日变化 /oid%s", asset))
	return c.Send(sb.String())
}

type fundWindowItem struct {
	Day     int
	SwapNet float64
	SpotNet float64
}

func (qb *QueryBot) buildFundWindows(ctx context.Context, symbol string, windows []int) []fundWindowItem {
	if len(windows) == 0 {
		return nil
	}
	maxDays := 1
	for _, d := range windows {
		if d > maxDays {
			maxDays = d
		}
	}
	startMs := time.Now().UnixMilli() - int64(maxDays+1)*24*60*60*1000
	swapRows, _ := qb.ch.QueryTradeBuckets(ctx, "swap", symbol, nil, "1d", startMs, 0, "desc", maxDays+2)
	spotRows, _ := qb.ch.QueryTradeBuckets(ctx, "spot", symbol, nil, "1d", startMs, 0, "desc", maxDays+2)

	sumNet := func(rows []model.CHTradeRow, day int) float64 {
		n := day
		if len(rows) < n {
			n = len(rows)
		}
		total := 0.0
		for i := 0; i < n; i++ {
			total += rows[i].TakerBuyNotional - rows[i].TakerSellNotional
		}
		return total
	}

	out := make([]fundWindowItem, 0, len(windows))
	for _, day := range windows {
		out = append(out, fundWindowItem{Day: day, SwapNet: sumNet(swapRows, day), SpotNet: sumNet(spotRows, day)})
	}
	return out
}

func (qb *QueryBot) buildLSRRows(ctx context.Context, symbol string, limit int) []map[string]float64 {
	globalRows, _ := qb.bn.GetGlobalLongShortAccountRatio(ctx, symbol, "1h", limit)
	topAccountRows, _ := qb.bn.GetTopLongShortAccountRatio(ctx, symbol, "1h", limit)
	topPositionRows, _ := qb.bn.GetTopLongShortPositionRatio(ctx, symbol, "1h", limit)

	indexByTs := func(rows []map[string]interface{}) map[int64]float64 {
		out := make(map[int64]float64, len(rows))
		for _, item := range rows {
			ts := toI64(item["timestamp"])
			r := toF64(item["longShortRatio"])
			if ts > 0 {
				out[ts] = r
			}
		}
		return out
	}
	gMap := indexByTs(globalRows)
	aMap := indexByTs(topAccountRows)
	pMap := indexByTs(topPositionRows)

	keys := make([]int64, 0, len(gMap)+len(aMap)+len(pMap))
	seen := map[int64]struct{}{}
	for ts := range gMap {
		seen[ts] = struct{}{}
	}
	for ts := range aMap {
		seen[ts] = struct{}{}
	}
	for ts := range pMap {
		seen[ts] = struct{}{}
	}
	for ts := range seen {
		keys = append(keys, ts)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}

	out := make([]map[string]float64, 0, len(keys))
	for _, ts := range keys {
		out = append(out, map[string]float64{
			"timestamp":        float64(ts),
			"globalRatio":      gMap[ts],
			"topAccountRatio":  aMap[ts],
			"topPositionRatio": pMap[ts],
		})
	}
	return out
}

type bullIndexItem struct {
	Symbol      string
	Score       float64
	RetPct      float64
	FlowBiasPct float64
}

func (qb *QueryBot) bullIndexRank(ctx context.Context, limit int) (int64, []bullIndexItem) {
	const hourMs int64 = 60 * 60 * 1000
	nowMs := time.Now().UnixMilli()
	lastClosed := (nowMs/hourMs)*hourMs - hourMs
	target := int64(0)
	for i := 0; i < 24; i++ {
		start := lastClosed - int64(i)*hourMs
		end := start + hourMs - 1
		rows, _ := qb.ch.QueryTradeBuckets(ctx, "swap", "", nil, "1h", start, end, "asc", 1)
		if len(rows) > 0 {
			target = start
			break
		}
	}
	if target == 0 {
		return 0, nil
	}
	rows, err := qb.ch.QueryTradeBuckets(ctx, "swap", "", nil, "1h", target, target+hourMs-1, "asc", 0)
	if err != nil || len(rows) == 0 {
		return target, nil
	}
	items := make([]bullIndexItem, 0, len(rows))
	for _, row := range rows {
		if row.OpenPrice == nil || row.ClosePrice == nil || *row.OpenPrice <= 0 || binance.IsExcludedSymbol(row.Symbol) {
			continue
		}
		retPct := (*row.ClosePrice - *row.OpenPrice) / *row.OpenPrice * 100
		flowDenom := row.TakerBuyNotional + row.TakerSellNotional
		flowBias := 0.0
		if flowDenom > 0 {
			flowBias = (row.TakerBuyNotional - row.TakerSellNotional) / flowDenom
		}
		score := 50 + retPct*2 + flowBias*50
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		items = append(items, bullIndexItem{
			Symbol:      row.Symbol,
			Score:       score,
			RetPct:      retPct,
			FlowBiasPct: flowBias * 100,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	if len(items) > limit {
		items = items[:limit]
	}
	return target, items
}

type oiGrowthItem struct {
	Symbol        string
	ChangePct     float64
	OINotionalUSD float64
}

func (qb *QueryBot) openInterestGrowthRank(ctx context.Context, limit int) []oiGrowthItem {
	rows, err := qb.ch.QueryOISnapshots(ctx)
	if err != nil || len(rows) == 0 {
		return nil
	}
	candidateCount := limit * 3
	if candidateCount < 40 {
		candidateCount = 40
	}
	if candidateCount > 120 {
		candidateCount = 120
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].OINotionalUSD > rows[j].OINotionalUSD })
	if len(rows) > candidateCount {
		rows = rows[:candidateCount]
	}

	type result struct {
		item oiGrowthItem
		ok   bool
	}
	sem := make(chan struct{}, 8)
	outCh := make(chan result, len(rows))
	var wg sync.WaitGroup
	for _, row := range rows {
		r := row
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hist, err := qb.bn.GetOpenInterestHist(ctx, r.Symbol, "1d", 2)
			if err != nil || len(hist) < 2 {
				outCh <- result{}
				return
			}
			sort.Slice(hist, func(i, j int) bool { return toI64(hist[i]["timestamp"]) < toI64(hist[j]["timestamp"]) })
			prev := toF64(hist[len(hist)-2]["sumOpenInterestValue"])
			curr := toF64(hist[len(hist)-1]["sumOpenInterestValue"])
			if prev <= 0 || curr <= 0 {
				outCh <- result{}
				return
			}
			outCh <- result{
				ok: true,
				item: oiGrowthItem{
					Symbol:        r.Symbol,
					ChangePct:     (curr - prev) / prev * 100,
					OINotionalUSD: r.OINotionalUSD,
				},
			}
		}()
	}
	wg.Wait()
	close(outCh)

	items := make([]oiGrowthItem, 0, len(rows))
	for r := range outCh {
		if r.ok {
			items = append(items, r.item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ChangePct > items[j].ChangePct })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (qb *QueryBot) userID(c tele.Context) int64 {
	if s := c.Sender(); s != nil {
		return s.ID
	}
	return 0
}

const defaultQueryTZ = "Australia/Sydney"

var tzAliasMap = map[string]string{
	"UTC":              "UTC",
	"SYDNEY":           "Australia/Sydney",
	"AEST":             "Australia/Sydney",
	"AUSTRALIA/SYDNEY": "Australia/Sydney",
	"ASIA/SHANGHAI":    "Asia/Shanghai",
}

func normalizeTZName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}
	up := strings.ToUpper(name)
	if v, ok := tzAliasMap[up]; ok {
		name = v
	}
	if strings.EqualFold(name, "UTC") {
		return "UTC"
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

func (qb *QueryBot) tzRedisKey(userID int64) string {
	return fmt.Sprintf("%s:query:tz:%d", qb.prefix, userID)
}

func (qb *QueryBot) getUserTZ(ctx context.Context, userID int64) string {
	if userID <= 0 {
		return defaultQueryTZ
	}
	qb.tzMu.RLock()
	if v, ok := qb.tzMap[userID]; ok && v != "" {
		qb.tzMu.RUnlock()
		return v
	}
	qb.tzMu.RUnlock()

	tz := defaultQueryTZ
	if qb.redis != nil {
		if raw, err := qb.redis.Get(ctx, qb.tzRedisKey(userID)); err == nil {
			if norm := normalizeTZName(raw); norm != "" {
				tz = norm
			}
		}
	}
	qb.tzMu.Lock()
	qb.tzMap[userID] = tz
	qb.tzMu.Unlock()
	return tz
}

func (qb *QueryBot) setUserTZ(ctx context.Context, userID int64, tz string) {
	if userID <= 0 || tz == "" {
		return
	}
	qb.tzMu.Lock()
	qb.tzMap[userID] = tz
	qb.tzMu.Unlock()
	if qb.redis != nil {
		_ = qb.redis.Set(ctx, qb.tzRedisKey(userID), tz, 0)
	}
}

func (qb *QueryBot) locOrUTC(tz string) *time.Location {
	if strings.EqualFold(tz, "UTC") {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (qb *QueryBot) userLoc(c tele.Context) *time.Location {
	return qb.locOrUTC(qb.getUserTZ(context.Background(), qb.userID(c)))
}

type netPoint struct {
	Ts   int64
	Swap float64
	Spot float64
}

func (qb *QueryBot) intradayNetSnapshot(ctx context.Context, symbol string) []netPoint {
	nowMs := time.Now().UnixMilli()
	const hourMs int64 = 60 * 60 * 1000
	const dayMs int64 = 24 * 60 * 60 * 1000
	dayStart := (nowMs / dayMs) * dayMs
	currentHour := (nowMs / hourMs) * hourMs

	rows, err := qb.ch.QueryTradeBuckets(ctx, "", symbol, nil, "1m", dayStart, nowMs, "asc", 0)
	if err != nil || len(rows) == 0 {
		return nil
	}
	type v struct {
		ts  int64
		net float64
	}
	byMkt := map[string][]v{"swap": {}, "spot": {}}
	for _, r := range rows {
		byMkt[r.Market] = append(byMkt[r.Market], v{ts: r.BucketStartMs, net: r.TakerBuyNotional - r.TakerSellNotional})
	}
	cumAt := func(entries []v, cutoffs []int64) []float64 {
		out := make([]float64, 0, len(cutoffs))
		if len(cutoffs) == 0 {
			return out
		}
		idx := 0
		total := 0.0
		for _, cutoff := range cutoffs {
			for idx < len(entries) && entries[idx].ts < cutoff {
				total += entries[idx].net
				idx++
			}
			out = append(out, total)
		}
		return out
	}
	cutoffs := make([]int64, 0, 24)
	for ts := dayStart + hourMs; ts <= currentHour; ts += hourMs {
		cutoffs = append(cutoffs, ts)
	}
	swapClosed := cumAt(byMkt["swap"], cutoffs)
	spotClosed := cumAt(byMkt["spot"], cutoffs)
	swapNow := cumAt(byMkt["swap"], []int64{nowMs + 1})
	spotNow := cumAt(byMkt["spot"], []int64{nowMs + 1})
	items := make([]netPoint, 0, len(cutoffs)+1)
	for i, ts := range cutoffs {
		items = append(items, netPoint{Ts: ts, Swap: swapClosed[i], Spot: spotClosed[i]})
	}
	if nowMs-currentHour >= 60*1000 && len(swapNow) > 0 && len(spotNow) > 0 {
		items = append(items, netPoint{Ts: nowMs, Swap: swapNow[0], Spot: spotNow[0]})
	}
	return items
}

func (qb *QueryBot) dailyNetSeries(ctx context.Context, symbol string, days int) []netPoint {
	nowMs := time.Now().UnixMilli()
	const dayMs int64 = 24 * 60 * 60 * 1000
	startMs := nowMs - int64(days+2)*dayMs
	swapRows, _ := qb.ch.QueryTradeBuckets(ctx, "swap", symbol, nil, "1d", startMs, 0, "asc", days+3)
	spotRows, _ := qb.ch.QueryTradeBuckets(ctx, "spot", symbol, nil, "1d", startMs, 0, "asc", days+3)
	swapMap := map[int64]float64{}
	spotMap := map[int64]float64{}
	for _, r := range swapRows {
		swapMap[r.BucketStartMs] = r.TakerBuyNotional - r.TakerSellNotional
	}
	for _, r := range spotRows {
		spotMap[r.BucketStartMs] = r.TakerBuyNotional - r.TakerSellNotional
	}
	keys := make([]int64, 0, len(swapMap)+len(spotMap))
	seen := map[int64]struct{}{}
	for k := range swapMap {
		seen[k] = struct{}{}
	}
	for k := range spotMap {
		seen[k] = struct{}{}
	}
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) > days {
		keys = keys[len(keys)-days:]
	}
	items := make([]netPoint, 0, len(keys))
	for _, ts := range keys {
		items = append(items, netPoint{Ts: ts, Swap: swapMap[ts], Spot: spotMap[ts]})
	}
	return items
}

type oiPoint struct {
	Ts        int64
	OIQty     float64
	OIValue   float64
	ChangePct float64
	HasChange bool
}

func (qb *QueryBot) oiHourlySnapshot(ctx context.Context, symbol string, hours int) []oiPoint {
	rows, err := qb.bn.GetOpenInterestHist(ctx, symbol, "1h", maxInt(2, hours))
	if err != nil || len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return toI64(rows[i]["timestamp"]) < toI64(rows[j]["timestamp"]) })
	dayStart := (time.Now().UnixMilli() / 86_400_000) * 86_400_000
	dayOpen := math.NaN()
	for _, r := range rows {
		ts := toI64(r["timestamp"])
		if ts >= dayStart {
			dayOpen = toF64(r["sumOpenInterest"])
			break
		}
	}
	if len(rows) > hours {
		rows = rows[len(rows)-hours:]
	}
	out := make([]oiPoint, 0, len(rows)+1)
	for _, r := range rows {
		ts := toI64(r["timestamp"])
		qty := toF64(r["sumOpenInterest"])
		val := toF64(r["sumOpenInterestValue"])
		p := oiPoint{Ts: ts, OIQty: qty, OIValue: val}
		if !math.IsNaN(dayOpen) && dayOpen > 0 && ts >= dayStart {
			p.ChangePct = (qty - dayOpen) / dayOpen * 100
			p.HasChange = true
		}
		out = append(out, p)
	}
	if snap, _ := qb.ch.QueryOIBySymbol(ctx, symbol); snap != nil {
		p := oiPoint{Ts: time.Now().UnixMilli(), OIQty: snap.OpenInterest, OIValue: snap.OINotionalUSD}
		if !math.IsNaN(dayOpen) && dayOpen > 0 {
			p.ChangePct = (p.OIQty - dayOpen) / dayOpen * 100
			p.HasChange = true
		}
		out = append(out, p)
	}
	return out
}

func (qb *QueryBot) oiDailySeries(ctx context.Context, symbol string, days int) []oiPoint {
	rows, err := qb.bn.GetOpenInterestHist(ctx, symbol, "1d", maxInt(2, days))
	if err != nil || len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return toI64(rows[i]["timestamp"]) < toI64(rows[j]["timestamp"]) })
	if len(rows) > days {
		rows = rows[len(rows)-days:]
	}
	prevQty := math.NaN()
	out := make([]oiPoint, 0, len(rows))
	for _, r := range rows {
		qty := toF64(r["sumOpenInterest"])
		p := oiPoint{
			Ts:      toI64(r["timestamp"]),
			OIQty:   qty,
			OIValue: toF64(r["sumOpenInterestValue"]),
		}
		if !math.IsNaN(prevQty) && prevQty > 0 {
			p.ChangePct = (qty - prevQty) / prevQty * 100
			p.HasChange = true
		}
		prevQty = qty
		out = append(out, p)
	}
	return out
}

func (qb *QueryBot) sendNCH(c tele.Context, symbol string) error {
	ctx := context.Background()
	if pairs, err := qb.bn.GetPairs(ctx, "swap"); err == nil && !containsSymbol(pairs, symbol) {
		return c.Send("币种不存在或未在合约市场交易")
	}
	items := qb.intradayNetSnapshot(ctx, symbol)
	if len(items) == 0 {
		return c.Send(fmt.Sprintf("%s 盘间资金暂无数据", stripUSDT(symbol)))
	}
	loc := qb.userLoc(c)
	swapMarks := markAnomaly(valuesFromNet(items, true), 3.0, 6, 5_000_000, 1_000_000)
	spotMarks := markAnomaly(valuesFromNet(items, false), 3.0, 6, 5_000_000, 1_000_000)
	base := stripUSDT(symbol)
	lines := []string{fmt.Sprintf("%s累计资金快照 (UTC口径 / 本地展示) (合约 | 现货)", base), "* 为阈值异常", ""}
	for i, it := range items {
		swapText := fmtCompact(it.Swap)
		spotText := fmtCompact(it.Spot)
		if i < len(swapMarks) && swapMarks[i] {
			swapText += "*"
		}
		if i < len(spotMarks) && spotMarks[i] {
			spotText += "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s | %s", fmtHHMM(it.Ts, loc), swapText, spotText))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) sendNCD(c tele.Context, symbol string) error {
	ctx := context.Background()
	if pairs, err := qb.bn.GetPairs(ctx, "swap"); err == nil && !containsSymbol(pairs, symbol) {
		return c.Send("币种不存在或未在合约市场交易")
	}
	items := qb.dailyNetSeries(ctx, symbol, 30)
	if len(items) == 0 {
		return c.Send(fmt.Sprintf("%s 每日净资金暂无数据", stripUSDT(symbol)))
	}
	loc := qb.userLoc(c)
	swapMarks := markAnomaly(valuesFromNet(items, true), 3.2, 10, 20_000_000, 5_000_000)
	spotMarks := markAnomaly(valuesFromNet(items, false), 3.2, 10, 20_000_000, 5_000_000)
	base := stripUSDT(symbol)
	lines := []string{fmt.Sprintf("%s每日净资金 (UTC口径 / 本地展示) (合约 | 现货)", base), "* 为阈值异常", ""}
	for i, it := range items {
		swapText := fmtCompact(it.Swap)
		spotText := fmtCompact(it.Spot)
		if i < len(swapMarks) && swapMarks[i] {
			swapText += "*"
		}
		if i < len(spotMarks) && spotMarks[i] {
			spotText += "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s | %s", fmtMMDD(it.Ts, loc), swapText, spotText))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) sendOIH(c tele.Context, symbol string) error {
	ctx := context.Background()
	if pairs, err := qb.bn.GetPairs(ctx, "swap"); err == nil && !containsSymbol(pairs, symbol) {
		return c.Send("币种不存在或未在合约市场交易")
	}
	items := qb.oiHourlySnapshot(ctx, symbol, 24)
	if len(items) == 0 {
		return c.Send(fmt.Sprintf("%s 持仓快照暂无数据", stripUSDT(symbol)))
	}
	loc := qb.userLoc(c)
	pcts := make([]float64, 0, len(items))
	for _, it := range items {
		if it.HasChange {
			pcts = append(pcts, it.ChangePct)
		} else {
			pcts = append(pcts, math.NaN())
		}
	}
	marks := markAnomaly(pcts, 3.0, 8, 3.0, 0.5)
	base := stripUSDT(symbol)
	lines := []string{fmt.Sprintf("%s持仓快照 (UTC口径 / 本地展示) (较开盘时变化)", base), "* 为阈值异常", ""}
	for i, it := range items {
		star := ""
		if i < len(marks) && marks[i] {
			star = "*"
		}
		pctText := ""
		if it.HasChange {
			pctText = fmt.Sprintf(" (%s)", fmtSignedPctKeepNaN(it.ChangePct, 2, true))
		}
		lines = append(lines, fmt.Sprintf("%s %s%s (%sU)%s", fmtHHMM(it.Ts, loc), fmtCompact(it.OIQty), star, fmtBigUSD(it.OIValue), pctText))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) sendOID(c tele.Context, symbol string) error {
	ctx := context.Background()
	if pairs, err := qb.bn.GetPairs(ctx, "swap"); err == nil && !containsSymbol(pairs, symbol) {
		return c.Send("币种不存在或未在合约市场交易")
	}
	items := qb.oiDailySeries(ctx, symbol, 30)
	if len(items) == 0 {
		return c.Send(fmt.Sprintf("%s 近30天持仓暂无数据", stripUSDT(symbol)))
	}
	loc := qb.userLoc(c)
	pcts := make([]float64, 0, len(items))
	for _, it := range items {
		if it.HasChange {
			pcts = append(pcts, it.ChangePct)
		} else {
			pcts = append(pcts, math.NaN())
		}
	}
	marks := markAnomaly(pcts, 3.2, 10, 3.0, 0.5)
	base := stripUSDT(symbol)
	lines := []string{fmt.Sprintf("%s近30天持仓数据 (UTC口径 / 本地展示) (较前一天变化)", base), "* 为阈值异常", ""}
	for i, it := range items {
		star := ""
		if i < len(marks) && marks[i] {
			star = "*"
		}
		pctText := ""
		if it.HasChange {
			pctText = " " + fmtSignedPctKeepNaN(it.ChangePct, 2, true)
		}
		lines = append(lines, fmt.Sprintf("%s %s%s%s (%sU)", fmtMMDD(it.Ts, loc), fmtCompact(it.OIQty), star, pctText, fmtBigUSD(it.OIValue)))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (qb *QueryBot) inlineSymbolCmd(c tele.Context, cmd, symbol string) error {
	switch cmd {
	case "nch":
		return qb.sendNCH(c, symbol)
	case "ncd":
		return qb.sendNCD(c, symbol)
	case "oih":
		return qb.sendOIH(c, symbol)
	case "oid":
		return qb.sendOID(c, symbol)
	default:
		return nil
	}
}

func containsSymbol(symbols []string, symbol string) bool {
	for _, s := range symbols {
		if s == symbol {
			return true
		}
	}
	return false
}

var symbolTextRe = regexp.MustCompile(`^[A-Za-z0-9]{2,16}$`)

func isSymbolText(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "/") {
		return false
	}
	return symbolTextRe.MatchString(t)
}

func stripUSDT(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	return strings.TrimSuffix(s, "USDT")
}

var inlineSymbolCmdRe = regexp.MustCompile(`(?i)^/(nch|ncd|oih|oid)([A-Za-z0-9]{2,16})(?:@[A-Za-z0-9_]+)?$`)

func parseInlineSymbolCommand(text string) (string, string, bool) {
	m := inlineSymbolCmdRe.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) != 3 {
		return "", "", false
	}
	cmd := strings.ToLower(m[1])
	sym := normalizeSymbol(m[2])
	if sym == "" {
		return "", "", false
	}
	return cmd, sym, true
}

func fmtMMDD(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return "--"
	}
	return time.UnixMilli(ms).In(loc).Format("0102")
}

func valuesFromNet(items []netPoint, swap bool) []float64 {
	out := make([]float64, 0, len(items))
	for _, it := range items {
		if swap {
			out = append(out, it.Swap)
		} else {
			out = append(out, it.Spot)
		}
	}
	return out
}

func markAnomaly(values []float64, zThreshold float64, minPoints int, fallbackAbs float64, minAbs float64) []bool {
	nums := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			nums = append(nums, v)
		}
	}
	marks := make([]bool, len(values))
	if len(nums) < maxInt(2, minPoints) {
		for i, v := range values {
			if !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) >= fallbackAbs {
				marks[i] = true
			}
		}
		return marks
	}
	med := median(nums)
	dev := make([]float64, 0, len(nums))
	for _, v := range nums {
		dev = append(dev, math.Abs(v-med))
	}
	mad := median(dev)
	if mad < 1e-9 {
		return marks
	}
	for i, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) < minAbs {
			continue
		}
		robustZ := 0.6745 * (v - med) / mad
		if math.Abs(robustZ) >= zThreshold {
			marks[i] = true
		}
	}
	return marks
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	n := len(cp)
	mid := n / 2
	if n%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func dailyReturnPct(rows []model.CHTradeRow, idx int) float64 {
	if idx < 0 || idx >= len(rows) {
		return math.NaN()
	}
	open := rows[idx].OpenPrice
	close := rows[idx].ClosePrice
	if open == nil || close == nil || *open <= 0 {
		return math.NaN()
	}
	return (*close - *open) / (*open) * 100
}

func windowReturnPct(rows []model.CHTradeRow, days, offset int) float64 {
	if len(rows) < offset+days || days <= 0 {
		return math.NaN()
	}
	latest := rows[offset]
	oldest := rows[offset+days-1]
	if latest.ClosePrice == nil || oldest.OpenPrice == nil || *oldest.OpenPrice <= 0 {
		return math.NaN()
	}
	return (*latest.ClosePrice - *oldest.OpenPrice) / (*oldest.OpenPrice) * 100
}

func scoreTrend(rows []model.CHTradeRow, window int) float64 {
	if len(rows) < 2 {
		return math.NaN()
	}
	n := window
	if n < 2 {
		n = 2
	}
	if n > len(rows) {
		n = len(rows)
	}
	useRows := rows[:n]
	if useRows[0].ClosePrice == nil || useRows[n-1].OpenPrice == nil || *useRows[n-1].OpenPrice <= 0 {
		return math.NaN()
	}
	netRet := (*useRows[0].ClosePrice - *useRows[n-1].OpenPrice) / *useRows[n-1].OpenPrice
	upDays := 0
	validDays := 0
	for _, row := range useRows {
		if row.OpenPrice == nil || row.ClosePrice == nil || *row.OpenPrice <= 0 {
			continue
		}
		validDays++
		if *row.ClosePrice >= *row.OpenPrice {
			upDays++
		}
	}
	if validDays == 0 {
		return math.NaN()
	}
	upRatio := float64(upDays) / float64(validDays)
	score := 50 + netRet*280 + (upRatio-0.5)*30
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return math.Round(score)
}

func trendLabelV2(score float64) string {
	if math.IsNaN(score) {
		return "-"
	}
	if score >= 70 {
		return "强上升"
	}
	if score >= 55 {
		return "上升"
	}
	if score <= 30 {
		return "强下降"
	}
	if score <= 45 {
		return "下降"
	}
	return "震荡"
}

func fmtTrendScore(score float64) string {
	if math.IsNaN(score) {
		return "-"
	}
	return fmt.Sprintf("%.0f分", score)
}

func fmtSignedPctKeepNaN(v float64, digits int, dropPlus bool) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	out := fmt.Sprintf("%+.*f%%", digits, v)
	if dropPlus {
		out = strings.TrimPrefix(out, "+")
	}
	return out
}

func toI64(v interface{}) int64 {
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

func fmtLSRHour(ms int64) string {
	if ms <= 0 {
		return "--:--"
	}
	return time.UnixMilli(ms).In(time.Local).Format("15:00")
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
