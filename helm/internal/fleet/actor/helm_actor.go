package actor

// helm_actor.go — HelmRuntime's single-owner trade actor.
//
// ProcessTrade, ReportFill, EnsureLeverage, and ReportFeeEvent all touch account-level
// shared state (Portfolio, leverage-per-symbol bookkeeping) that every hand under this
// helm calls into concurrently. Historically this was guarded by a raw tradeMu mutex;
// runTradeActor replaces that with the same "single owning goroutine, everyone else only
// sends" idiom already used for runLifecycleProcessor/runFillProcessor (helm_streams.go) —
// no lock needed inside the actor loop itself, since only it ever touches this state.
//
// Started unconditionally in NewHelmRuntime (not StartStreaming): StartStreaming no-ops
// for exchanges without AccountStreamer (or when the WS connect fails), and many tests
// construct a HelmRuntime and call ProcessTrade/ReportFill directly without ever calling
// StartStreaming — the trade actor must not depend on WS streaming being available.

import (
	"context"
	"fmt"
	"log/slog"
	"mallow/helm/internal/fleet/actor/eventcode"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/tactics"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/safe"
)

// tradeRequest carries a TradeProposal to the actor loop and receives the reply
// on a per-request channel — request/reply, unlike the fire-and-forget queues
// used for fills/lifecycle events, because the caller needs the sizing decision.
type tradeRequest struct {
	proposal signalfollower.TradeProposal
	tact     tactics.Planner
	reply    chan helmdomain.TradeReply
}

// leverageRequest asks the actor to ensure leverage/margin-mode is configured for
// symbol, no-op if already done. done is closed once processed (no data to return).
type leverageRequest struct {
	symbol  string
	futures *handdomain.FuturesConfig
	done    chan struct{}
}

// FeeEvent is a generic account-level fee/credit reported by the exchange (funding
// fee today; a future equities daily-accrual margin/borrow-interest kind would add
// its own Kind value and feeSplit case, not change this struct's shape). Attribution
// only — the actor computes each hand's proportional share and notifies it via a
// HelmEvent; it does not mutate Portfolio/cash (see helm_actor.go's feeSplit doc).
type FeeEvent struct {
	Kind   string // "funding"
	Symbol string
	Amount decimal.Decimal // negative = charge, positive = credit
}

// runTradeActor is the sole goroutine that ever reads tradeReqCh, fillReportCh,
// feeEventCh, and leverageReqCh, and the sole writer of leverageSet. Every other
// goroutine (each hand's own run loop) only ever sends on these channels via the
// public ProcessTrade / ReportFill / EnsureLeverage / ReportFeeEvent methods below.
func (r *HelmRuntime) runTradeActor(ctx context.Context) {
	defer safe.Recover()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case req := <-r.tradeReqCh:
			req.reply <- r.processTrade(req.proposal, req.tact)
		case fill := <-r.fillReportCh:
			r.reportFill(fill)
		case req := <-r.leverageReqCh:
			r.ensureLeverage(ctx, req.symbol, req.futures)
			close(req.done)
		case ev := <-r.feeEventCh:
			r.applyFeeEvent(ev)
		}
	}
}

// ProcessTrade validates a trade against account-level guards and sizes via the
// hand's tactician. Thin wrapper: the real logic lives in processTrade, run only
// on the trade actor goroutine. Blocks until the actor replies or ctx is done.
func (r *HelmRuntime) ProcessTrade(ctx context.Context, proposal signalfollower.TradeProposal, tact tactics.Planner) helmdomain.TradeReply {
	req := &tradeRequest{proposal: proposal, tact: tact, reply: make(chan helmdomain.TradeReply, 1)}
	select {
	case r.tradeReqCh <- req:
	case <-ctx.Done():
		return helmdomain.TradeReply{Approved: false, Reason: "context cancelled"}
	case <-r.stopCh:
		return helmdomain.TradeReply{Approved: false, Reason: "helm stopped"}
	}
	select {
	case reply := <-req.reply:
		return reply
	case <-ctx.Done():
		return helmdomain.TradeReply{Approved: false, Reason: "context cancelled"}
	case <-r.stopCh:
		return helmdomain.TradeReply{Approved: false, Reason: "helm stopped"}
	}
}

// ReportFill is the single choke-point for updating portfolio state after any fill —
// see reportFill's doc for the three call paths that converge here. Fire-and-forget:
// no reply needed, mirrors the lifecycleQueue/wsFillQueue send pattern. Selecting on
// stopCh (not a ctx — ReportFill's existing signature takes none) keeps this from
// blocking forever if the actor loop is already gone.
func (r *HelmRuntime) ReportFill(fill helmdomain.FillReport) {
	select {
	case r.fillReportCh <- fill:
	case <-r.stopCh:
	}
}

