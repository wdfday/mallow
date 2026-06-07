package act

import "mallow/helm/internal/infra/exchange"

// Binance futures echoes clOrdId on the WS stream and order queries.
var (
	_ exchange.ClientOrderQuerier = (*Client)(nil)
	_ exchange.ClidCapable        = (*Client)(nil)
)

func (*Client) ClidSurfaces() exchange.ClidSurfaces {
	return exchange.ClidSurfaces{WS: true, OrderQuery: true, TradeHistory: false}
}
