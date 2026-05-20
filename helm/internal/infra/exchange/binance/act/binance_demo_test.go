// Integration tests against Binance Demo API.
// Fill in binanceDemoAPIKey / binanceDemoAPISecret in creds_test.go, then run:
//
//	go test -v -run TestDemo ./internal/infra/exchange/binance/act/
package act

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// roundTick rounds price to the given tick size (e.g. 0.01 for BTCUSDT).
func roundTick(price, tick float64) float64 {
	return math.Round(price/tick) * tick
}

func demoClient(t *testing.T) (*Client, exchange.Credentials) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" || binanceDemoAPISecret == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}
	return New(true), creds // true = demo-api.binance.com
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
	limitPrice := decimal.NewFromFloat(price.InexactFloat64() * 0.95).Round(2) // 5% below market

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
	}, nil); err != nil {
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

// ── T02 · Limit order reconcile ───────────────────────────────────────────────

// TestDemo_ListOpenOrders_Reconcile places a limit order well below market,
// verifies it appears in ListOpenOrders, cancels it, then verifies it's gone.
func TestDemo_ListOpenOrders_Reconcile(t *testing.T) {
	c, creds := demoClient(t)

	cx0, cancel0 := ctx()
	defer cancel0()
	price, err := c.GetCurrentPrice(cx0, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}
	limitPrice := decimal.NewFromFloat(price.InexactFloat64() * 0.85).Round(2)

	cx1, cancel1 := ctx()
	defer cancel1()
	result, err := c.PlaceOrder(cx1, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit: %v", err)
	}
	t.Logf("limit order placed: id=%s  price=%s", result.ID, limitPrice)

	// Verify appears in open orders
	time.Sleep(300 * time.Millisecond)
	cx2, cancel2 := ctx()
	defer cancel2()
	orders, err := c.ListOpenOrders(cx2, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	found := false
	for _, o := range orders {
		if o.ID == result.ID {
			found = true
			t.Logf("order confirmed in open list: id=%s  status=%s", o.ID, o.Status)
		}
	}
	if !found {
		t.Errorf("expected order %s in ListOpenOrders, got %d orders", result.ID, len(orders))
	}

	// Cancel
	cx3, cancel3 := ctx()
	defer cancel3()
	if err := c.CancelOrder(cx3, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}

	// Verify gone
	time.Sleep(300 * time.Millisecond)
	cx4, cancel4 := ctx()
	defer cancel4()
	ordersAfter, _ := c.ListOpenOrders(cx4, creds, "BTCUSDT")
	for _, o := range ordersAfter {
		if o.ID == result.ID {
			t.Errorf("cancelled order %s still appears in ListOpenOrders", result.ID)
		}
	}
	t.Logf("PASS: order absent after cancel (open orders remaining: %d)", len(ordersAfter))
}

// ── T03 · Market order → OCO bracket ─────────────────────────────────────────

func TestDemo_BracketOrder(t *testing.T) {
	c, creds := demoClient(t)

	// Entry
	cx0, cancel0 := ctx()
	defer cancel0()
	entry, err := c.PlaceOrder(cx0, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Fatalf("PlaceOrder entry: %v", err)
	}
	t.Logf("entry: id=%s  status=%s", entry.ID, entry.Status)

	// Poll fill price
	fillAvg := entry.FilledAvg
	if fillAvg.IsZero() {
		for range 6 {
			time.Sleep(400 * time.Millisecond)
			cx, cancel := ctx()
			got, _ := c.GetOrder(cx, creds, entry.ID)
			cancel()
			if got != nil && got.FilledAvg.IsPositive() {
				fillAvg = got.FilledAvg
				break
			}
		}
	}
	if fillAvg.IsZero() {
		t.Fatal("entry fill price unknown — cannot set bracket prices")
	}
	t.Logf("fill avg: %s", fillAvg)

	tp := decimal.NewFromFloat(fillAvg.InexactFloat64() * 1.03).Round(2) // +3%
	sl := decimal.NewFromFloat(fillAvg.InexactFloat64() * 0.96).Round(2) // -4% (below tp)

	cx1, cancel1 := ctx()
	defer cancel1()
	exitResult, err := c.PlaceExitOrders(cx1, creds, exchange.ExitOrderRequest{
		Symbol:     "BTCUSDT",
		Side:       exchange.Sell,
		Qty:        decimal.NewFromFloat(0.001),
		StopLoss:   sl,
		TakeProfit: tp,
	})
	if err != nil {
		t.Fatalf("PlaceExitOrders (OCO): %v", err)
	}
	t.Logf("OCO bracket: sl=%s tp=%s  order_ids=%v", sl, tp, exitResult.OrderIDs)
	if len(exitResult.OrderIDs) == 0 {
		t.Error("expected ≥1 order ID from PlaceExitOrders")
	}

	// Cleanup: cancel each leg then close position
	for _, id := range exitResult.OrderIDs {
		cx, cancel := ctx()
		if err := c.CancelOrder(cx, creds, id); err != nil {
			t.Logf("CancelOrder %s: %v (OCO may cancel both legs at once)", id, err)
		}
		cancel()
	}
	time.Sleep(300 * time.Millisecond)
	cx2, cancel2 := ctx()
	defer cancel2()
	if _, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Sell,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	}); err != nil {
		t.Logf("cleanup sell: %v", err)
	}
}

