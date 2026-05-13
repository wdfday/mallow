// Integration tests against OKX Simulated Trading API.
// Run with:
//
//	OKX_API_KEY=xxx OKX_API_SECRET=yyy OKX_PASSPHRASE=zzz go test -v -run TestOKX ./internal/infra/exchange/okx/action/
package act

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

	"mallow/helm/internal/infra/exchange"
)

func paperOKXClient(t *testing.T) (*Client, exchange.Credentials) {
	t.Helper()
	key := os.Getenv("OKX_API_KEY")
	secret := os.Getenv("OKX_API_SECRET")
	passphrase := os.Getenv("OKX_PASSPHRASE")
	if key == "" || secret == "" || passphrase == "" {
		t.Skip("OKX_API_KEY / OKX_API_SECRET / OKX_PASSPHRASE not set")
	}
	creds := exchange.Credentials{APIKey: key, APISecret: secret, Passphrase: passphrase}
	return New(Config{Paper: true}), creds
}

func okxCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// roundOKX rounds to tick size (e.g. 0.1 for BTC-USDT).
func roundOKX(price, tick float64) float64 {
	return math.Round(price/tick) * tick
}

// ── Account ───────────────────────────────────────────────────────────────────

func TestOKX_GetBalance(t *testing.T) {
	c, creds := paperOKXClient(t)
	cx, cancel := okxCtx()
	defer cancel()

	info, err := c.GetBalance(cx, creds)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	t.Logf("total_equity: $%.2f", info.TotalEquity)
	for _, b := range info.Balances {
		t.Logf("  %s: avail=%.6f  frozen=%.6f  equity=%.6f",
			b.Currency, b.Available, b.Frozen, b.Equity)
	}
}

func TestOKX_SyncAccount(t *testing.T) {
	c, creds := paperOKXClient(t)
	cx, cancel := okxCtx()
	defer cancel()

	snap, err := c.SyncAccount(cx, creds, nil)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	t.Logf("cash=%s  equity=%s  positions=%d", snap.Cash, snap.Equity, len(snap.Positions))
	for _, p := range snap.Positions {
		t.Logf("  %s: qty=%s  cur=%s", p.Symbol, p.Qty, p.CurPrice)
	}
}

func TestOKX_GetPositions(t *testing.T) {
	c, creds := paperOKXClient(t)
	cx, cancel := okxCtx()
	defer cancel()

	positions, err := c.GetPositions(cx, creds, "")
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) == 0 {
		t.Log("no open positions")
		return
	}
	for _, p := range positions {
		t.Logf("  %s [%s]: pos=%.6f  avg=%.4f  upl=%.4f",
			p.InstID, p.PosSide, p.Pos, p.AvgPx, p.Upl)
	}
}

// ── Market data ───────────────────────────────────────────────────────────────

func TestOKX_GetCurrentPrice(t *testing.T) {
	c, creds := paperOKXClient(t)
	for _, sym := range []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"} {
		cx, cancel := okxCtx()
		price, err := c.GetCurrentPrice(cx, creds, sym)
		cancel()
		if err != nil {
			t.Logf("  %s: ERROR %v", sym, err)
			continue
		}
		t.Logf("  %s: $%s", sym, price)
	}
}

func TestOKX_GetInstrument(t *testing.T) {
	c, _ := paperOKXClient(t)
	cx, cancel := okxCtx()
	defer cancel()

	inst, err := c.GetInstrument(cx, "SPOT", "BTC-USDT")
	if err != nil {
		t.Fatalf("GetInstrument: %v", err)
	}
	t.Logf("BTC-USDT: base=%s  quote=%s  state=%s  tickSz=%s  lotSz=%s",
		inst.BaseCcy, inst.QuoteCcy, inst.State, inst.TickSz, inst.LotSz)
}

// ── Orders ────────────────────────────────────────────────────────────────────

func TestOKX_MarketOrder(t *testing.T) {
	c, creds := paperOKXClient(t)
	cx, cancel := okxCtx()
	defer cancel()

	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTC-USDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.01),
	})
	if err != nil {
		t.Fatalf("PlaceOrder market buy: %v", err)
	}
	t.Logf("order placed: id=%s  status=%s  qty=%s", result.ID, result.Status, result.Qty)

	// GetOrder
	cx2, cancel2 := okxCtx()
	defer cancel2()
	time.Sleep(300 * time.Millisecond)
	got, err := c.GetOrder(cx2, creds, result.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	t.Logf("order status: %s  filledQty=%s @ $%s", got.Status, got.FilledQty, got.FilledAvg)
}

