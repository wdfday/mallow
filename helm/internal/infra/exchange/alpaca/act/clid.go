package act

import "mallow/helm/internal/infra/exchange"

// alpaca echoes the clOrdId across WS, order queries, and the REST trade-history/sync path.
// See CLIENT_ORDER_ID.md.
// An adapter that can query by clid must also declare its clid echo surfaces.
var (
	_ exchange.ClientOrderQuerier = (*Client)(nil)
	_ exchange.ClidCapable        = (*Client)(nil)
)

func (*Client) ClidSurfaces() exchange.ClidSurfaces {
	return exchange.ClidSurfaces{WS: true, OrderQuery: true, TradeHistory: true}
}