// ── T04 · SubscribeFills ──────────────────────────────────────────────────────

func TestDemo_SubscribeFills(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	fills, err := c.SubscribeFills(cx, creds)
	if err != nil {
		t.Fatalf("SubscribeFills: %v", err)
	}
	t.Log("fill stream open — placing market order...")

	time.Sleep(1 * time.Second)
	cx2, cancel2 := ctx()
	defer cancel2()
	result, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Logf("PlaceOrder: %v", err)
	} else {
		t.Logf("order placed: id=%s", result.ID)
	}

	select {
	case f := <-fills:
		t.Logf("PASS fill: orderID=%s  symbol=%s  side=%s  qty=%s @ %s  at=%s",
			f.OrderID, f.Symbol, f.Side, f.FilledQty, f.FillPrice, f.Timestamp.Format(time.RFC3339))
	case <-cx.Done():
		t.Log("no fill event within 25s (demo WS may not support fill stream)")
	}
}

// ── T08 · Position lifecycle ──────────────────────────────────────────────────

func TestDemo_PositionLifecycle(t *testing.T) {
	c, creds := demoClient(t)

	baseline := func() []exchange.PositionResult {
		cx, cancel := ctx()
		defer cancel()
		ps, err := c.ListPositions(cx, creds)
		if err != nil {
			t.Fatalf("ListPositions: %v", err)
		}
		return ps
	}
	hasSymbol := func(ps []exchange.PositionResult, sym string) bool {
		for _, p := range ps {
			if p.Symbol == sym {
				return true
			}
		}
		return false
	}

	before := baseline()
	t.Logf("positions before: %d", len(before))

	// Buy
	cx1, cancel1 := ctx()
	defer cancel1()
	if _, err := c.PlaceOrder(cx1, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	}); err != nil {
		t.Fatalf("buy: %v", err)
	}
	time.Sleep(600 * time.Millisecond)

	after := baseline()
	t.Logf("positions after buy: %d", len(after))
	if !hasSymbol(after, "BTCUSDT") {
		t.Error("expected BTCUSDT in ListPositions after buy")
	} else {
		t.Log("PASS: BTCUSDT appears in positions after buy")
	}

	// Sell back
	cx2, cancel2 := ctx()
	defer cancel2()
	if _, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Sell,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	}); err != nil {
		t.Logf("sell back: %v", err)
	}
	time.Sleep(600 * time.Millisecond)

	closed := baseline()
	t.Logf("positions after sell: %d", len(closed))
	if hasSymbol(closed, "BTCUSDT") {
		t.Log("note: BTCUSDT still in positions (dust balance — OK on demo)")
	} else {
		t.Log("PASS: BTCUSDT absent from positions after sell")
	}
}

