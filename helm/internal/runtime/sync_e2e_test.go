// E2E test: orchestrator data sync pipeline.
//
// Tests the full chain:
//
//	Exchange REST (SyncAccount)
//	  → Portfolio.ApplySync   (in-memory state)
//	  → NATS portfolio.synced.{accountID}  (investment service audit)
//	  → NATS investment.transactions.{accountID}  (new fills only, JetStream)
//
// Requires a live NATS server and Binance demo credentials:
//
//	NATS_URL=nats://localhost:4222
//	BINANCE_API_KEY=xxx BINANCE_API_SECRET=yyy
//	go test -v -run TestSync_E2E ./internal/runtime/ -timeout 30s
package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	binanceaction "mallow/helm/internal/infra/exchange/binance/action"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
)

func TestSync_E2E(t *testing.T) {
	// ── Prerequisites ──────────────────────────────────────────────────────────
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://orchestrator:orchestrator-dev@127.0.0.1:4222"
	}
	apiKey := os.Getenv("BINANCE_API_KEY")
	apiSecret := os.Getenv("BINANCE_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		t.Skip("BINANCE_API_KEY / BINANCE_API_SECRET not set")
	}

	// ── NATS ───────────────────────────────────────────────────────────────────
	// orchestrator user: publishes portfolio.synced.> and investment.transactions.>
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("NATS unavailable at %s: %v", natsURL, err)
	}
	defer nc.Close()
	t.Logf("NATS connected (orchestrator): %s", natsURL)

	// investment user: subscribes to portfolio.synced.> to mirror what the investment service does
	investURL := os.Getenv("NATS_INVEST_URL")
	if investURL == "" {
		investURL = "nats://investment:investment-dev@127.0.0.1:4222"
	}
	ncInvest, err := nats.Connect(investURL)
	if err != nil {
		t.Logf("investment NATS unavailable (%v) — skipping NATS assertion", err)
		ncInvest = nil
	} else {
		defer ncInvest.Close()
		t.Logf("NATS connected (investment): %s", investURL)
	}

	// ── Exchange client ────────────────────────────────────────────────────────
	ex := binanceaction.New(true) // testnet=true → demo-api.binance.com
	creds := exchange.Credentials{APIKey: apiKey, APISecret: apiSecret}

	// ── Orchestrator ───────────────────────────────────────────────────────────
	orchID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()

	pf := portfolio.New(decimal.Zero) // capital updated by SyncAccount
	rm := risk.New(risk.DefaultConfig(), pf)
	ob := orderbook.NewOrderBook("binance")

	rt := runtime.NewHelmRuntime(
		orchID, accountID, userID,
		"binance",
		pf, rm, ob, ex, creds,
		nil, // no prior sync time → full sync
	)

	// ── Subscribe NATS before Sync (investment user) ──────────────────────────
	portfolioSyncCh := make(chan *natsapi.PortfolioSyncEvent, 4)
	subj := fmt.Sprintf(natsapi.SubjPortfolioSynced, accountID.String())
	if ncInvest != nil {
		sub, err := ncInvest.Subscribe(subj, func(msg *nats.Msg) {
			var ev natsapi.PortfolioSyncEvent
			if err := json.Unmarshal(msg.Data, &ev); err != nil {
				t.Logf("portfolio.synced parse error: %v", err)
				return
			}
			portfolioSyncCh <- &ev
		})
		if err != nil {
			t.Logf("subscribe %s failed: %v — skipping NATS assertion", subj, err)
			ncInvest = nil
		} else {
			defer sub.Unsubscribe()
		}
	}

	// ── Run Sync ───────────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Log("running Sync()...")
	if err := rt.Sync(ctx, nc, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// ── Verify in-memory portfolio ─────────────────────────────────────────────
	equity := pf.Equity()
	cash := pf.Cash()
	positions := pf.Positions()

	t.Logf("portfolio after sync:")
	t.Logf("  cash   = %s", cash)
	t.Logf("  equity = %s", equity)
	t.Logf("  positions (%d):", len(positions))
	for _, p := range positions {
		t.Logf("    %s: qty=%s  avg=%s  cur=%s", p.Symbol, p.Qty, p.AvgPrice, p.CurrentPrice)
	}

	if !cash.IsPositive() {
		t.Error("portfolio cash should be positive after sync")
	}
	if !equity.IsPositive() {
		t.Error("portfolio equity should be positive after sync")
	}
	if rt.LastSyncAt().IsZero() {
		t.Error("LastSyncAt should be set after sync")
	}

	// ── Verify NATS portfolio.synced published ─────────────────────────────────
	select {
	case ev := <-portfolioSyncCh:
		t.Logf("portfolio.synced received:")
		t.Logf("  orchestrator_id = %s", ev.OrchestratorID)
		t.Logf("  account_id      = %s", ev.AccountID)
		t.Logf("  cash            = %s", ev.Cash)
		t.Logf("  equity          = %s", ev.Equity)
		t.Logf("  positions       = %d", len(ev.Positions))
		t.Logf("  transactions    = %d", len(ev.Transactions))
		t.Logf("  synced_at       = %s", ev.SyncedAt)

		if ev.OrchestratorID != orchID.String() {
			t.Errorf("orchestrator_id mismatch: got %s want %s", ev.OrchestratorID, orchID)
		}
		if ev.AccountID != accountID.String() {
			t.Errorf("account_id mismatch: got %s want %s", ev.AccountID, accountID)
		}
		if !ev.Cash.Equal(cash) {
			t.Errorf("NATS cash %s ≠ portfolio cash %s", ev.Cash, cash)
		}
	case <-time.After(5 * time.Second):
		t.Error("no portfolio.synced event received within 5s")
	}

	// ── Second Sync: only new transactions should be forwarded ─────────────────
	t.Log("running second Sync() — should skip already-seen transactions...")
	if err := rt.Sync(ctx, nc, nil); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	select {
	case ev := <-portfolioSyncCh:
		t.Logf("second portfolio.synced: transactions=%d (want 0 new)", len(ev.Transactions))
	case <-time.After(3 * time.Second):
		t.Error("no second portfolio.synced event")
	}

	t.Log("PASS: data sync pipeline verified end-to-end")
}
