// E2E test: orchestrator data sync pipeline.
//
// Tests the full chain:
//
//	Exchange REST (SyncAccount)
//	  → Portfolio.ApplySync   (in-memory state)
//	  → NATS portfolio.synced.{accountID}  (JetStream; cash/equity/positions + new transactions)
//
// The account's fill history rides inside the PortfolioSyncEvent.Transactions field —
// there is no separate "investment.transactions.{accountID}" subject in the current
// design (that was a leftover from the old investment/ service, folded into helm/ —
// see CLAUDE.md's service-map history note). Don't resurrect that subject name.
//
// Requires a live NATS server and Binance demo credentials:
//
//	NATS_URL=nats://localhost:4222
//	BINANCE_API_KEY=xxx BINANCE_API_SECRET=yyy
//	go test -v -run TestSync_E2E ./internal/runtime/integration/ -timeout 30s
package integration_test

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

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/infra/exchange"
	binanceaction "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/infra/natsapi"
)

// connectNATSWithDiagnostics connects to url and wires verbose lifecycle logging plus
// a channel that receives the raw error text of any async NATS error — most importantly
// permission violations, which otherwise surface only as a silent, never-delivered
// subscription and a confusing "timeout waiting for event" a few seconds later.
// Returns (nil, nil) if the connection cannot be established (caller should skip).
func connectNATSWithDiagnostics(t *testing.T, label, url string) (*nats.Conn, <-chan string) {
	t.Helper()
	asyncErrs := make(chan string, 8)
	nc, err := nats.Connect(url,
		nats.Name("helm-integration-test-"+label),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			t.Logf("NATS[%s]: disconnected: %v", label, err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			t.Logf("NATS[%s]: reconnected to %s", label, c.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			t.Logf("NATS[%s]: connection closed", label)
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := "?"
			if sub != nil {
				subject = sub.Subject
			}
			t.Logf("NATS[%s]: async error on subject %q: %v", label, subject, err)
			select {
			case asyncErrs <- err.Error():
			default:
			}
		}),
	)
	if err != nil {
		t.Logf("NATS[%s]: connect to %s failed: %v", label, url, err)
		return nil, asyncErrs
	}
	t.Logf("NATS[%s]: connected to %s (server: %s)", label, url, nc.ConnectedServerId())
	return nc, asyncErrs
}