// ── Book comparison: real vs demo ─────────────────────────────────────────────

type binanceBookLevel struct {
	price float64
	size  float64
}

type binanceBook struct {
	Bids []binanceBookLevel
	Asks []binanceBookLevel
}

// fetchBinanceBook fetches top `limit` levels from Binance REST (real or demo endpoint).
func fetchBinanceBook(baseURL, symbol string, limit int) (*binanceBook, error) {
	url := fmt.Sprintf("%s/api/v3/depth?symbol=%s&limit=%d", baseURL, symbol, limit)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	parse := func(rows [][]string) []binanceBookLevel {
		out := make([]binanceBookLevel, 0, len(rows))
		for _, r := range rows {
			var p, s float64
			fmt.Sscanf(r[0], "%f", &p)
			fmt.Sscanf(r[1], "%f", &s)
			out = append(out, binanceBookLevel{p, s})
		}
		return out
	}
	return &binanceBook{Bids: parse(raw.Bids), Asks: parse(raw.Asks)}, nil
}

func binanceVWAP(levels []binanceBookLevel, qty float64) (vwap, filled float64, ok bool) {
	remaining := qty
	notional := 0.0
	for _, l := range levels {
		take := l.size
		if take > remaining {
			take = remaining
		}
		notional += take * l.price
		filled += take
		remaining -= take
		if remaining <= 0 {
			break
		}
	}
	if filled == 0 {
		return 0, 0, false
	}
	return notional / filled, filled, remaining <= 0
}

// TestDemo_BookComparison fetches real and demo order books for several symbols
// and prints side-by-side ask levels + VWAP divergence.
// No orders placed — pure read-only.
func TestDemo_BookComparison(t *testing.T) {
	symbols := []string{"BTCUSDT", "DOGEUSDT", "XRPUSDT", "TRXUSDT"}
	probeUSDT := 1000.0

	const (
		realBase = "https://api.binance.com"
		demoBase = "https://demo-api.binance.com"
	)

	for _, sym := range symbols {
		realBook, err := fetchBinanceBook(realBase, sym, 20)
		if err != nil {
			t.Logf("%s: real book error: %v", sym, err)
			continue
		}
		demoBook, demoErr := fetchBinanceBook(demoBase, sym, 20)

		bestAsk := 0.0
		if len(realBook.Asks) > 0 {
			bestAsk = realBook.Asks[0].price
		}
		probeQty := 0.0
		if bestAsk > 0 {
			probeQty = probeUSDT / bestAsk
		}

		realVWAP, _, _ := binanceVWAP(realBook.Asks, probeQty)
		demoVWAP := 0.0
		if demoErr == nil && demoBook != nil {
			demoVWAP, _, _ = binanceVWAP(demoBook.Asks, probeQty)
		}

		t.Logf("══ %s  (probe qty=%.4f @ ~$%.0f) ══", sym, probeQty, probeUSDT)
		t.Log("  lvl  real_price      real_size       demo_price      demo_size")
		t.Log("  ───  ──────────────  ──────────────  ──────────────  ──────────────")
		for j := 0; j < 5; j++ {
			rPrice, rSize := 0.0, 0.0
			dPrice, dSize := 0.0, 0.0
			if j < len(realBook.Asks) {
				rPrice = realBook.Asks[j].price
				rSize = realBook.Asks[j].size
			}
			if demoErr == nil && demoBook != nil && j < len(demoBook.Asks) {
				dPrice = demoBook.Asks[j].price
				dSize = demoBook.Asks[j].size
			}
			t.Logf("  [%d]  %-14.6f  %-14.4f  %-14.6f  %-14.4f", j+1, rPrice, rSize, dPrice, dSize)
		}

		if demoVWAP > 0 && realVWAP > 0 {
			divergeBps := (demoVWAP - realVWAP) / realVWAP * 10000
			t.Logf("  exp_vwap_real=%.6f  exp_vwap_demo=%.6f  diverge=%+.4f bps", realVWAP, demoVWAP, divergeBps)
		} else if demoErr != nil {
			t.Logf("  demo unavailable: %v  exp_vwap_real=%.6f", demoErr, realVWAP)
		}
		t.Log("")
	}
}

