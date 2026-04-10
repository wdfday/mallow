// Integration tests against Alpaca Paper Trading API.
// Run with:
//
//	ALPACA_API_KEY=xxx ALPACA_API_SECRET=yyy go test -v -run TestPaper ./internal/infra/exchange/alpaca/action/
package action

import (
	"context"
	"os"
	"testing"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

func paperClient(t *testing.T) *Client {
	t.Helper()
	key := os.Getenv("ALPACA_API_KEY")
	secret := os.Getenv("ALPACA_API_SECRET")
	if key == "" || secret == "" {
		t.Skip("ALPACA_API_KEY / ALPACA_API_SECRET not set")
	}
	return New(Config{
		APIKey:    key,
		APISecret: secret,
		// BaseURL defaults to paper-api.alpaca.markets
	})
}

// ── Connectivity ──────────────────────────────────────────────────────────────

func TestPaper_Clock(t *testing.T) {
	c := paperClient(t)
	clock, err := c.GetClock()
	if err != nil {
		t.Fatalf("GetClock: %v", err)
	}
	t.Logf("market open: %v  next_open: %s  next_close: %s",
		clock.IsOpen, clock.NextOpen.Format(time.RFC3339), clock.NextClose.Format(time.RFC3339))
}

// ── Account ───────────────────────────────────────────────────────────────────

func TestPaper_GetAccount(t *testing.T) {
	c := paperClient(t)
	info, err := c.GetAccount()
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	t.Logf("status:        %s", info.Status)
	t.Logf("cash:          $%.2f", info.Cash)
	t.Logf("buying_power:  $%.2f", info.BuyingPower)
	t.Logf("equity:        $%.2f", info.Equity)
	t.Logf("pdt:           %v", info.PatternDayTrader)
	t.Logf("blocked:       %v", info.TradingBlocked)
}

func TestPaper_SpotBalance(t *testing.T) {
	c := paperClient(t)
	bal, err := c.SpotBalance(context.Background(), "USD")
	if err != nil {
		t.Fatalf("SpotBalance USD: %v", err)
	}
	t.Logf("USD cash: $%.2f", bal)
}

func TestPaper_SyncAccount(t *testing.T) {
	c := paperClient(t)
	snap, err := c.SyncAccount(context.Background(), nil)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	t.Logf("cash=%s  equity=%s  positions=%d", snap.Cash, snap.Equity, len(snap.Positions))
	for _, p := range snap.Positions {
		t.Logf("  %s: qty=%s  avg=%s  cur=%s", p.Symbol, p.Qty, p.AvgPrice, p.CurPrice)
	}
}

func TestPaper_GetPositions(t *testing.T) {
	c := paperClient(t)
	positions, err := c.GetPositions()
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) == 0 {
		t.Log("no open positions")
		return
	}
	for _, p := range positions {
		t.Logf("  %s: qty=%.4f  avg=%.4f  cur=%.4f  pnl=%.2f (%.2f%%)",
			p.Symbol, p.Qty, p.AvgEntryPrice, p.CurrentPrice, p.UnrealizedPL, p.UnrealizedPct*100)
	}
}

// ── Market data ───────────────────────────────────────────────────────────────

func TestPaper_GetCurrentPrice(t *testing.T) {
	c := paperClient(t)
	for _, sym := range []string{"AAPL", "SPY", "BTC/USD"} {
		price, err := c.GetCurrentPrice(context.Background(), sym)
		if err != nil {
			t.Logf("  %s: ERROR %v", sym, err)
			continue
		}
		t.Logf("  %s: $%s", sym, price)
	}
}

func TestPaper_GetLatestQuote(t *testing.T) {
	c := paperClient(t)
	for _, sym := range []string{"AAPL", "SPY"} {
		q, err := c.GetLatestQuote(sym)
		if err != nil {
			t.Logf("  %s: ERROR %v", sym, err)
			continue
		}
		spread := q.AskPrice - q.BidPrice
		spreadBps := spread / ((q.BidPrice + q.AskPrice) / 2) * 10000
		t.Logf("  %s: bid=%.4f (sz=%.0f)  ask=%.4f (sz=%.0f)  spread=%.4f (%.2f bps)",
			sym, q.BidPrice, q.BidSize, q.AskPrice, q.AskSize, spread, spreadBps)
	}
}

