package binance

type StreamEnvelope struct {
	Stream string         `json:"stream"`
	Data   AggTradeStream `json:"data"`
}

type AggTradeStream struct {
	EventType      string `json:"e"`
	EventTimeMs    int64  `json:"E"`
	Symbol         string `json:"s"`
	AggTradeID     int64  `json:"a"`
	Price          string `json:"p"`
	Quantity       string `json:"q"`
	FirstTradeID   int64  `json:"f"`
	LastTradeID    int64  `json:"l"`
	TradeTimeMs    int64  `json:"T"`
	IsBuyerMaker   bool   `json:"m"`
	IgnoreMismatch bool   `json:"M"`
}

type DepthStreamEnvelope struct {
	Stream string      `json:"stream"`
	Data   DepthStream `json:"data"`
}

type DepthStream struct {
	EventType   string     `json:"e"`
	EventTimeMs int64      `json:"E"`
	Symbol      string     `json:"s"`
	Bids        [][]string `json:"b"`
	Asks        [][]string `json:"a"`
	BidsAlt     [][]string `json:"bids"`
	AsksAlt     [][]string `json:"asks"`
}