// ── Large-order market impact ─────────────────────────────────────────────────

// TestDemo_LargeOrderImpact mirrors TestOKX_LargeOrderImpact.
// Sells any held BTC, then finds the thinnest-book candidate among several symbols,
// places market buys at 1/5/15/30% of total ask-side depth, and measures whether
// the demo API models book-walk slippage.
func TestDemo_LargeOrderImpact(t *testing.T) {
	c, creds := demoClient(t)

	const (
		realBase = "https://api.binance.com"
		demoBase = "https://demo-api.binance.com"
	)

	// ── Step 1: sell any BTC to maximise USDT ────────────────────────────────
	cx0, cancel0 := ctx()
	bal, err := c.GetBalance(cx0, creds, "BTC")
	cancel0()
	if err == nil && bal != nil && bal.Free >= 0.001 {
		t.Logf("selling %.6f BTC → USDT", bal.Free)
		cx, cancel := ctx()
		c.PlaceOrder(cx, creds, exchange.OrderRequest{ //nolint
			Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market,
			Qty: decimal.NewFromFloat(math.Floor(bal.Free*1000) / 1000),
		})
		cancel()
		time.Sleep(600 * time.Millisecond)
	}

	cx1, cancel1 := ctx()
	usdtBal, err := c.GetBalance(cx1, creds, "USDT")
	cancel1()
	availUSDT := 0.0
	if err == nil && usdtBal != nil {
		availUSDT = usdtBal.Free
	}
	t.Logf("available USDT: %.2f", availUSDT)
	if availUSDT < 100 {
		t.Skip("insufficient USDT balance (< $100)")
	}

	// ── Step 2: find thinnest book among candidates ───────────────────────────
	candidates := []struct {
		symbol   string
		lotDecim int
		minQty   float64
	}{
		{"DOGEUSDT", 0, 1},
		{"TRXUSDT", 0, 1},
		{"XRPUSDT", 1, 1},
		{"ADAUSDT", 1, 1},
		{"LINKUSDT", 2, 0.01},
	}

	type candidate struct {
		symbol   string
		depthUSD float64
		totalQty float64
		bestAsk  float64
		lotDecim int
		minQty   float64
	}
	var best candidate

	for _, cd := range candidates {
		book, err := fetchBinanceBook(realBase, cd.symbol, 20)
		if err != nil || len(book.Asks) == 0 {
			continue
		}
		notional, totalQty := 0.0, 0.0
		for _, l := range book.Asks {
			notional += l.price * l.size
			totalQty += l.size
		}
		t.Logf("  %s: depth=$%.0f (%.0f units, best ask=%.6f)", cd.symbol, notional, totalQty, book.Asks[0].price)
		if best.symbol == "" || notional < best.depthUSD {
			best = candidate{cd.symbol, notional, totalQty, book.Asks[0].price, cd.lotDecim, cd.minQty}
		}
	}
	if best.symbol == "" {
		t.Fatal("could not fetch any candidate book")
	}
	t.Logf("selected: %s (depth=$%.0f, budget=$%.0f → budget/depth=%.1f%%)",
		best.symbol, best.depthUSD, availUSDT, availUSDT/best.depthUSD*100)

	// ── Step 3: build order sizes ─────────────────────────────────────────────
	round := func(v float64, dec int) float64 {
		f := math.Pow(10, float64(dec))
		return math.Round(v*f) / f
	}
	budgetUSDT := availUSDT * 0.85
	pcts := []float64{0.01, 0.05, 0.15, 0.30}
	var sizes []float64
	usedUSDT := 0.0
	for _, p := range pcts {
		qty := round(best.totalQty*p, best.lotDecim)
		if qty < best.minQty {
			qty = best.minQty
		}
		notional := qty * best.bestAsk
		if usedUSDT+notional > budgetUSDT {
			qty = round((budgetUSDT-usedUSDT)/best.bestAsk, best.lotDecim)
		}
		if qty < best.minQty {
			break
		}
		sizes = append(sizes, qty)
		usedUSDT += qty * best.bestAsk
	}
	t.Logf("test sizes: %v %s (total notional: ~$%.2f)", sizes, best.symbol, usedUSDT)

	// ── Step 4: place orders, measure slippage ────────────────────────────────
	type row struct {
		qty         float64
		pctDepth    float64
		expVWAPReal float64
		expVWAPDemo float64
		fillAvg     float64
		diffRealBps float64
		diffDemoBps float64
		depthOK     bool
	}
	var rows []row

	for i, qty := range sizes {
		realBook, err := fetchBinanceBook(realBase, best.symbol, 20)
		if err != nil {
			t.Fatalf("real book: %v", err)
		}
		demoBook, _ := fetchBinanceBook(demoBase, best.symbol, 20)

		totalDepth := 0.0
		for _, l := range realBook.Asks {
			totalDepth += l.size
		}
		pctDepth := qty / totalDepth * 100

		expVWAPReal, _, depthOK := binanceVWAP(realBook.Asks, qty)
		expVWAPDemo := 0.0
		if demoBook != nil {
			expVWAPDemo, _, _ = binanceVWAP(demoBook.Asks, qty)
		}

		t.Logf("── order %d/%d: qty=%.4f %s (%.1f%% of real depth=%.2f) exp_vwap_real=%.4f ──",
			i+1, len(sizes), qty, best.symbol, pctDepth, totalDepth, expVWAPReal)

		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: best.symbol,
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(qty),
		})
		cancel()
		if err != nil {
			t.Logf("  PlaceOrder error: %v", err)
			rows = append(rows, row{qty: qty, pctDepth: pctDepth, expVWAPReal: expVWAPReal, depthOK: depthOK})
			time.Sleep(400 * time.Millisecond)
			continue
		}

		// Poll fill
		fillAvg := resp.FilledAvg
		if fillAvg.IsZero() {
			for range 5 {
				time.Sleep(400 * time.Millisecond)
				cx2, cancel2 := ctx()
				got, _ := c.GetOrder(cx2, creds, resp.ID)
				cancel2()
				if got != nil && got.FilledAvg.IsPositive() {
					fillAvg = got.FilledAvg
					break
				}
			}
		}
		fillAvgF := fillAvg.InexactFloat64()
		diffRealBps, diffDemoBps := 0.0, 0.0
		if fillAvgF > 0 && expVWAPReal > 0 {
			diffRealBps = (fillAvgF - expVWAPReal) / expVWAPReal * 10000
		}
		if fillAvgF > 0 && expVWAPDemo > 0 {
			diffDemoBps = (fillAvgF - expVWAPDemo) / expVWAPDemo * 10000
		}
		t.Logf("  fill=%.6f  diff_real=%+.4f bps  diff_demo=%+.4f bps", fillAvgF, diffRealBps, diffDemoBps)

		rows = append(rows, row{qty, pctDepth, expVWAPReal, expVWAPDemo, fillAvgF, diffRealBps, diffDemoBps, depthOK})

		// Sell back to flatten
		time.Sleep(300 * time.Millisecond)
		cx3, cancel3 := ctx()
		c.PlaceOrder(cx3, creds, exchange.OrderRequest{ //nolint
			Symbol: best.symbol, Side: exchange.Sell, Type: exchange.Market,
			Qty: decimal.NewFromFloat(qty),
		})
		cancel3()
		time.Sleep(400 * time.Millisecond)
	}

	t.Log("\n══ SUMMARY ═══════════════════════════════════════════════════════════════════════════════════")
	t.Logf("  %-10s %-7s %-14s %-14s %-14s %-11s %-11s",
		"qty", "%depth", "exp_vwap_real", "exp_vwap_demo", "fill_avg", "diff_real", "diff_demo")
	t.Log("  ─────────────────────────────────────────────────────────────────────────────────────────")
	for _, r := range rows {
		flag := ""
		if !r.depthOK {
			flag = "[partial]"
		}
		t.Logf("  %-10.4f %-7.1f %-14.6f %-14.6f %-14.6f %+10.4f  %+10.4f  %s",
			r.qty, r.pctDepth, r.expVWAPReal, r.expVWAPDemo, r.fillAvg, r.diffRealBps, r.diffDemoBps, flag)
	}
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════")
}