func TestPaper_GetSnapshot(t *testing.T) {
	c := paperClient(t)
	snap, err := c.GetSnapshot("AAPL")
	if err != nil {
		t.Fatalf("GetSnapshot AAPL: %v", err)
	}
	t.Logf("AAPL latest trade: $%.4f  daily_bar: o=%.2f h=%.2f l=%.2f c=%.2f v=%d",
		snap.LatestTrade,
		snap.DailyOpen, snap.DailyHigh, snap.DailyLow, snap.DailyClose,
		int(snap.DailyVolume))
}

func TestPaper_GetLatestBar(t *testing.T) {
	c := paperClient(t)
	bar, err := c.GetLatestBar("AAPL", marketdata.OneMin)
	if err != nil {
		t.Fatalf("GetLatestBar AAPL: %v", err)
	}
	t.Logf("AAPL M1: t=%s  o=%.4f h=%.4f l=%.4f c=%.4f v=%.0f",
		bar.Timestamp.Format("15:04:05"), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
}

func TestPaper_GetAsset(t *testing.T) {
	c := paperClient(t)
	for _, sym := range []string{"AAPL", "SPY", "TSLA"} {
		a, err := c.GetAsset(sym)
		if err != nil {
			t.Logf("  %s: ERROR %v", sym, err)
			continue
		}
		t.Logf("  %s: tradable=%v  marginable=%v  shortable=%v  fractionable=%v",
			sym, a.Tradable, a.Marginable, a.Shortable, a.Fractionable)
	}
}

// ── Orders ────────────────────────────────────────────────────────────────────

func TestPaper_MarketOrder(t *testing.T) {
	c := paperClient(t)

	result, err := c.PlaceOrder(context.Background(), exchange.OrderRequest{
		Symbol: "AAPL",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("PlaceOrder market buy AAPL: %v", err)
	}
	t.Logf("order placed: id=%s  status=%s  qty=%s  filled=%s @ $%s",
		result.ID, result.Status, result.Qty, result.FilledQty, result.FilledAvg)

	// GetOrder
	got, err := c.GetOrder(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	t.Logf("order status: %s  filledQty=%s @ $%s", got.Status, got.FilledQty, got.FilledAvg)
}

func TestPaper_LimitOrder_ThenCancel(t *testing.T) {
	c := paperClient(t)

	price, err := c.GetCurrentPrice(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}
	limitPrice := price.Mul(decimal.NewFromFloat(0.95)) // 5% below market

	result, err := c.PlaceOrder(context.Background(), exchange.OrderRequest{
		Symbol: "AAPL",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromInt(1),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit buy AAPL: %v", err)
	}
	t.Logf("limit order placed: id=%s  status=%s  price=$%s", result.ID, result.Status, limitPrice)

	if err := c.CancelOrder(context.Background(), result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	t.Logf("order %s cancelled", result.ID)

	got, err := c.GetOrder(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("GetOrder after cancel: %v", err)
	}
	t.Logf("final status: %s", got.Status)
}

func TestPaper_BracketOrder(t *testing.T) {
	c := paperClient(t)

	price, err := c.GetCurrentPrice(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}

	stopLoss := price.Mul(decimal.NewFromFloat(0.98))
	takeProfit := price.Mul(decimal.NewFromFloat(1.02))
	result, err := c.PlaceOrder(context.Background(), exchange.OrderRequest{
		Symbol:     "AAPL",
		Side:       exchange.Buy,
		Type:       exchange.Market,
		Qty:        decimal.NewFromInt(1),
		StopLoss:   stopLoss,
		TakeProfit: takeProfit,
	})
	if err != nil {
		t.Fatalf("PlaceOrder bracket: %v", err)
	}
	t.Logf("bracket order: id=%s  status=%s", result.ID, result.Status)
	t.Logf("  entry  @ market (~$%s)", price)
	t.Logf("  stop   @ $%s (-2%%)", stopLoss)
	t.Logf("  target @ $%s (+2%%)", takeProfit)

	// Cancel the bracket (closes all legs)
	time.Sleep(500 * time.Millisecond)
	if err := c.CancelAllOrders(); err != nil {
		t.Logf("CancelAllOrders: %v (may already be filled)", err)
	}
	if _, err := c.ClosePosition("AAPL", decimal.NewFromInt(1)); err != nil {
		t.Logf("ClosePosition: %v", err)
	}
}

// ── Slippage (vs quote mid) ───────────────────────────────────────────────────

func TestPaper_Slippage(t *testing.T) {
	c := paperClient(t)

	clock, _ := c.GetClock()
	if clock != nil && !clock.IsOpen {
		t.Skip("market is closed — slippage test requires live market")
	}

	type result struct {
		side      string
		bid       float64
		ask       float64
		mid       float64
		fillPrice float64
		slipBps   float64
	}
	var results []result

	for _, side := range []exchange.OrderSide{exchange.Buy, exchange.Sell} {
		if side == exchange.Sell {
			// Ensure position exists
			pos, _ := c.GetPosition("AAPL")
			if pos == nil || pos.Qty < 1 {
				c.PlaceOrder(context.Background(), exchange.OrderRequest{ //nolint
					Symbol: "AAPL", Side: exchange.Buy, Type: exchange.Market, Qty: decimal.NewFromInt(1),
				})
				time.Sleep(500 * time.Millisecond)
			}
		}

		q, err := c.GetLatestQuote("AAPL")
		if err != nil {
			t.Fatalf("GetLatestQuote: %v", err)
		}
		bid, ask := q.BidPrice, q.AskPrice
		mid := (bid + ask) / 2

		resp, err := c.PlaceOrder(context.Background(), exchange.OrderRequest{
			Symbol: "AAPL",
			Side:   side,
			Type:   exchange.Market,
			Qty:    decimal.NewFromInt(1),
		})
		if err != nil {
			t.Fatalf("PlaceOrder %s: %v", side, err)
		}

		// Poll until filled (paper orders may take a moment)
		fillPrice := resp.FilledAvg
		if fillPrice.IsZero() {
			for i := 0; i < 5; i++ {
				time.Sleep(500 * time.Millisecond)
				got, _ := c.GetOrder(context.Background(), resp.ID)
				if got != nil && got.FilledAvg.IsPositive() {
					fillPrice = got.FilledAvg
					break
				}
			}
		}
		fillPriceF := fillPrice.InexactFloat64()

		var slipBps float64
		if fillPriceF > 0 && mid > 0 {
			if side == exchange.Buy {
				slipBps = (fillPriceF - mid) / mid * 10000
			} else {
				slipBps = (mid - fillPriceF) / mid * 10000
			}
		}

		results = append(results, result{
			side: string(side), bid: bid, ask: ask, mid: mid,
			fillPrice: fillPriceF, slipBps: slipBps,
		})
	}

	t.Log("┌──────────────────────────────────────────────────────────────────────┐")
	t.Log("│  side   bid          ask          mid          fill          slip    │")
	t.Log("├──────────────────────────────────────────────────────────────────────┤")
	for _, r := range results {
		t.Logf("│  %-4s   %-11.4f  %-11.4f  %-11.4f  %-12.4f  %+.3f bps │",
			r.side, r.bid, r.ask, r.mid, r.fillPrice, r.slipBps)
	}
	t.Log("└──────────────────────────────────────────────────────────────────────┘")

	// GetOrders list
	orders, err := c.GetOrders(alpacasdk.GetOrdersRequest{Status: "all", Limit: 5})
	if err == nil {
		t.Logf("recent orders (%d):", len(orders))
		for _, o := range orders {
			t.Logf("  %s  %s %s  status=%s  filled=%s @ $%s",
				o.ID[:8], o.Symbol, o.Side, o.Status, o.FilledQty, o.FilledAvg)
		}
	}
}

// ── Order streaming ───────────────────────────────────────────────────────────

func TestPaper_StreamOrders(t *testing.T) {
	c := paperClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	events := make(chan exchange.OrderEvent, 8)
	if err := c.StreamOrders(cx, func(e exchange.OrderEvent) {
		events <- e
	}); err != nil {
		t.Fatalf("StreamOrders: %v", err)
	}
	t.Log("order stream started — placing a market order to trigger events...")

	time.Sleep(500 * time.Millisecond)
	c.PlaceOrder(context.Background(), exchange.OrderRequest{ //nolint
		Symbol: "AAPL", Side: exchange.Buy, Type: exchange.Market, Qty: decimal.NewFromInt(1),
	})

	select {
	case e := <-events:
		t.Logf("event received: type=%s orderID=%s  symbol=%s  side=%s  qty=%s @ $%s",
			e.Type, e.OrderID, e.Symbol, e.Side, e.FilledQty, e.FilledAvg)
	case <-cx.Done():
		t.Log("no event received within 15s")
	}
}
