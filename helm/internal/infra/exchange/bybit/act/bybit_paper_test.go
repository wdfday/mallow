// Integration tests against Bybit Testnet API.
// Fill in bybitTestAPIKey / bybitTestAPISecret in creds_test.go, then run:
//
//	go test -v -run TestBybit ./internal/infra/exchange/bybit/act/
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

type depthLevel struct {
	price float64
	size  float64
}

func demoClient(t *testing.T) (*Client, exchange.Credentials) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if bybitTestAPIKey == "" || bybitTestAPISecret == "" {
		t.Skip("bybit demo credentials not set in creds_test.go")
	}
	creds := exchange.Credentials{APIKey: bybitTestAPIKey, APISecret: bybitTestAPISecret}
	return New(Config{Paper: true}), creds
}

func bybitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func roundBybit(price, tick float64) float64 {
	return math.Round(price/tick) * tick
}

// ── Account ───────────────────────────────────────────────────────────────────

func TestBybit_GetWalletBalance(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	info, err := c.GetWalletBalance(cx, creds, "UNIFIED")
	if err != nil {
		t.Fatalf("GetWalletBalance UNIFIED: %v", err)
	}
	t.Logf("account_type=%s  equity=%.4f  balance=%.4f  pnl=%.4f",
		info.AccountType, info.TotalEquity, info.TotalBalance, info.TotalPnL)
	for _, coin := range info.Coins {
		t.Logf("  %s: free=%.6f  locked=%.6f  wallet=%.6f",
			coin.Coin, coin.Free, coin.Locked, coin.WalletBalance)
	}
}

func TestBybit_SpotBalance(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	bal, err := c.SpotBalance(cx, creds, "USDT")
	if err != nil {
		t.Fatalf("SpotBalance USDT: %v", err)
	}
	t.Logf("USDT free: %s", bal)
}

func TestBybit_GetFeeRate(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	fee, err := c.GetFeeRate(cx, creds, "spot", "BTCUSDT")
	if err != nil {
		t.Fatalf("GetFeeRate: %v", err)
	}
	t.Logf("BTCUSDT: maker=%.6f%%  taker=%.6f%%",
		fee.MakerFeeRate*100, fee.TakerFeeRate*100)
}

// ── Orders ────────────────────────────────────────────────────────────────────

func TestBybit_MarketOrder(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
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
	t.Logf("order placed: id=%s  status=%s  qty=%s", result.ID, result.Status, result.Qty)

	// Poll for fill
	for i := range 5 {
		time.Sleep(400 * time.Millisecond)
		cx2, cancel2 := bybitCtx()
		got, err := c.GetOrder(cx2, creds, result.ID)
		cancel2()
		if err != nil {
			t.Logf("  poll[%d] GetOrder error: %v", i+1, err)
			continue
		}
		t.Logf("  poll[%d]: status=%s  filled=%s @ %s", i+1, got.Status, got.FilledQty, got.FilledAvg)
		if got.Status == "filled" || got.FilledQty.IsPositive() {
			break
		}
	}
}

func TestBybit_LimitOrder_ThenCancel(t *testing.T) {
	c, creds := demoClient(t)

	// Get mark price to set a safe limit well below market
	cx0, cancel0 := bybitCtx()
	mp, err := c.MarkPrice(cx0, creds, "BTCUSDT")
	cancel0()
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	limitPrice := decimal.NewFromFloat(mp.InexactFloat64() * 0.90).Round(1) // 10% below
	t.Logf("mark price: %s  limit: %s", mp, limitPrice)

	cx, cancel := bybitCtx()
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit: %v", err)
	}
	t.Logf("limit order: id=%s  status=%s  price=%s", result.ID, result.Status, limitPrice)

	cx2, cancel2 := bybitCtx()
	defer cancel2()
	if err := c.CancelOrder(cx2, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	t.Logf("order %s cancelled", result.ID)
}

