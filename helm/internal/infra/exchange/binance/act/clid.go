package act

import "mallow/helm/internal/infra/exchange"

// Binance echoes the clOrdId on the WS user-data stream and on order queries, but its
// spot trade-history (myTrades) record omits clientOrderId — so REST-sync attribution
// falls back to the exchange order id. See CLIENT_ORDER_ID.md.
// An adapter that can query by clid must also declare its clid echo surfaces.
var (
	_ exchange.ClientOrderQuerier = (*Client)(nil)
	_ exchange.ClidCapable        = (*Client)(nil)
)

func (*Client) ClidSurfaces() exchange.ClidSurfaces {
	return exchange.ClidSurfaces{WS: true, OrderQuery: true, TradeHistory: false}
}
