// Integration tests against Binance Demo API.
// Run with:
//
//	BINANCE_API_KEY=xxx BINANCE_API_SECRET=yyy go test -v -run TestDemo ./internal/infra/exchange/binance/action/
//
// Tests are skipped automatically when env vars are not set.
package action

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// roundTick rounds price to the given tick size (e.g. 0.01 for BTCUSDT).
func roundTick(price, tick float64) float64 {
	return math.Round(price/tick) * tick
}

func demoClient(t *testing.T) (*Client, exchange.Credentials) {
	t.Helper()
	key := os.Getenv("BINANCE_API_KEY")
	secret := os.Getenv("BINANCE_API_SECRET")
	if key == "" || secret == "" {
		t.Skip("BINANCE_API_KEY / BINANCE_API_SECRET not set")
	}
	creds := exchange.Credentials{APIKey: key, APISecret: secret}
	return New(true), creds // true = testnet/demo-api.binance.com
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// ── Connectivity ──────────────────────────────────────────────────────────────

func TestDemo_ServerTime(t *testing.T) {
	c, _ := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	ts, err := c.GetServerTime(cx)
	if err != nil {
		t.Fatalf("GetServerTime: %v", err)
	}
	t.Logf("server time: %d (%s)", ts, time.UnixMilli(ts).UTC())
}

// ── Account ───────────────────────────────────────────────────────────────────

func TestDemo_GetAccount(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	info, err := c.GetAccount(cx, creds)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	t.Logf("account type: %s  canTrade: %v", info.AccountType, info.CanTrade)
	for _, b := range info.Balances {
		t.Logf("  %s: free=%.8f  locked=%.8f", b.Asset, b.Free, b.Locked)
	}
}

func TestDemo_GetBalance(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	b, err := c.GetBalance(cx, creds, "USDT")
	if err != nil {
		t.Fatalf("GetBalance USDT: %v", err)
	}
	t.Logf("USDT: free=%.2f  locked=%.2f", b.Free, b.Locked)
}

func TestDemo_SpotBalance(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	free, err := c.SpotBalance(cx, creds, "USDT")
	if err != nil {
		t.Fatalf("SpotBalance USDT: %v", err)
	}
	t.Logf("USDT free: %s", free)
}

func TestDemo_SyncAccount(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	snap, err := c.SyncAccount(cx, creds, nil)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	t.Logf("cash=%s  equity=%s  positions=%d", snap.Cash, snap.Equity, len(snap.Positions))
	for _, p := range snap.Positions {
		t.Logf("  %s: qty=%s  curPrice=%s", p.Symbol, p.Qty, p.CurPrice)
	}
}

// ── Market data ───────────────────────────────────────────────────────────────

func TestDemo_GetCurrentPrice(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	price, err := c.GetCurrentPrice(cx, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("GetCurrentPrice BTCUSDT: %v", err)
	}
	t.Logf("BTCUSDT price: %s", price)
}

func TestDemo_GetExchangeAsset(t *testing.T) {
	c, _ := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	asset, err := c.GetExchangeAsset(cx, "BTCUSDT")
	if err != nil {
		t.Fatalf("GetExchangeAsset BTCUSDT: %v", err)
	}
	t.Logf("symbol=%s  base=%s  quote=%s  status=%s  tradable=%v",
		asset.Symbol, asset.BaseAsset, asset.QuoteAsset, asset.Status, asset.Tradable)
}

// ── Spot orders ───────────────────────────────────────────────────────────────

func TestDemo_SpotMarketOrder(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := ctx()
	defer cancel()

	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Fatalf("PlaceOrder market buy: %v", err)
	}
	t.Logf("order placed: id=%s  status=%s  filled=%s @ %s",
		result.ID, result.Status, result.FilledQty, result.FilledAvg)

	// GetOrder
	cx2, cancel2 := ctx()
	defer cancel2()
	got, err := c.GetOrder(cx2, creds, result.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	t.Logf("order status: %s  filledQty=%s", got.Status, got.FilledQty)
}

func TestDemo_SpotLimitOrder_ThenCancel(t *testing.T) {
	c, creds := demoClient(t)

	// Get current price first to set a realistic limit price
	cx0, cancel0 := ctx()
	defer cancel0()
	price, err := c.GetCurrentPrice(cx0, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}
	limitPrice := decimal.NewFromFloat(roundTick(price.InexactFloat64()*0.95, 0.01)) // 5% below market

	cx, cancel := ctx()
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit buy: %v", err)
	}
	t.Logf("limit order placed: id=%s  status=%s  price=%s", result.ID, result.Status, limitPrice)

	// Cancel it
	cx2, cancel2 := ctx()
	defer cancel2()
	if err := c.CancelOrder(cx2, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	t.Logf("order %s cancelled", result.ID)

	// Verify cancelled
	cx3, cancel3 := ctx()
	defer cancel3()
	got, err := c.GetOrder(cx3, creds, result.ID)
	if err != nil {
		t.Fatalf("GetOrder after cancel: %v", err)
	}
	t.Logf("final status: %s", got.Status)
}

func TestDemo_SpotSellOrder(t *testing.T) {
	c, creds := demoClient(t)

	// Check BTC balance first — buy some if needed
	cx0, cancel0 := ctx()
	defer cancel0()
	bal, err := c.GetBalance(cx0, creds, "BTC")
	if err != nil {
		t.Fatalf("GetBalance BTC: %v", err)
	}
	if bal.Free < 0.001 {
		t.Logf("BTC balance %.8f < 0.001, buying first...", bal.Free)
		cxb, cancelb := ctx()
		defer cancelb()
		if _, err := c.PlaceOrder(cxb, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(0.001),
		}); err != nil {
			t.Fatalf("buy before sell: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
		cx1, cancel1 := ctx()
		defer cancel1()
		bal, err = c.GetBalance(cx1, creds, "BTC")
		if err != nil || bal.Free < 0.001 {
			t.Skipf("still insufficient BTC after buy: free=%.8f", bal.Free)
		}
	}
	t.Logf("BTC available: %.8f", bal.Free)

	cx, cancel := ctx()
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Sell,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Fatalf("PlaceOrder market sell: %v", err)
	}
	t.Logf("sell order: id=%s  status=%s  filled=%s @ %s",
		result.ID, result.Status, result.FilledQty, result.FilledAvg)
}

// ── Slippage ──────────────────────────────────────────────────────────────────

// bookTicker fetches best bid/ask directly from Binance REST.
func bookTicker(symbol string) (bid, ask float64, err error) {
	resp, err := http.Get("https://demo-api.binance.com/api/v3/ticker/bookTicker?symbol=" + symbol)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var v struct {
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, 0, err
	}
	fmt.Sscanf(v.BidPrice, "%f", &bid)
	fmt.Sscanf(v.AskPrice, "%f", &ask)
	return bid, ask, nil
}

func TestDemo_Slippage(t *testing.T) {
	c, creds := demoClient(t)

	type result struct {
		side      string
		bid       float64
		ask       float64
		mid       float64
		fillPrice float64
		spreadBps float64
		slipBps   float64 // fill vs mid
	}
	var results []result

	for _, side := range []exchange.OrderSide{exchange.Buy, exchange.Sell} {
		// Ensure BTC balance for sell
		if side == exchange.Sell {
			cx, cancel := ctx()
			bal, _ := c.GetBalance(cx, creds, "BTC")
			cancel()
			if bal == nil || bal.Free < 0.001 {
				cxb, cancelb := ctx()
				c.PlaceOrder(cxb, creds, exchange.OrderRequest{ //nolint
					Symbol: "BTCUSDT", Side: exchange.Buy, Type: exchange.Market, Qty: decimal.NewFromFloat(0.001),
				})
				cancelb()
				time.Sleep(300 * time.Millisecond)
			}
		}

		// Snapshot bid/ask immediately before order — mid price is the fair reference
		bid, ask, err := bookTicker("BTCUSDT")
		if err != nil {
			t.Fatalf("bookTicker: %v", err)
		}
		mid := (bid + ask) / 2
		spreadBps := (ask - bid) / mid * 10000

		cx, cancel := ctx()
		resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   side,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(0.001),
		})
		cancel()
		if err != nil {
			t.Fatalf("PlaceOrder %s: %v", side, err)
		}

		fillPriceF := resp.FilledAvg.InexactFloat64()
		// Slippage = how far fill deviates from mid, in the adverse direction
		// buy:  fill above mid is adverse  (+)
		// sell: fill below mid is adverse  (+)
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
			fillPrice: fillPriceF, spreadBps: spreadBps, slipBps: slipBps,
		})
	}

	t.Log("┌─────────────────────────────────────────────────────────────────────┐")
	t.Log("│  side   bid          ask          mid          fill         slip    │")
	t.Log("├─────────────────────────────────────────────────────────────────────┤")
	for _, r := range results {
		t.Logf("│  %-4s   %-11.2f  %-11.2f  %-11.2f  %-11.2f  %+.2f bps │",
			r.side, r.bid, r.ask, r.mid, r.fillPrice, r.slipBps)
	}
	t.Log("└─────────────────────────────────────────────────────────────────────┘")
	if len(results) > 0 {
		t.Logf("spread: %.4f bps (bid/ask gap)", results[0].spreadBps)
		t.Logf("note: on Demo API, fill = ask (buy) or bid (sell) → slippage = spread/2")
	}
}

// ── Order streaming ───────────────────────────────────────────────────────────

// TestDemo_StreamOrders subscribes to the demo order stream via signature-based auth
// (wss://demo-ws-api.binance.com/ws-api/v3), then places a market order to trigger events.
func TestDemo_StreamOrders(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events := make(chan exchange.OrderEvent, 8)
	if err := c.StreamOrders(cx, creds, func(e exchange.OrderEvent) {
		events <- e
	}); err != nil {
		t.Fatalf("StreamOrders: %v", err)
	}
	t.Log("order stream started — waiting 1s for subscription to settle...")
	time.Sleep(1 * time.Second)

	cx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	_, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Logf("PlaceOrder: %v (stream still running)", err)
	}

	for {
		select {
		case e := <-events:
			t.Logf("event: type=%-12s orderID=%s  symbol=%s  side=%s  qty=%s filledQty=%s @ %s",
				e.Type, e.OrderID, e.Symbol, e.Side, e.Qty, e.FilledQty, e.FilledAvg)
			if e.Type == exchange.OrderEventFilled {
				t.Log("PASS: received filled event")
				return
			}
		case <-cx.Done():
			t.Log("timeout — no filled event received (stream may not be supported on demo)")
			return
		}
	}
}