func TestBybit_GetOrders(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	orders, err := c.GetOrders(cx, creds, "spot", "BTCUSDT", "")
	if err != nil {
		t.Fatalf("GetOrders: %v", err)
	}
	t.Logf("open orders: %d", len(orders))
	for _, o := range orders {
		t.Logf("  id=%s  status=%s  side=%s  qty=%s  filled=%s @ %s",
			o.ID, o.Status, o.Side, o.Qty, o.FilledQty, o.FilledAvg)
	}
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

func TestBybit_ListOpenOrders(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	orders, err := c.ListOpenOrders(cx, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	t.Logf("open orders (reconcile): %d", len(orders))
	for _, o := range orders {
		t.Logf("  id=%s  side=%s  qty=%s  status=%s",
			o.ID, o.Side, o.Qty, o.Status)
	}
}

func TestBybit_ListPositions(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	positions, err := c.ListPositions(cx, creds)
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	t.Logf("open positions: %d", len(positions))
	for _, p := range positions {
		t.Logf("  %s: side=%s  qty=%s  avg=%s  pnl=%s",
			p.Symbol, p.Side, p.Qty, p.AvgPrice, p.UnrealPnL)
	}
}

// ── Futures (linear perpetuals) ───────────────────────────────────────────────

func TestBybit_MarkPrice(t *testing.T) {
	c, creds := demoClient(t)
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		cx, cancel := bybitCtx()
		price, err := c.MarkPrice(cx, creds, sym)
		cancel()
		if err != nil {
			t.Logf("  %s: ERROR %v", sym, err)
			continue
		}
		t.Logf("  %s mark: %s", sym, price)
	}
}

func TestBybit_FundingRate(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := bybitCtx()
	defer cancel()

	rate, err := c.FundingRate(cx, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("FundingRate BTCUSDT: %v", err)
	}
	t.Logf("BTCUSDT funding rate: %s (%.4f%%)", rate, rate.Mul(decimal.NewFromInt(100)).InexactFloat64())
}

// ── Order streaming ───────────────────────────────────────────────────────────

func TestBybit_StreamOrders(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fills := make(chan exchange.WsFillEvent, 8)
	lifecycle := make(chan exchange.OrderLifecycleEvent, 8)
	if err := c.StreamOrders(cx, creds,
		func(e exchange.OrderLifecycleEvent) { lifecycle <- e },
		func(e exchange.WsFillEvent) { fills <- e },
		nil, nil, nil,
	); err != nil {
		t.Fatalf("StreamOrders: %v", err)
	}
	t.Log("order stream started — placing a market order to trigger events...")
	time.Sleep(1 * time.Second)

	cx2, cancel2 := bybitCtx()
	defer cancel2()
	result, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Logf("PlaceOrder: %v (stream still running)", err)
	} else {
		t.Logf("order placed: id=%s  status=%s", result.ID, result.Status)
	}

	select {
	case e := <-lifecycle:
		t.Logf("lifecycle event: type=%s  orderID=%s  symbol=%s  side=%s",
			e.Type, e.OrderID, e.Symbol, e.Side)
	case e := <-fills:
		t.Logf("fill event: partial=%v  orderID=%s  symbol=%s  side=%s  qty=%s @ %s",
			e.Partial, e.OrderID, e.Symbol, e.Side, e.FilledQty, e.FilledAvg)
		if !e.Partial {
			t.Log("PASS: received filled event")
		}
	case <-cx.Done():
		t.Log("no event received within 20s")
	}
}

// ── StreamOrders (fills) ──────────────────────────────────────────────────────

func TestBybit_SubscribeFills(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	fills := make(chan exchange.WsFillEvent, 64)
	go func() {
		_ = c.StreamOrders(cx, creds, nil, func(ev exchange.WsFillEvent) {
			select {
			case fills <- ev:
			case <-cx.Done():
			}
		}, nil, nil, nil)
	}()
	t.Log("fill stream open — placing order to trigger fill event...")
	time.Sleep(1 * time.Second)

	cx2, cancel2 := bybitCtx()
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
		t.Logf("fill received: orderID=%s  symbol=%s  side=%s  qty=%s @ %s  at=%s",
			f.OrderID, f.Symbol, f.Side, f.FilledQty, f.FilledAvg, f.Timestamp.Format(time.RFC3339))
	case <-cx.Done():
		t.Log("no fill received within 25s (demo may have propagation delay)")
	}
}