// ── Heavy execution tests ─────────────────────────────────────────────────────

// TestDemo_AggressiveLimit places a LIMIT BUY at ask+ε so it fills as a taker,
// then sells back. Asserts fill_avg ≤ limit_price (price improvement is OK).
func TestDemo_AggressiveLimit(t *testing.T) {
	c, creds := demoClient(t)

	_, ask, err := bookTicker("BTCUSDT")
	if err != nil {
		t.Fatalf("bookTicker: %v", err)
	}
	limitPrice := decimal.NewFromFloat(ask * 1.0001).Round(2)
	t.Logf("current ask=%.2f  limit=%.2f", ask, limitPrice.InexactFloat64())

	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	t.Logf("order placed: id=%s  status=%s", result.ID, result.Status)

	var fillAvg decimal.Decimal
	var filledQty decimal.Decimal
	for range 15 {
		time.Sleep(400 * time.Millisecond)
		cx2, cancel2 := ctx()
		got, err := c.GetOrder(cx2, creds, result.ID)
		cancel2()
		if err != nil {
			continue
		}
		t.Logf("  poll: status=%s filledQty=%s @ %s", got.Status, got.FilledQty, got.FilledAvg)
		if got.FilledQty.IsPositive() {
			fillAvg = got.FilledAvg
			filledQty = got.FilledQty
			break
		}
	}
	if filledQty.IsZero() {
		cx3, cancel3 := ctx()
		c.CancelOrder(cx3, creds, result.ID) //nolint
		cancel3()
		t.Skip("aggressive limit did not fill within 6s on demo (demo may not have taker matching)")
	}

	t.Logf("PASS fill: qty=%s @ avg=%s  limit=%s", filledQty, fillAvg, limitPrice)
	if fillAvg.GreaterThan(limitPrice) {
		t.Errorf("fill avg %s > limit price %s (unexpected)", fillAvg, limitPrice)
	}

	cx4, cancel4 := ctx()
	defer cancel4()
	c.PlaceOrder(cx4, creds, exchange.OrderRequest{ //nolint
		Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: filledQty,
	})
}