func TestSync_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	// ── Prerequisites ──────────────────────────────────────────────────────────
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://helm:helm-dev@127.0.0.1:4222"
	}

	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	apiKey := binanceDemoAPIKey
	apiSecret := binanceDemoAPISecret

	// ── NATS ───────────────────────────────────────────────────────────────────
	// helm user: publishes portfolio.synced.>
	nc, helmAsyncErrs := connectNATSWithDiagnostics(t, "helm", natsURL)
	if nc == nil {
		t.Skipf("NATS unavailable at %s", natsURL)
	}
	defer nc.Close()

	// subscriber user: subscribes to portfolio.synced.> to mirror what a downstream
	// consumer (gateway/UI) does. Uses a distinct env var so a permissions-scoped NATS
	// user can be tested against a differently-scoped publisher, matching production
	// topology (helm publishes, gateway subscribes).
	subURL := os.Getenv("NATS_SUBSCRIBER_URL")
	if subURL == "" {
		subURL = natsURL
	}
	ncSub, subAsyncErrs := connectNATSWithDiagnostics(t, "subscriber", subURL)
	if ncSub != nil {
		defer ncSub.Close()
	}

	// ── Exchange client ────────────────────────────────────────────────────────
	ex := binanceaction.New(true) // testnet=true → demo-api.binance.com
	creds := exchange.Credentials{APIKey: apiKey, APISecret: apiSecret}

	// ── Orchestrator ───────────────────────────────────────────────────────────
	orchID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()
	t.Logf("test identities: helm_id=%s account_id=%s user_id=%s", orchID, accountID, userID)

	pf := portfolio.New(decimal.Zero) // capital updated by SyncAccount
	rm := risk.New(risk.DefaultConfig(), pf)

	rt := actor.NewHelmRuntime(
		orchID, accountID, userID,
		"binance",
		pf, rm, ex, creds,
		nil, // no prior sync time → full sync
		time.Now(),
	)

	// ── Subscribe NATS before Sync (subscriber user) ──────────────────────────
	portfolioSyncCh := make(chan *natsapi.PortfolioSyncEvent, 4)
	subj := fmt.Sprintf(natsapi.SubjPortfolioSynced, accountID.String())
	t.Logf("subscribing to %q", subj)
	if ncSub != nil {
		sub, err := ncSub.Subscribe(subj, func(msg *nats.Msg) {
			t.Logf("NATS[subscriber]: message received on %q (%d bytes)", msg.Subject, len(msg.Data))
			var ev natsapi.PortfolioSyncEvent
			if err := json.Unmarshal(msg.Data, &ev); err != nil {
				t.Logf("portfolio.synced parse error: %v", err)
				return
			}
			portfolioSyncCh <- &ev
		})
		if err != nil {
			t.Logf("subscribe %s failed: %v — skipping NATS assertion", subj, err)
			ncSub = nil
		} else {
			defer sub.Unsubscribe()
			// Flush so a SUB permissions violation (delivered asynchronously by the
			// server) has a chance to surface before we start waiting on the channel.
			if err := ncSub.Flush(); err != nil {
				t.Logf("NATS[subscriber]: flush after subscribe failed: %v", err)
			}
		}
	}

	// waitForSyncEvent waits for either a delivered PortfolioSyncEvent or an async NATS
	// error (most commonly a permissions violation) and fails with a clear, specific
	// message instead of a bare "timeout" that gives no hint about which of the two
	// happened.
	waitForSyncEvent := func(timeout time.Duration) *natsapi.PortfolioSyncEvent {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case ev := <-portfolioSyncCh:
				return ev
			case errText := <-subAsyncErrs:
				t.Errorf("NATS permissions/async error while waiting for %q: %s — check the subscriber NATS user's subscribe permissions for portfolio.synced.>", subj, errText)
				return nil
			case errText := <-helmAsyncErrs:
				t.Errorf("NATS permissions/async error on the publisher connection: %s — check the helm NATS user's publish permissions for portfolio.synced.>", errText)
				return nil
			case <-deadline:
				t.Errorf("no portfolio.synced event (and no async NATS error) received within %s", timeout)
				return nil
			}
		}
	}

	// ── Run Sync ───────────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Log("running first Sync()...")
	syncStart := time.Now()
	if err := rt.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	t.Logf("first Sync() took %s", time.Since(syncStart))

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
	firstSyncAt := rt.LastSyncAt()
	if firstSyncAt.IsZero() {
		t.Error("LastSyncAt should be set after sync")
	} else {
		t.Logf("LastSyncAt = %s", firstSyncAt)
	}

	// ── Verify NATS portfolio.synced published ─────────────────────────────────
	if ev := waitForSyncEvent(5 * time.Second); ev != nil {
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
		if !ev.Equity.Equal(equity) {
			t.Errorf("NATS equity %s ≠ portfolio equity %s", ev.Equity, equity)
		}
		if len(ev.Positions) != len(positions) {
			t.Errorf("NATS positions count %d ≠ portfolio positions count %d", len(ev.Positions), len(positions))
		}
	}

	// ── Second Sync: only new transactions should be forwarded, LastSyncAt must advance ──
	t.Log("running second Sync() — should skip already-seen transactions...")
	syncStart = time.Now()
	if err := rt.Sync(ctx); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	t.Logf("second Sync() took %s", time.Since(syncStart))

	secondSyncAt := rt.LastSyncAt()
	t.Logf("LastSyncAt after second sync = %s", secondSyncAt)
	if !secondSyncAt.After(firstSyncAt) {
		t.Errorf("expected LastSyncAt to advance between syncs: first=%s second=%s", firstSyncAt, secondSyncAt)
	}

	if ev := waitForSyncEvent(3 * time.Second); ev != nil {
		t.Logf("second portfolio.synced: transactions=%d (want 0 new)", len(ev.Transactions))
		if len(ev.Transactions) != 0 {
			t.Errorf("expected 0 new transactions on second sync (already-seen fills should be deduped), got %d", len(ev.Transactions))
		}
	}

	// ── Third Sync: idempotency — cash/equity should be stable with no new fills ──
	t.Log("running third Sync() — verifying idempotency (no fills expected in this window)...")
	if err := rt.Sync(ctx); err != nil {
		t.Fatalf("third Sync: %v", err)
	}
	thirdCash := pf.Cash()
	if !thirdCash.Equal(cash) {
		t.Logf("cash changed between first and third sync (%s → %s) — expected only if a fill landed during the test window", cash, thirdCash)
	} else {
		t.Logf("cash stable across 3 syncs: %s", thirdCash)
	}
	if ev := waitForSyncEvent(3 * time.Second); ev != nil {
		t.Logf("third portfolio.synced: transactions=%d (want 0 new)", len(ev.Transactions))
	}

	t.Log("PASS: data sync pipeline verified end-to-end")
}