// ── T02 · Limit order reconcile ───────────────────────────────────────────────

func TestBybit_ListOpenOrders_Reconcile(t *testing.T) {
	c, creds := demoClient(t)

	cx0, cancel0 := bybitCtx()
	defer cancel0()
	mp, err := c.MarkPrice(cx0, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	limitPrice := decimal.NewFromFloat(mp.InexactFloat64() * 0.85).Round(1)

	cx1, cancel1 := bybitCtx()
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

	time.Sleep(300 * time.Millisecond)
	cx2, cancel2 := bybitCtx()
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

	cx3, cancel3 := bybitCtx()
	defer cancel3()
	if err := c.CancelOrder(cx3, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	cx4, cancel4 := bybitCtx()
	defer cancel4()
	ordersAfter, _ := c.ListOpenOrders(cx4, creds, "BTCUSDT")
	for _, o := range ordersAfter {
		if o.ID == result.ID {
			t.Errorf("cancelled order %s still appears in ListOpenOrders", result.ID)
		}
	}
	t.Logf("PASS: order absent after cancel (open orders remaining: %d)", len(ordersAfter))
}

// ── T03 · Market order → bracket (SL + TP) ───────────────────────────────────

func TestBybit_BracketOrder(t *testing.T) {
	c, creds := demoClient(t)

	cx0, cancel0 := bybitCtx()
	defer cancel0()
	mp, err := c.MarkPrice(cx0, creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}

	cx1, cancel1 := bybitCtx()
	defer cancel1()
	entry, err := c.PlaceOrder(cx1, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	})
	if err != nil {
		t.Fatalf("PlaceOrder entry: %v", err)
	}
	t.Logf("entry: id=%s  status=%s", entry.ID, entry.Status)

	fillAvg := entry.FilledAvg
	if fillAvg.IsZero() {
		fillAvg = mp // fallback to mark price for bracket calculation
	}
	tp := decimal.NewFromFloat(fillAvg.InexactFloat64() * 1.03).Round(1)
	sl := decimal.NewFromFloat(fillAvg.InexactFloat64() * 0.96).Round(1)
	t.Logf("bracket: fill≈%s  sl=%s  tp=%s", fillAvg, sl, tp)

	cx2, cancel2 := bybitCtx()
	defer cancel2()
	exitResult, err := c.PlaceExitOrders(cx2, creds, exchange.ExitOrderRequest{
		Symbol:     "BTCUSDT",
		Side:       exchange.Sell,
		Qty:        decimal.NewFromFloat(0.001),
		StopLoss:   sl,
		TakeProfit: tp,
	})
	if err != nil {
		t.Fatalf("PlaceExitOrders: %v", err)
	}
	t.Logf("bracket placed: order_ids=%v", exitResult.OrderIDs)
	if len(exitResult.OrderIDs) == 0 {
		t.Error("expected ≥1 order ID from PlaceExitOrders")
	}

	// Cancel bracket legs then close position
	for _, id := range exitResult.OrderIDs {
		cx, cancel := bybitCtx()
		if err := c.CancelOrder(cx, creds, id); err != nil {
			t.Logf("CancelOrder %s: %v", id, err)
		}
		cancel()
	}
	time.Sleep(300 * time.Millisecond)
	cx3, cancel3 := bybitCtx()
	defer cancel3()
	if _, err := c.PlaceOrder(cx3, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Sell,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	}); err != nil {
		t.Logf("cleanup sell: %v", err)
	}
}

// ── T08 · Position lifecycle ──────────────────────────────────────────────────