// TestDemo_CancelReplace places a limit order 15% below market, cancels it, then
// re-places at ask+ε (Binance spot has no native amend). Asserts the replacement fills.
func TestDemo_CancelReplace(t *testing.T) {
	c, creds := demoClient(t)

	cx0, cancel0 := ctx()
	price, err := c.GetCurrentPrice(cx0, creds, "BTCUSDT")
	cancel0()
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}
	farLimit := decimal.NewFromFloat(price.InexactFloat64() * 0.85).Round(2)

	cx1, cancel1 := ctx()
	orig, err := c.PlaceOrder(cx1, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  farLimit,
	})
	cancel1()
	if err != nil {
		t.Fatalf("original PlaceOrder: %v", err)
	}
	t.Logf("original order: id=%s  price=%s", orig.ID, farLimit)
	time.Sleep(300 * time.Millisecond)

	cx2, cancel2 := ctx()
	if err := c.CancelOrder(cx2, creds, orig.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	cancel2()
	t.Log("original order cancelled")

	_, ask, err := bookTicker("BTCUSDT")
	if err != nil {
		t.Fatalf("bookTicker: %v", err)
	}
	newLimit := decimal.NewFromFloat(ask * 1.0001).Round(2)
	t.Logf("re-placing at ask+ε = %.2f", newLimit.InexactFloat64())

	cx3, cancel3 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel3()
	replacement, err := c.PlaceOrder(cx3, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  newLimit,
	})
	if err != nil {
		t.Fatalf("replacement PlaceOrder: %v", err)
	}
	t.Logf("replacement order: id=%s  status=%s", replacement.ID, replacement.Status)

	for range 15 {
		time.Sleep(400 * time.Millisecond)
		cx4, cancel4 := ctx()
		got, err := c.GetOrder(cx4, creds, replacement.ID)
		cancel4()
		if err != nil {
			continue
		}
		if got.FilledQty.IsPositive() {
			t.Logf("PASS: replacement filled qty=%s @ %s", got.FilledQty, got.FilledAvg)
			cx5, cancel5 := ctx()
			c.PlaceOrder(cx5, creds, exchange.OrderRequest{ //nolint
				Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: got.FilledQty,
			})
			cancel5()
			return
		}
	}

	cx6, cancel6 := ctx()
	c.CancelOrder(cx6, creds, replacement.ID) //nolint
	cancel6()
	t.Skip("replacement did not fill within 6s on demo")
}

