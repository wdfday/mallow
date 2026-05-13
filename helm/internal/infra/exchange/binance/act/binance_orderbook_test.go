// Orderbook comparison: Binance demo-stream vs production stream.
// Subscribes to BTCUSDT BBO + top-5 depth on both environments simultaneously
// and prints side-by-side snapshots to show whether demo mirrors real market data.
//
// Run:
//
//	go test -v -run TestOrderbook ./internal/infra/exchange/binance/action/ -timeout 30s
package act

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
)

// ── BBO comparison ────────────────────────────────────────────────────────────

type bboSnapshot struct {
	env      string
	bidPrice float64
	bidQty   float64
	askPrice float64
	askQty   float64
	at       time.Time
}

func TestOrderbook_BBO(t *testing.T) {
	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var mu sync.Mutex
	latest := map[string]*bboSnapshot{}

	subscribe := func(env string, demo bool) {
		wsMu.Lock()
		gobinance.UseDemo = demo
		doneC, stopC, err := gobinance.WsBookTickerServe("BTCUSDT", func(e *gobinance.WsBookTickerEvent) {
			bid, _ := strconv.ParseFloat(e.BestBidPrice, 64)
			ask, _ := strconv.ParseFloat(e.BestAskPrice, 64)
			bq, _ := strconv.ParseFloat(e.BestBidQty, 64)
			aq, _ := strconv.ParseFloat(e.BestAskQty, 64)
			mu.Lock()
			latest[env] = &bboSnapshot{env: env, bidPrice: bid, bidQty: bq, askPrice: ask, askQty: aq, at: time.Now()}
			mu.Unlock()
		}, func(err error) {
			t.Logf("%s bbo err: %v", env, err)
		})
		gobinance.UseDemo = false
		wsMu.Unlock()
		if err != nil {
			t.Logf("subscribe %s BBO: %v", env, err)
			return
		}
		select {
		case <-cx.Done():
			close(stopC)
		case <-doneC:
		}
	}

	// Subscribe production first, then demo (serialise to avoid UseDemo race)
	go subscribe("prod", false)
	time.Sleep(100 * time.Millisecond)
	go subscribe("demo", true)

	// Print comparison every 2s
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	rounds := 0
	for {
		select {
		case <-cx.Done():
			return
		case <-ticker.C:
			rounds++
			mu.Lock()
			prod := latest["prod"]
			demo := latest["demo"]
			mu.Unlock()

			t.Log("─────────────────────────────────────────────────────────────────")
			if prod != nil {
				t.Logf("  prod  bid=%-10.2f qty=%-8.4f  ask=%-10.2f qty=%-8.4f  spread=%.2f bps",
					prod.bidPrice, prod.bidQty, prod.askPrice, prod.askQty,
					(prod.askPrice-prod.bidPrice)/prod.bidPrice*10000)
			} else {
				t.Log("  prod  (no data yet)")
			}
			if demo != nil {
				t.Logf("  demo  bid=%-10.2f qty=%-8.4f  ask=%-10.2f qty=%-8.4f  spread=%.2f bps",
					demo.bidPrice, demo.bidQty, demo.askPrice, demo.askQty,
					(demo.askPrice-demo.bidPrice)/demo.bidPrice*10000)
			} else {
				t.Log("  demo  (no data yet)")
			}
			if prod != nil && demo != nil {
				bidDiff := math.Abs(prod.bidPrice-demo.bidPrice) / prod.bidPrice * 10000
				askDiff := math.Abs(prod.askPrice-demo.askPrice) / prod.askPrice * 10000
				t.Logf("  Δbid=%.2f bps  Δask=%.2f bps", bidDiff, askDiff)
			}

			if rounds >= 5 {
				return
			}
		}
	}
}

// ── Depth (top 5 levels) comparison ──────────────────────────────────────────

type depthSnapshot struct {
	env  string
	bids []gobinance.Bid
	asks []gobinance.Ask
	at   time.Time
}

func TestOrderbook_Depth5(t *testing.T) {
	cx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var mu sync.Mutex
	latest := map[string]*depthSnapshot{}

	subscribe := func(env string, demo bool) {
		wsMu.Lock()
		gobinance.UseDemo = demo
		doneC, stopC, err := gobinance.WsPartialDepthServe100Ms("BTCUSDT", "5",
			func(e *gobinance.WsPartialDepthEvent) {
				mu.Lock()
				latest[env] = &depthSnapshot{env: env, bids: e.Bids, asks: e.Asks, at: time.Now()}
				mu.Unlock()
			}, func(err error) {
				t.Logf("%s depth err: %v", env, err)
			})
		gobinance.UseDemo = false
		wsMu.Unlock()
		if err != nil {
			t.Logf("subscribe %s depth5: %v", env, err)
			return
		}
		select {
		case <-cx.Done():
			close(stopC)
		case <-doneC:
		}
	}

	go subscribe("prod", false)
	time.Sleep(100 * time.Millisecond)
	go subscribe("demo", true)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	rounds := 0
	for {
		select {
		case <-cx.Done():
			return
		case <-ticker.C:
			rounds++
			mu.Lock()
			prod := latest["prod"]
			demo := latest["demo"]
			mu.Unlock()

			t.Log("════════════════════════════════════════════════════════════════════")
			printDepth(t, "prod", prod)
			t.Log("────────────────────────────────────────────────────────────────────")
			printDepth(t, "demo", demo)

			if rounds >= 3 {
				return
			}
		}
	}
}

func printDepth(t *testing.T, env string, snap *depthSnapshot) {
	t.Helper()
	if snap == nil {
		t.Logf("  %s: (no data yet)", env)
		return
	}
	t.Logf("  %s  (age=%dms)", env, time.Since(snap.at).Milliseconds())
	n := len(snap.bids)
	if len(snap.asks) < n {
		n = len(snap.asks)
	}
	t.Logf("  %-6s  %-12s %-12s  |  %-12s %-12s", env, "bid price", "bid qty", "ask price", "ask qty")
	for i := 0; i < n; i++ {
		t.Logf("  %6d  %-12s %-12s  |  %-12s %-12s",
			i+1,
			fmt.Sprintf("%.2f", parseFloat(snap.bids[i].Price)),
			snap.bids[i].Quantity,
			fmt.Sprintf("%.2f", parseFloat(snap.asks[i].Price)),
			snap.asks[i].Quantity,
		)
	}
}
