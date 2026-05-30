package exchange_test

// Conformance contract for client-order-id (clid) support across adapters.
//
// The risk this guards: an adapter can wire clid on PlaceOrder but forget to echo it on
// one surface (WS / order query / trade-history), producing a SILENT attribution gap —
// exactly what happened with Binance's trade-history. This test makes the support matrix
// explicit and fails if a clid-capable adapter doesn't declare the surfaces that routing
// requires. See CLIENT_ORDER_ID.md.
//
// go test ./internal/infra/exchange/ -run TestClidConformance -count=1

import (
	"testing"

	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
)

// Compile-time: each adapter satisfies both halves of the clid contract together.
var (
	_ exchange.ClientOrderQuerier = (*binanceact.Client)(nil)
	_ exchange.ClidCapable        = (*binanceact.Client)(nil)
	_ exchange.ClientOrderQuerier = (*okxact.Client)(nil)
	_ exchange.ClidCapable        = (*okxact.Client)(nil)
	_ exchange.ClientOrderQuerier = (*bybitact.Client)(nil)
	_ exchange.ClidCapable        = (*bybitact.Client)(nil)
	_ exchange.ClientOrderQuerier = (*alpacaact.Client)(nil)
	_ exchange.ClidCapable        = (*alpacaact.Client)(nil)
)

func TestClidConformance(t *testing.T) {
	// ClidSurfaces is callable on a nil receiver (no live credentials needed).
	cases := []struct {
		name string
		surf exchange.ClidSurfaces
	}{
		{"binance", (*binanceact.Client)(nil).ClidSurfaces()},
		{"okx", (*okxact.Client)(nil).ClidSurfaces()},
		{"bybit", (*bybitact.Client)(nil).ClidSurfaces()},
		{"alpaca", (*alpacaact.Client)(nil).ClidSurfaces()},
	}
	for _, c := range cases {
		// Routing resolves WS fills and order queries by clid — without these two surfaces
		// clid attribution cannot work at all, so they are mandatory for any clid adapter.
		if !c.surf.WS {
			t.Errorf("%s: must echo clid on the WS stream (ClidSurfaces.WS=false)", c.name)
		}
		if !c.surf.OrderQuery {
			t.Errorf("%s: must echo clid on order queries (ClidSurfaces.OrderQuery=false)", c.name)
		}
		// TradeHistory is allowed to be false (e.g. Binance) — REST-sync attribution then
		// falls back to the exchange id. We only assert it is a deliberate declaration.
		t.Logf("%s clid surfaces: WS=%v OrderQuery=%v TradeHistory=%v",
			c.name, c.surf.WS, c.surf.OrderQuery, c.surf.TradeHistory)
	}
}