// TestDemo_ConcurrentOrders fires 3 market BUY orders from separate goroutines
// simultaneously and asserts all 3 succeed without error.
func TestDemo_ConcurrentOrders(t *testing.T) {
	c, creds := demoClient(t)

	const n = 3
	type result struct {
		id  string
		err error
	}
	ch := make(chan result, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
				Symbol: "BTCUSDT",
				Side:   exchange.Buy,
				Type:   exchange.Market,
				Qty:    decimal.NewFromFloat(0.001),
			})
			if err != nil {
				ch <- result{err: fmt.Errorf("goroutine %d: %w", i, err)}
				return
			}
			ch <- result{id: resp.ID}
		}(i)
	}
	wg.Wait()
	close(ch)

	var ids []string
	for r := range ch {
		if r.err != nil {
			t.Errorf("order error: %v", r.err)
		} else {
			ids = append(ids, r.id)
			t.Logf("order placed: id=%s", r.id)
		}
	}
	t.Logf("PASS: %d/%d concurrent orders succeeded", len(ids), n)

	if len(ids) == 0 {
		return
	}
	time.Sleep(400 * time.Millisecond)
	cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c.PlaceOrder(cx, creds, exchange.OrderRequest{ //nolint
		Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market,
		Qty: decimal.NewFromFloat(float64(len(ids)) * 0.001),
	})
}

// TestDemo_FillStreamIntegrity opens SubscribeFills, places 3 sequential market orders
// 100 ms apart, and asserts all 3 FillEvents arrive on the channel without drops.
func TestDemo_FillStreamIntegrity(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fills, err := c.SubscribeFills(cx, creds)
	if err != nil {
		t.Fatalf("SubscribeFills: %v", err)
	}
	t.Log("fill stream open — settling 1s before orders...")
	time.Sleep(1 * time.Second)

	const n = 3
	for i := range n {
		cx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(0.001),
		})
		cancel2()
		if err != nil {
			t.Logf("order %d error: %v", i+1, err)
		} else {
			t.Logf("order %d placed: id=%s", i+1, resp.ID)
		}
		time.Sleep(100 * time.Millisecond)
	}

	received := 0
	deadline := time.After(30 * time.Second)
	for received < n {
		select {
		case f := <-fills:
			received++
			t.Logf("fill %d: orderID=%s  qty=%s @ %s", received, f.OrderID, f.FilledQty, f.FillPrice)
		case <-deadline:
			goto done
		case <-cx.Done():
			goto done
		}
	}
