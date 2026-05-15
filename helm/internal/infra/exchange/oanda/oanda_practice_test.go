// Integration tests against OANDA fxTrade Practice API.
// Fill in oandaPracticeToken / oandaPracticeAccountID in creds_test.go, then run:
//
//	go test -v -run TestOANDA ./internal/infra/exchange/oanda/
package oanda

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

func practiceClient(t *testing.T) (*Client, exchange.Credentials) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if oandaPracticeToken == "" || oandaPracticeAccountID == "" {
		t.Skip("oanda practice credentials not set in creds_test.go")
	}
	creds := exchange.Credentials{
		APIKey:    oandaPracticeToken,
		AccountID: oandaPracticeAccountID,
	}
	return New(Config{}), creds // default = https://api-fxpractice.oanda.com
}

func oandaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// ── Account ───────────────────────────────────────────────────────────────────

func TestOANDA_GetAccount(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	info, err := c.GetAccount(cx, creds)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	t.Logf("id=%s  currency=%s", info.ID, info.Currency)
	t.Logf("balance=%.4f  nav=%.4f  unrealized_pnl=%.4f", info.Balance, info.NAV, info.UnrealizedPL)
	t.Logf("margin_used=%.4f  margin_avail=%.4f", info.MarginUsed, info.MarginAvailable)
	t.Logf("open_trades=%d  open_positions=%d", info.OpenTradeCount, info.OpenPositionCount)
}

func TestOANDA_GetPositions(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	positions, err := c.GetPositions(cx, creds)
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) == 0 {
		t.Log("no open positions")
		return
	}
	for _, p := range positions {
		t.Logf("  %s: long=%.0f@%.5f  short=%.0f@%.5f  upl=%.4f",
			p.Instrument, p.LongUnits, p.LongAvgPrice, p.ShortUnits, p.ShortAvgPrice, p.UnrealizedPL)
	}
}

func TestOANDA_GetInstruments(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	instruments, err := c.GetInstruments(cx, creds)
	if err != nil {
		t.Fatalf("GetInstruments: %v", err)
	}
	t.Logf("available instruments: %d", len(instruments))
	for _, inst := range instruments {
		if inst.Name == "EUR_USD" || inst.Name == "GBP_USD" || inst.Name == "USD_JPY" {
			t.Logf("  %s (%s): type=%s  pip=%d  min_size=%s",
				inst.Name, inst.DisplayName, inst.Type, inst.PipLocation, inst.MinTradeSize)
		}
	}
}

// ── Orders ────────────────────────────────────────────────────────────────────

func TestOANDA_MarketOrder(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	// 1000 units of EUR_USD (≈ $1000 notional at current rate)
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "EUR_USD",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("PlaceOrder market buy EUR_USD: %v", err)
	}
	t.Logf("order placed: id=%s  status=%s  filled=%s @ %s",
		result.ID, result.Status, result.FilledQty, result.FilledAvg)

	// GetOrder
	cx2, cancel2 := oandaCtx()
	defer cancel2()
	got, err := c.GetOrder(cx2, creds, result.ID)
	if err != nil {
		t.Logf("GetOrder: %v (market orders may not be retrievable after fill)", err)
	} else {
		t.Logf("order status: %s  filled=%s @ %s", got.Status, got.FilledQty, got.FilledAvg)
	}

	// Close the position to leave account clean
	cx3, cancel3 := oandaCtx()
	defer cancel3()
	if err := c.ClosePosition(cx3, creds, "EUR_USD"); err != nil {
		t.Logf("ClosePosition: %v (may already be closed)", err)
	} else {
		t.Log("position closed")
	}
}

func TestOANDA_LimitOrder_ThenCancel(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	// Place limit buy 5% below a rough EUR/USD price (≈ 1.10 → limit at 1.045)
	limitPrice := decimal.NewFromFloat(1.00) // well below market, won't fill

	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "EUR_USD",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromInt(1000),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit: %v", err)
	}
	t.Logf("limit order placed: id=%s  status=%s  price=%s", result.ID, result.Status, limitPrice)

	cx2, cancel2 := oandaCtx()
	defer cancel2()
	if err := c.CancelOrder(cx2, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	t.Logf("order %s cancelled", result.ID)

	cx3, cancel3 := oandaCtx()
	defer cancel3()
	got, err := c.GetOrder(cx3, creds, result.ID)
	if err != nil {
		t.Logf("GetOrder after cancel: %v", err)
	} else {
		t.Logf("final status: %s", got.Status)
	}
}

func TestOANDA_GetPendingOrders(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	orders, err := c.GetPendingOrders(cx, creds, "")
	if err != nil {
		t.Fatalf("GetPendingOrders: %v", err)
	}
	t.Logf("pending orders: %d", len(orders))
	for _, o := range orders {
		t.Logf("  id=%s  symbol=%s  side=%s  qty=%s  status=%s",
			o.ID, o.Symbol, o.Side, o.Qty, o.Status)
	}
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

func TestOANDA_ListOpenOrders(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	orders, err := c.ListOpenOrders(cx, creds, "")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	t.Logf("open orders (reconcile): %d", len(orders))
	for _, o := range orders {
		t.Logf("  id=%s  symbol=%s  side=%s  qty=%s  status=%s",
			o.ID, o.Symbol, o.Side, o.Qty, o.Status)
	}
}

func TestOANDA_ListPositions(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := oandaCtx()
	defer cancel()

	positions, err := c.ListPositions(cx, creds)
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	t.Logf("open positions (reconcile): %d", len(positions))
	for _, p := range positions {
		t.Logf("  %s: side=%s  qty=%s  avg=%s  pnl=%s",
			p.Symbol, p.Side, p.Qty, p.AvgPrice, p.UnrealPnL)
	}
}

// ── Fill streaming ────────────────────────────────────────────────────────────

func TestOANDA_SubscribeFills(t *testing.T) {
	c, creds := practiceClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	fills, err := c.SubscribeFills(cx, creds)
	if err != nil {
		t.Fatalf("SubscribeFills: %v", err)
	}
	t.Log("fill stream open — placing market order to trigger fill event...")

	cx2, cancel2 := oandaCtx()
	defer cancel2()
	result, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "EUR_USD",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Logf("PlaceOrder: %v", err)
	} else {
		t.Logf("order placed: id=%s  filled @ %s", result.ID, result.FilledAvg)
	}

	select {
	case f := <-fills:
		t.Logf("fill received: orderID=%s  symbol=%s  side=%s  qty=%s @ %s  at=%s",
			f.OrderID, f.Symbol, f.Side, f.FilledQty, f.FillPrice, f.Timestamp.Format(time.RFC3339))

		// Cleanup: close the position
		cx3, cancel3 := oandaCtx()
		defer cancel3()
		if err := c.ClosePosition(cx3, creds, "EUR_USD"); err != nil {
			t.Logf("ClosePosition: %v", err)
		}
	case <-cx.Done():
		t.Log("no fill received within 25s — check stream connectivity")
	}
}