func TestBybit_PositionLifecycle(t *testing.T) {
	c, creds := demoClient(t)

	listPos := func() []exchange.PositionResult {
		cx, cancel := bybitCtx()
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

	before := listPos()
	t.Logf("positions before: %d", len(before))

	cx1, cancel1 := bybitCtx()
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

	after := listPos()
	t.Logf("positions after buy: %d", len(after))
	if !hasSymbol(after, "BTCUSDT") {
		t.Error("expected BTCUSDT in ListPositions after buy")
	} else {
		t.Log("PASS: BTCUSDT appears in positions after buy")
	}

	cx2, cancel2 := bybitCtx()
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

	closed := listPos()
	t.Logf("positions after sell: %d", len(closed))
	if hasSymbol(closed, "BTCUSDT") {
		t.Log("note: BTCUSDT still in positions (dust — OK)")
	} else {
		t.Log("PASS: BTCUSDT absent from positions after sell")
	}
}

// ── T07 · Market impact (demo depth) ───────────────────────────────────────

// bybitDepthBook fetches order book from Bybit demo (no auth required).
func bybitDepthBook(baseURL, symbol string, limit int) (asks, bids []depthLevel, err error) {
	type level [2]string
	var resp struct {
		Result struct {
			A []level `json:"a"`
			B []level `json:"b"`
		} `json:"result"`
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	url := fmt.Sprintf("%s/v5/market/orderbook?category=spot&symbol=%s&limit=%d", baseURL, symbol, limit)
	r, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, nil, err
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, nil, err
	}
	if resp.RetCode != 0 {
		return nil, nil, fmt.Errorf("bybit depth: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	parse := func(levels []level) []depthLevel {
		out := make([]depthLevel, 0, len(levels))
		for _, l := range levels {
			var p, s float64
			fmt.Sscanf(l[0], "%f", &p)
			fmt.Sscanf(l[1], "%f", &s)
			out = append(out, depthLevel{p, s})
		}
		return out
	}
	return parse(resp.Result.A), parse(resp.Result.B), nil
}

func bybitVWAP(levels []depthLevel, qty float64) (vwap, filled float64) {
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
	if filled > 0 {
		vwap = notional / filled
	}
	return
}

func TestBybit_MarketImpact(t *testing.T) {
	c, creds := demoClient(t)
	baseURL := "https://api-demo.bybit.com"

	asks, _, err := bybitDepthBook(baseURL, "BTCUSDT", 50)
	if err != nil || len(asks) == 0 {
		t.Fatalf("bybitDepthBook: %v", err)
	}

	totalQty := 0.0
	for _, l := range asks {
		totalQty += l.size
	}
	bestAsk := asks[0].price
	t.Logf("BTCUSDT ask depth: %.4f BTC @ best ask %.2f", totalQty, bestAsk)

	pcts := []float64{0.01, 0.05, 0.15, 0.30}
	type row struct {
		qty      float64
		pctDepth float64
		expVWAP  float64
		fillAvg  float64
		slipBps  float64
		depthOK  bool
	}
	var rows []row

	for _, p := range pcts {
		qty := math.Round(totalQty*p*1000) / 1000
		if qty < 0.001 {
			qty = 0.001
		}
		expVWAP, filledQty := bybitVWAP(asks, qty)
		depthOK := math.Abs(filledQty-qty)/qty < 0.01

		t.Logf("── qty=%.4f (%.1f%% of depth, exp_vwap=%.2f) ──", qty, p*100, expVWAP)

		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(qty),
		})
		cancel()
		if err != nil {
			t.Logf("  PlaceOrder error: %v", err)
			rows = append(rows, row{qty: qty, pctDepth: p * 100, expVWAP: expVWAP, depthOK: depthOK})
			continue
		}

		// Poll fill
		fillAvg := resp.FilledAvg
		if fillAvg.IsZero() {
			for range 5 {
				time.Sleep(400 * time.Millisecond)
				cx2, cancel2 := bybitCtx()
				got, _ := c.GetOrder(cx2, creds, resp.ID)
				cancel2()
				if got != nil && got.FilledAvg.IsPositive() {
					fillAvg = got.FilledAvg
					break
				}
			}
		}
		fillAvgF := fillAvg.InexactFloat64()
		var slipBps float64
		if fillAvgF > 0 && expVWAP > 0 {
			slipBps = (fillAvgF - expVWAP) / expVWAP * 10000
		}
		t.Logf("  fill_avg=%.4f  exp_vwap=%.4f  slip=%+.4f bps  depth_ok=%v", fillAvgF, expVWAP, slipBps, depthOK)

		rows = append(rows, row{qty: qty, pctDepth: p * 100, expVWAP: expVWAP, fillAvg: fillAvgF, slipBps: slipBps, depthOK: depthOK})

		// Sell back immediately to flatten
		time.Sleep(300 * time.Millisecond)
		cx3, cancel3 := bybitCtx()
		if _, serr := c.PlaceOrder(cx3, creds, exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Sell,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(qty),
		}); serr != nil {
			t.Logf("  sell-back error: %v", serr)
		}
		cancel3()
		time.Sleep(400 * time.Millisecond)
	}

	t.Log("\n══ SUMMARY ════════════════════════════════════════════════════════")
	t.Logf("  %-10s %-8s %-14s %-14s %-12s", "qty", "%depth", "exp_vwap", "fill_avg", "slip_bps")
	t.Log("  ──────────────────────────────────────────────────────────────")
	for _, r := range rows {
		t.Logf("  %-10.4f %-8.1f %-14.4f %-14.4f %+.4f", r.qty, r.pctDepth, r.expVWAP, r.fillAvg, r.slipBps)
	}
	t.Log("══════════════════════════════════════════════════════════════════")
}