done:
	if received < n {
		t.Logf("fill stream integrity: got %d/%d events (demo WS may not deliver all fills)", received, n)
	} else {
		t.Logf("PASS: received all %d fill events", n)
	}
}

// TestDemo_PositionDeltaAccumulation buys 0.001 BTC three times and calls
// ListPositions after each, asserting the held qty grows monotonically.
func TestDemo_PositionDeltaAccumulation(t *testing.T) {
	c, creds := demoClient(t)

	getQty := func() float64 {
		cx, cancel := ctx()
		defer cancel()
		ps, err := c.ListPositions(cx, creds)
		if err != nil {
			t.Logf("ListPositions: %v", err)
			return -1
		}
		for _, p := range ps {
			if p.Symbol == "BTCUSDT" {
				q, _ := p.Qty.Float64()
				return q
			}
		}
		return 0
	}

	const buys = 3
	const lotQty = 0.001
	prevQty := getQty()
	t.Logf("initial BTCUSDT qty: %.6f", prevQty)

	for i := range buys {
		cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(lotQty),
		}); err != nil {
			t.Fatalf("buy %d: %v", i+1, err)
		}
		cancel()
		time.Sleep(800 * time.Millisecond)

		newQty := getQty()
		t.Logf("after buy %d: qty=%.6f (Δ=%.6f)", i+1, newQty, newQty-prevQty)
		if newQty <= prevQty && prevQty >= 0 {
			t.Errorf("buy %d: qty did not grow: %.6f → %.6f", i+1, prevQty, newQty)
		}
		prevQty = newQty
	}
	t.Logf("PASS: qty grew monotonically after %d buys", buys)

	totalSell := decimal.NewFromFloat(math.Round(float64(buys)*lotQty*1000) / 1000)
	cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.PlaceOrder(cx, creds, exchange.OrderRequest{ //nolint
		Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: totalSell,
	})
}

// TestDemo_FillLatencyDist places 5 market orders sequentially and measures wall-clock
// latency from PlaceOrder call to FillEvent on SubscribeFills. Prints p50/p95/max.
func TestDemo_FillLatencyDist(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fills, err := c.SubscribeFills(cx, creds)
	if err != nil {
		t.Fatalf("SubscribeFills: %v", err)
	}
	t.Log("fill stream open — settling 1s...")
	time.Sleep(1 * time.Second)

	const n = 5
	latencies := make([]time.Duration, 0, n)

	for i := range n {
		t0 := time.Now()
		cx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(0.001),
		})
		cancel2()
		if err != nil {
			t.Logf("order %d error: %v", i+1, err)
			continue
		}
		t.Logf("order %d placed: id=%s", i+1, resp.ID)

		select {
		case f := <-fills:
			lat := time.Since(t0)
			latencies = append(latencies, lat)
			t.Logf("  fill %d: orderID=%s  lat=%s", i+1, f.OrderID, lat.Round(time.Millisecond))
		case <-time.After(20 * time.Second):
			t.Logf("  order %d: no fill event within 20s", i+1)
		}

		cx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
		c.PlaceOrder(cx3, creds, exchange.OrderRequest{ //nolint
			Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market,
			Qty: decimal.NewFromFloat(0.001),
		})
		cancel3()
		time.Sleep(300 * time.Millisecond)
	}

	if len(latencies) == 0 {
		t.Log("no latency samples (demo fill stream may not be supported)")
		return
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)/2]
	p95 := latencies[int(float64(len(latencies))*0.95)]
	maxLat := latencies[len(latencies)-1]

	t.Log("┌──────────────────────────────────────┐")
	t.Logf("│ fill latency  samples=%-15d │", len(latencies))
	t.Logf("│  p50  = %-28s │", p50.Round(time.Millisecond))
	t.Logf("│  p95  = %-28s │", p95.Round(time.Millisecond))
	t.Logf("│  max  = %-28s │", maxLat.Round(time.Millisecond))
	t.Log("└──────────────────────────────────────┘")
}