func TestOKX_LimitOrder_ThenCancel(t *testing.T) {
	c, creds := paperOKXClient(t)

	cx0, cancel0 := okxCtx()
	defer cancel0()
	price, err := c.GetCurrentPrice(cx0, creds, "BTC-USDT")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}
	limitPrice := decimal.NewFromFloat(roundOKX(price.InexactFloat64()*0.95, 0.1)) // 5% below market

	cx, cancel := okxCtx()
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTC-USDT",
		Side:   exchange.Buy,
		Type:   exchange.Limit,
		Qty:    decimal.NewFromFloat(0.01),
		Price:  limitPrice,
	})
	if err != nil {
		t.Fatalf("PlaceOrder limit buy: %v", err)
	}
	t.Logf("limit order placed: id=%s  status=%s  price=%s", result.ID, result.Status, limitPrice)

	cx2, cancel2 := okxCtx()
	defer cancel2()
	if err := c.CancelOrder(cx2, creds, result.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	t.Logf("order %s cancelled", result.ID)

	cx3, cancel3 := okxCtx()
	defer cancel3()
	time.Sleep(300 * time.Millisecond)
	got, err := c.GetOrder(cx3, creds, result.ID)
	if err != nil {
		t.Fatalf("GetOrder after cancel: %v", err)
	}
	t.Logf("final status: %s", got.Status)
}

func TestOKX_BracketOrder(t *testing.T) {
	c, creds := paperOKXClient(t)

	cx0, cancel0 := okxCtx()
	defer cancel0()
	price, err := c.GetCurrentPrice(cx0, creds, "BTC-USDT")
	if err != nil {
		t.Fatalf("GetCurrentPrice: %v", err)
	}

	priceF := price.InexactFloat64()
	stopLoss := decimal.NewFromFloat(roundOKX(priceF*0.98, 0.1))
	takeProfit := decimal.NewFromFloat(roundOKX(priceF*1.02, 0.1))

	cx, cancel := okxCtx()
	defer cancel()
	result, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
		Symbol: "BTC-USDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.01),
	})
	if err != nil {
		t.Fatalf("PlaceOrder entry: %v", err)
	}
	t.Logf("entry order: id=%s  status=%s", result.ID, result.Status)

	// Place exit bracket (SL/TP) post-fill via PlaceExitOrders.
	exitResult, err := c.PlaceExitOrders(cx, creds, exchange.ExitOrderRequest{
		Symbol:     "BTC-USDT",
		Side:       exchange.Sell,
		Qty:        decimal.NewFromFloat(0.01),
		StopLoss:   stopLoss,
		TakeProfit: takeProfit,
	})
	if err != nil {
		t.Fatalf("PlaceExitOrders bracket: %v", err)
	}
	t.Logf("  stop   @ $%s (-2%%)  algo_ids=%v", stopLoss, exitResult.OrderIDs)
	t.Logf("  target @ $%s (+2%%)", takeProfit)
}

// ── Slippage ──────────────────────────────────────────────────────────────────

type bookLevel struct {
	price float64
	size  float64
}

type orderBook struct {
	Bids []bookLevel
	Asks []bookLevel
}