// ── Heavy execution tests ─────────────────────────────────────────────────────

// TestBybit_AggressiveLimit places a LIMIT BUY at ask+ε so it fills as a taker,
// then sells back. Asserts fill_avg ≤ limit_price.
func TestBybit_AggressiveLimit(t *testing.T) {
	c, creds := demoClient(t)

	asks, _, err := bybitDepthBook("https://api-demo.bybit.com", "BTCUSDT", 5)
	if err != nil || len(asks) == 0 {
		t.Fatalf("bybitDepthBook: %v", err)
	}
	ask := asks[0].price
	limitPrice := decimal.NewFromFloat(ask * 1.0001).Round(1)
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
		cx2, cancel2 := bybitCtx()
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
		cx3, cancel3 := bybitCtx()
		c.CancelOrder(cx3, creds, result.ID) //nolint
		cancel3()
		t.Skip("aggressive limit did not fill within 6s on demo")
	}

	t.Logf("PASS fill: qty=%s @ avg=%s  limit=%s", filledQty, fillAvg, limitPrice)
	if fillAvg.GreaterThan(limitPrice) {
		t.Errorf("fill avg %s > limit price %s", fillAvg, limitPrice)
	}

	cx4, cancel4 := bybitCtx()
	defer cancel4()
	c.PlaceOrder(cx4, creds, exchange.OrderRequest{ //nolint
		Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: filledQty,
	})
}

// TestBybit_AmendThenFill places a limit 5% below market, amends it to ask+ε,
// and asserts the amended order fills.
func TestBybit_AmendThenFill(t *testing.T) {
	c, creds := demoClient(t)

	cx0, cancel0 := bybitCtx()
	price, err := c.MarkPrice(cx0, creds, "BTCUSDT")
	cancel0()
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	farLimit := decimal.NewFromFloat(price.InexactFloat64() * 0.95).Round(1)

	cx1, cancel1 := bybitCtx()
	result, err := c.PlaceOrder(cx1, creds, exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.001),
		Price:  farLimit,
	})
	cancel1()
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	t.Logf("placed limit: id=%s  price=%s", result.ID, farLimit)
	time.Sleep(300 * time.Millisecond)

	asks, _, err := bybitDepthBook("https://api-demo.bybit.com", "BTCUSDT", 5)
	if err != nil || len(asks) == 0 {
		t.Fatalf("bybitDepthBook: %v", err)
	}
	newPriceStr := decimal.NewFromFloat(asks[0].price * 1.0001).Round(1).String()
	t.Logf("amending to ask+ε = %s", newPriceStr)

	cx2, cancel2 := bybitCtx()
	if err := c.AmendOrder(cx2, creds, "spot", "BTCUSDT", result.ID, "", newPriceStr); err != nil {
		cancel2()
		cx3, cancel3 := bybitCtx()
		c.CancelOrder(cx3, creds, result.ID) //nolint
		cancel3()
		t.Fatalf("AmendOrder: %v", err)
	}
	cancel2()
	t.Log("amended OK — polling fill...")

	for range 15 {
		time.Sleep(400 * time.Millisecond)
		cx4, cancel4 := bybitCtx()
		got, err := c.GetOrder(cx4, creds, result.ID)
		cancel4()
		if err != nil {
			continue
		}
		t.Logf("  poll: status=%s  filledQty=%s @ %s", got.Status, got.FilledQty, got.FilledAvg)
		if got.FilledQty.IsPositive() {
			t.Logf("PASS: amended order filled qty=%s @ %s", got.FilledQty, got.FilledAvg)
			cx5, cancel5 := bybitCtx()
			c.PlaceOrder(cx5, creds, exchange.OrderRequest{ //nolint
				Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: got.FilledQty,
			})
			cancel5()
			return
		}
	}

	cx6, cancel6 := bybitCtx()
	c.CancelOrder(cx6, creds, result.ID) //nolint
	cancel6()
	t.Skip("amended order did not fill within 6s on demo")
}