// EnsureLeverage asks the trade actor to configure leverage/margin-mode for symbol,
// no-op if already done for this helm (leverageSet is actor-owned — see ensureLeverage).
// Replaces the old per-Hand leverageApplied tracking, which let two hands on the same
// helm race on the exchange's single account+symbol leverage setting.
func (r *HelmRuntime) EnsureLeverage(ctx context.Context, symbol string, futures *handdomain.FuturesConfig) {
	req := &leverageRequest{symbol: symbol, futures: futures, done: make(chan struct{})}
	select {
	case r.leverageReqCh <- req:
	case <-ctx.Done():
		return
	case <-r.stopCh:
		return
	}
	select {
	case <-req.done:
	case <-ctx.Done():
	case <-r.stopCh:
	}
}

// ReportFeeEvent notifies the trade actor of an account-level fee/credit so it can
// attribute a proportional share to each hand holding a position in ev.Symbol. Entry
// point for a future exchange-adapter WS handler that detects real funding events —
// no adapter wires this yet (see helm_actor.go's package doc). Fire-and-forget.
func (r *HelmRuntime) ReportFeeEvent(ev FeeEvent) {
	select {
	case r.feeEventCh <- ev:
	case <-r.stopCh:
	}
}

// ensureLeverage is the trade-actor-only body behind EnsureLeverage. Runs only on
// runTradeActor's goroutine — leverageSet needs no lock. Non-blocking on failure
// (just logs), matching the previous per-Hand applyFuturesLeverage's behavior.
func (r *HelmRuntime) ensureLeverage(ctx context.Context, symbol string, futures *handdomain.FuturesConfig) {
	if futures == nil || futures.Leverage <= 0 {
		return
	}
	if r.leverageSet[symbol] {
		return
	}
	setter, ok := r.Exchange.(exchange.LeverageSetter)
	if !ok {
		return
	}
	marginType := string(futures.MarginType)
	if marginType == "" {
		marginType = "isolated"
	}
	if err := setter.SetLeverage(ctx, r.Creds, symbol, futures.Leverage, marginType); err != nil {
		slog.Warn("helm: set leverage failed (non-fatal)", "helm_id", r.HelmID, "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType, "err", err)
		return
	}
	r.leverageSet[symbol] = true
	slog.Info("helm: leverage set", "helm_id", r.HelmID, "symbol", symbol,
		"leverage", futures.Leverage, "margin_type", marginType)
	r.EmitEvent(natsapi.HelmEvent{
		Code:   eventcode.CodeHandLeverageSet,
		Symbol: symbol,
		Reason: fmt.Sprintf("leverage=%d margin_type=%s", futures.Leverage, marginType),
		Msg:    "helm: leverage & margin configured",
	})
}

// feeSplit returns each active hand's proportional share of ev.Amount, keyed by
// hand ID. "funding": snapshot each hand's current position qty in ev.Symbol,
// split by qty share of the account's total position in that symbol at this
// instant — matches how perp funding is actually billed by the exchange (a
// snapshot at the settlement instant, not time-weighted over the interval). A
// future daily-accrual kind (equities margin/borrow interest) would add its own
// case here with its own snapshot rule, not change how callers use FeeEvent.
func (r *HelmRuntime) feeSplit(ev FeeEvent) map[string]decimal.Decimal {
	shares := make(map[string]decimal.Decimal)
	if ev.Amount.IsZero() {
		return shares
	}
	r.mu.RLock()
	entries := make([]*handEntry, 0, len(r.hands))
	for _, e := range r.hands {
		entries = append(entries, e)
	}
	r.mu.RUnlock()

	qtyByHand := make(map[string]decimal.Decimal, len(entries))
	total := decimal.Zero
	for _, e := range entries {
		var handQty decimal.Decimal
		for _, leg := range e.h.ActiveLegs() {
			if leg.Symbol == ev.Symbol {
				handQty = handQty.Add(leg.Qty.Abs())
			}
		}
		if handQty.IsPositive() {
			qtyByHand[e.h.ID().String()] = handQty
			total = total.Add(handQty)
		}
	}
	if total.IsZero() {
		return shares
	}
	for handID, qty := range qtyByHand {
		shares[handID] = ev.Amount.Mul(qty).Div(total)
	}
	return shares
}

// applyFeeEvent computes the per-hand split and notifies each affected hand.
// Read-only: does not mutate Portfolio or any hand's bookkeeping — see FeeEvent's
// doc for why that's a deliberately separate, later decision.
func (r *HelmRuntime) applyFeeEvent(ev FeeEvent) {
	shares := r.feeSplit(ev)
	if len(shares) == 0 {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.hands {
		share, ok := shares[e.h.ID().String()]
		if !ok {
			continue
		}
		e.h.EmitEvent(natsapi.HelmEvent{
			Code:   eventcode.CodeFeeAttributed,
			Symbol: ev.Symbol,
			Reason: fmt.Sprintf("kind=%s amount=%s", ev.Kind, share),
			Msg:    "helm: fee event attributed to hand",
		})
	}
}