// expectedVWAP computes the average fill price when sweeping `qty` through levels.
// Returns (vwap, filledQty, ok). ok=false if book has insufficient depth.
func expectedVWAP(levels []bookLevel, qty float64) (vwap, filled float64, ok bool) {
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

func okxBookTicker(instID string) (bid, ask float64, err error) {
	resp, err := http.Get("https://www.okx.com/api/v5/market/books?instId=" + instID + "&sz=1")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var v struct {
		Data []struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, 0, err
	}
	if len(v.Data) == 0 || len(v.Data[0].Bids) == 0 || len(v.Data[0].Asks) == 0 {
		return 0, 0, fmt.Errorf("empty book")
	}
	fmt.Sscanf(v.Data[0].Bids[0][0], "%f", &bid)
	fmt.Sscanf(v.Data[0].Asks[0][0], "%f", &ask)
	return bid, ask, nil
}

func okxDeepBook(instID string, depth int) (*orderBook, error) {
	return fetchOKXBook("https://www.okx.com", instID, depth, false)
}

// okxDemoDeepBook fetches the order book via the simulated-trading endpoint
// (x-simulated-trading: 1 header). OKX may return a different book for demo.
func okxDemoDeepBook(c *Client, instID string, depth int) (*orderBook, error) {
	var v struct {
		Data []struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
			Ts   string     `json:"ts"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/api/v5/market/books?instId=%s&sz=%d", instID, depth)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.doRequest(ctx, exchange.Credentials{}, "GET", path, nil, &v); err != nil {
		return nil, err
	}
	if len(v.Data) == 0 {
		return nil, fmt.Errorf("empty demo book")
	}
	return parseOKXBook(v.Data[0].Bids, v.Data[0].Asks), nil
}

func fetchOKXBook(baseURL, instID string, depth int, _ bool) (*orderBook, error) {
	resp, err := http.Get(fmt.Sprintf("%s/api/v5/market/books?instId=%s&sz=%d", baseURL, instID, depth))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Data []struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	if len(v.Data) == 0 {
		return nil, fmt.Errorf("empty book")
	}
	return parseOKXBook(v.Data[0].Bids, v.Data[0].Asks), nil
}

func parseOKXBook(bids, asks [][]string) *orderBook {
	parse := func(rows [][]string) []bookLevel {
		levels := make([]bookLevel, 0, len(rows))
		for _, r := range rows {
			var p, s float64
			fmt.Sscanf(r[0], "%f", &p)
			fmt.Sscanf(r[1], "%f", &s)
			levels = append(levels, bookLevel{p, s})
		}
		return levels
	}
	return &orderBook{Bids: parse(bids), Asks: parse(asks)}
}

func TestOKX_Slippage(t *testing.T) {
	c, creds := paperOKXClient(t)

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
			// Ensure BTC balance for sell
			cx, cancel := okxCtx()
			bal, _ := c.GetBalance(cx, creds)
			cancel()
			hasBTC := false
			if bal != nil {
				for _, b := range bal.Balances {
					if b.Currency == "BTC" && b.Available >= 0.01 {
						hasBTC = true
						break
					}
				}
			}
			if !hasBTC {
				cx2, cancel2 := okxCtx()
				c.PlaceOrder(cx2, creds, exchange.OrderRequest{ //nolint
					Symbol: "BTC-USDT", Side: exchange.Buy, Type: exchange.Market, Qty: decimal.NewFromFloat(0.01),
				})
				cancel2()
				time.Sleep(500 * time.Millisecond)
			}
		}

		bid, ask, err := okxBookTicker("BTC-USDT")
		if err != nil {
			t.Fatalf("okxBookTicker: %v", err)
		}
		mid := (bid + ask) / 2

		cx, cancel := okxCtx()
		resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: "BTC-USDT",
			Side:   side,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(0.01),
		})
		cancel()
		if err != nil {
			t.Fatalf("PlaceOrder %s: %v", side, err)
		}

		// Poll fill price
		fillPrice := resp.FilledAvg
		if fillPrice.IsZero() {
			for i := 0; i < 5; i++ {
				time.Sleep(300 * time.Millisecond)
				cx2, cancel2 := okxCtx()
				got, _ := c.GetOrder(cx2, creds, resp.ID)
				cancel2()
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

	t.Log("┌─────────────────────────────────────────────────────────────────────┐")
	t.Log("│  side   bid          ask          mid          fill         slip    │")
	t.Log("├─────────────────────────────────────────────────────────────────────┤")
	for _, r := range results {
		t.Logf("│  %-4s   %-11.2f  %-11.2f  %-11.2f  %-11.2f  %+.4f bps │",
			r.side, r.bid, r.ask, r.mid, r.fillPrice, r.slipBps)
	}
	t.Log("└─────────────────────────────────────────────────────────────────────┘")
}

// ── Order streaming ───────────────────────────────────────────────────────────

func TestOKX_StreamOrders(t *testing.T) {
	c, creds := paperOKXClient(t)
	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events := make(chan exchange.OrderEvent, 8)
	if err := c.StreamOrders(cx, creds, func(e exchange.OrderEvent) {
		events <- e
	}); err != nil {
		t.Fatalf("StreamOrders: %v", err)
	}
	t.Log("order stream started — placing a market order to trigger events...")

	time.Sleep(1 * time.Second)

	cx2, cancel2 := okxCtx()
	defer cancel2()
	result, err := c.PlaceOrder(cx2, creds, exchange.OrderRequest{
		Symbol: "BTC-USDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.01),
	})
	if err != nil {
		t.Logf("PlaceOrder: %v (stream still running)", err)
	} else {
		t.Logf("order placed: id=%s  status=%s", result.ID, result.Status)
	}

	select {
	case e := <-events:
		t.Logf("✓ event received: type=%s orderID=%s  symbol=%s  side=%s  qty=%s @ %s",
			e.Type, e.OrderID, e.Symbol, e.Side, e.FilledQty, e.FilledAvg)
	case <-cx.Done():
		t.Log("no event received within 20s")
	}
}

// ── Book comparison: real vs demo ────────────────────────────────────────────

// TestOKX_BookComparison fetches both real and demo order books for a set of
// symbols and prints side-by-side ask top-5 levels + expected VWAP divergence.
// No orders are placed. Useful to see how much the simulated book differs from live.
func TestOKX_BookComparison(t *testing.T) {
	c, _ := paperOKXClient(t)
	symbols := []string{"BTC-USDT", "TRX-USDT", "DOGE-USDT", "XRP-USDT"}
	// qty to probe: ~$1000 notional worth at rough prices
	probeUSDT := 1000.0

	for _, sym := range symbols {
		realBook, err := okxDeepBook(sym, 20)
		if err != nil {
			t.Logf("%s: real book error: %v", sym, err)
			continue
		}
		demoBook, demoErr := okxDemoDeepBook(c, sym, 20)

		bestAsk := 0.0
		if len(realBook.Asks) > 0 {
			bestAsk = realBook.Asks[0].price
		}
		probeQty := 0.0
		if bestAsk > 0 {
			probeQty = probeUSDT / bestAsk
		}

		realVWAP, _, _ := expectedVWAP(realBook.Asks, probeQty)
		demoVWAP := 0.0
		if demoErr == nil && demoBook != nil {
			demoVWAP, _, _ = expectedVWAP(demoBook.Asks, probeQty)
		}

		t.Logf("══ %s  (probe qty=%.4f @ ~$%.2f) ══", sym, probeQty, probeUSDT)

		// Print side-by-side top-5
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

// TestOKX_LargeOrderImpact sells BTC to get USDT, scans several candidate symbols
// for the thinnest order book relative to the budget, then places market buys at
// 1%, 5%, 15%, 30%, 60% of total ask depth to measure whether OKX simulated
// trading models book-walk slippage.
func TestOKX_LargeOrderImpact(t *testing.T) {
	c, creds := paperOKXClient(t)

	// ── Step 1: sell BTC if we have it ────────────────────────────────────────
	cx0, cancel0 := context.WithTimeout(context.Background(), 10*time.Second)
	bal, err := c.GetBalance(cx0, creds)
	cancel0()
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	availUSDT := 0.0
	btcAvail := 0.0
	for _, b := range bal.Balances {
		switch b.Currency {
		case "USDT":
			availUSDT = b.Available
		case "BTC":
			btcAvail = b.Available
		}
	}
	t.Logf("balance: USDT=%.2f  BTC=%.4f", availUSDT, btcAvail)

	if btcAvail >= 0.001 {
		t.Logf("selling %.4f BTC → USDT", btcAvail)
		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: "BTC-USDT",
			Side:   exchange.Sell,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(btcAvail),
		})
		cancel()
		if err != nil {
			t.Logf("sell BTC: %v (continuing)", err)
		} else {
			time.Sleep(800 * time.Millisecond)
			cx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
			bal2, _ := c.GetBalance(cx2, creds)
			cancel2()
			if bal2 != nil {
				for _, b := range bal2.Balances {
					if b.Currency == "USDT" {
						availUSDT = b.Available
					}
				}
			}
		}
	}
	t.Logf("available USDT after BTC sell: %.2f", availUSDT)
	if availUSDT < 100 {
		t.Skip("insufficient USDT balance (< $100)")
	}

	// ── Step 2: find thinnest book relative to our budget ─────────────────────
	candidates := []string{
		"DOGE-USDT", "TRX-USDT", "XRP-USDT", "ADA-USDT",
		"LINK-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
	}
	type candidate struct {
		instID      string
		depthUSDT   float64 // total ask-side notional top-20
		bestAsk     float64
		totalQty    float64
		lotDecimals int
	}
	var best candidate
	for _, sym := range candidates {
		bk, err := okxDeepBook(sym, 20)
		if err != nil || len(bk.Asks) == 0 {
			continue
		}
		notional := 0.0
		totalQty := 0.0
		for _, l := range bk.Asks {
			notional += l.price * l.size
			totalQty += l.size
		}
		// lot size decimals: price < 1 → 0 decimal for qty, price ≥ 1 → 2 decimals
		decimals := 2
		if bk.Asks[0].price < 1 {
			decimals = 0
		}
		t.Logf("  %s: depth=$%.0f (%.0f units, best ask=%.6f)", sym, notional, totalQty, bk.Asks[0].price)
		if best.instID == "" || notional < best.depthUSDT {
			best = candidate{sym, notional, bk.Asks[0].price, totalQty, decimals}
		}
	}
	if best.instID == "" {
		t.Fatal("could not fetch any candidate book")
	}
	t.Logf("selected: %s (depth=$%.0f, budget=$%.0f → budget/depth=%.1f%%)",
		best.instID, best.depthUSDT, availUSDT, availUSDT/best.depthUSDT*100)

	// ── Step 3: build sizes as % of total ask depth ────────────────────────────
	round := func(v float64, dec int) float64 {
		f := math.Pow(10, float64(dec))
		return math.Round(v*f) / f
	}
	minQty := 1.0
	if best.lotDecimals > 0 {
		minQty = 0.01
	}

	budgetUSDT := availUSDT * 0.85
	pcts := []float64{0.01, 0.05, 0.15, 0.30, 0.60}
	var sizes []float64
	usedUSDT := 0.0
	for _, p := range pcts {
		qty := round(best.totalQty*p, best.lotDecimals)
		if qty < minQty {
			qty = minQty
		}
		notional := qty * best.bestAsk
		if usedUSDT+notional > budgetUSDT {
			qty = round((budgetUSDT-usedUSDT)/best.bestAsk, best.lotDecimals)
		}
		if qty < minQty {
			break
		}
		sizes = append(sizes, qty)
		usedUSDT += qty * best.bestAsk
	}
	t.Logf("test sizes: %v %s (total notional: ~$%.2f)", sizes, best.instID, usedUSDT)

	// ── Step 4: place orders and measure slippage ──────────────────────────────
	type row struct {
		qty         float64
		pctDepth    float64
		totalDepth  float64
		expVWAPReal float64
		expVWAPDemo float64
		fillAvg     float64
		filledQty   float64
		diffRealBps float64
		diffDemoBps float64
		status      string
		realDepthOK bool
		demoDepthOK bool
	}
	var rows []row

	logBook := func(label string, asks []bookLevel) {
		t.Logf("  %s ask book (top-10):", label)
		cumQty := 0.0
		for j, l := range asks {
			if j >= 10 {
				break
			}
			cumQty += l.size
			t.Logf("    [%2d] price=%-12.6f  size=%-12.4f  cumQty=%-12.4f  notional=$%.2f",
				j+1, l.price, l.size, cumQty, l.price*l.size)
		}
	}

	for i, qty := range sizes {
		// Fetch real and demo books in parallel (sequentially here — demo needs auth context)
		realBook, err := okxDeepBook(best.instID, 20)
		if err != nil {
			t.Fatalf("okxDeepBook (real): %v", err)
		}
		demoBook, demoErr := okxDemoDeepBook(c, best.instID, 20)

		expVWAPReal, _, realDepthOK := expectedVWAP(realBook.Asks, qty)
		totalDepth := 0.0
		for _, l := range realBook.Asks {
			totalDepth += l.size
		}
		pctDepth := qty / totalDepth * 100

		expVWAPDemo := 0.0
		demoDepthOK := false
		if demoErr == nil && demoBook != nil {
			expVWAPDemo, _, demoDepthOK = expectedVWAP(demoBook.Asks, qty)
		}

		t.Logf("── order %d/%d: qty=%.4f %s (%.1f%% of real ask depth=%.2f) ──",
			i+1, len(sizes), qty, best.instID, pctDepth, totalDepth)
		logBook("REAL", realBook.Asks)
		t.Logf("  exp_vwap_real=%.6f  (depth_ok=%v)", expVWAPReal, realDepthOK)
		if demoErr == nil && demoBook != nil {
			logBook("DEMO", demoBook.Asks)
			t.Logf("  exp_vwap_demo=%.6f  (depth_ok=%v)", expVWAPDemo, demoDepthOK)
			if expVWAPReal > 0 {
				bookDiffBps := (expVWAPDemo - expVWAPReal) / expVWAPReal * 10000
				t.Logf("  book divergence (demo-real): %+.4f bps", bookDiffBps)
			}
		} else {
			t.Logf("  demo book unavailable: %v", demoErr)
		}

		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resp, err := c.PlaceOrder(cx, creds, exchange.OrderRequest{
			Symbol: best.instID,
			Side:   exchange.Buy,
			Type:   exchange.Market,
			Qty:    decimal.NewFromFloat(qty),
		})
		cancel()
		if err != nil {
			t.Logf("  PlaceOrder error: %v", err)
			rows = append(rows, row{qty: qty, pctDepth: pctDepth, totalDepth: totalDepth, status: "error"})
			time.Sleep(500 * time.Millisecond)
			continue
		}
		t.Logf("  placed:  id=%s  status=%s  qty=%s  filledQty=%s  filledAvg=%s",
			resp.ID, resp.Status, resp.Qty, resp.FilledQty, resp.FilledAvg)

		// Poll until filled
		fillAvg := resp.FilledAvg
		filledQty := resp.FilledQty
		status := resp.Status
		if fillAvg.IsZero() {
			for attempt := range 6 {
				time.Sleep(400 * time.Millisecond)
				cx2, cancel2 := okxCtx()
				got, pollErr := c.GetOrder(cx2, creds, resp.ID)
				cancel2()
				if pollErr != nil {
					t.Logf("  poll[%d] error: %v", attempt+1, pollErr)
					continue
				}
				if got != nil {
					status = got.Status
					filledQty = got.FilledQty
					t.Logf("  poll[%d]: status=%s  filledQty=%s  filledAvg=%s",
						attempt+1, got.Status, got.FilledQty, got.FilledAvg)
					if got.FilledAvg.IsPositive() {
						fillAvg = got.FilledAvg
						break
					}
				}
			}
		}

		fillAvgF := fillAvg.InexactFloat64()
		diffRealBps, diffDemoBps := 0.0, 0.0
		if fillAvg.IsPositive() {
			if expVWAPReal > 0 {
				diffRealBps = (fillAvgF - expVWAPReal) / expVWAPReal * 10000
			}
			if expVWAPDemo > 0 {
				diffDemoBps = (fillAvgF - expVWAPDemo) / expVWAPDemo * 10000
			}
		}
		t.Logf("  result:  fillAvg=%.6f  exp_real=%.6f (%+.4fbps)  exp_demo=%.6f (%+.4fbps)  filledQty=%s",
			fillAvgF, expVWAPReal, diffRealBps, expVWAPDemo, diffDemoBps, filledQty)

		rows = append(rows, row{
			qty: qty, pctDepth: pctDepth, totalDepth: totalDepth,
			expVWAPReal: expVWAPReal, expVWAPDemo: expVWAPDemo,
			fillAvg: fillAvgF, filledQty: filledQty.InexactFloat64(),
			diffRealBps: diffRealBps, diffDemoBps: diffDemoBps,
			status: status, realDepthOK: realDepthOK, demoDepthOK: demoDepthOK,
		})
		time.Sleep(600 * time.Millisecond)
	}

	// Summary table
	t.Log("")
	t.Log("══ SUMMARY ══════════════════════════════════════════════════════════════════════════════════════════════════════════")
	t.Logf("  %-10s %-7s %-12s %-14s %-14s %-14s %-11s %-11s %-12s",
		"qty", "%depth", "depth_total", "exp_vwap_real", "exp_vwap_demo", "fill_avg", "diff_real", "diff_demo", "status")
	t.Log("  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────")
	for _, r := range rows {
		flags := ""
		if !r.realDepthOK {
			flags += "[real-partial]"
		}
		if !r.demoDepthOK {
			flags += "[demo-partial]"
		}
		t.Logf("  %-10.4f %-7.1f %-12.2f %-14.6f %-14.6f %-14.6f %+10.4f  %+10.4f  %s %s",
			r.qty, r.pctDepth, r.totalDepth,
			r.expVWAPReal, r.expVWAPDemo, r.fillAvg,
			r.diffRealBps, r.diffDemoBps, r.status, flags)
	}
	t.Log("════════════════════════════════════════════════════════════════════════════════════════════════════════════════════")
}