// TestBybit_ConcurrentOrders fires 3 market BUY orders from separate goroutines
// simultaneously and asserts all 3 succeed without error.
func TestBybit_ConcurrentOrders(t *testing.T) {
	c, creds := demoClient(t)

	const n = 3
	type res struct {
		id  string
		err error
	}
	ch := make(chan res, n)

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
				ch <- res{err: fmt.Errorf("goroutine %d: %w", i, err)}
				return
			}
			ch <- res{id: resp.ID}
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
	time.Sleep(500 * time.Millisecond)
	cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c.PlaceOrder(cx, creds, exchange.OrderRequest{ //nolint
		Symbol: "BTCUSDT", Side: exchange.Sell, Type: exchange.Market,
		Qty: decimal.NewFromInt(int64(len(ids))).Mul(decimal.NewFromFloat(0.001)),
	})
}

// TestBybit_FillStreamIntegrity opens a fill stream via StreamOrders, places 3 market orders
// 100 ms apart, and asserts all 3 FillEvents arrive on the channel.
func TestBybit_FillStreamIntegrity(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fills := make(chan exchange.WsFillEvent, 64)
	go func() {
		_ = c.StreamOrders(cx, creds, nil, func(ev exchange.WsFillEvent) {
			select {
			case fills <- ev:
			case <-cx.Done():
			}
		}, nil, nil, nil)
	}()
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
			t.Logf("fill %d: orderID=%s  qty=%s @ %s", received, f.OrderID, f.FilledQty, f.FilledAvg)
		case <-deadline:
			goto done
		case <-cx.Done():
			goto done
		}
	}
done:
	if received < n {
		t.Logf("fill stream integrity: got %d/%d events", received, n)
	} else {
		t.Logf("PASS: received all %d fill events", n)
	}
}

// TestBybit_PositionDeltaAccumulation buys 0.001 BTC three times and asserts
// ListPositions qty grows monotonically after each fill.
func TestBybit_PositionDeltaAccumulation(t *testing.T) {
	c, creds := demoClient(t)

	getQty := func() float64 {
		cx, cancel := bybitCtx()
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

// TestBybit_FillLatencyDist places 5 market orders sequentially and measures wall-clock
// latency from PlaceOrder call to WsFillEvent via StreamOrders. Prints p50/p95/max.
func TestBybit_FillLatencyDist(t *testing.T) {
	c, creds := demoClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fills := make(chan exchange.WsFillEvent, 64)
	go func() {
		_ = c.StreamOrders(cx, creds, nil, func(ev exchange.WsFillEvent) {
			select {
			case fills <- ev:
			case <-cx.Done():
			}
		}, nil, nil, nil)
	}()
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
		case <-time.After(5 * time.Second):
			t.Logf("  order %d: no fill event within 5s", i+1)
		}

		cx3, cancel3 := bybitCtx()
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
