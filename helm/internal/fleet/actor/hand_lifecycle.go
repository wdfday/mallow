package actor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/fleet/actor/position"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/hand/domain"
)

// NewHand creates a Hand. Call Start() to spawn its run-loop goroutine.
func NewHand(
	id, helmID uuid.UUID,
	rt *HelmRuntime,
	strat strategy.Strategy,
	tact tactics.Planner,
	pyramid bool,
	maxUnits int,
	signalTTL time.Duration,
	futuresConfig *domain.FuturesConfig,
	orderType domain.OrderType,
	limitTimeoutSec int,
	limitFallback domain.LimitFallback,
	edgeRisk domain.HandGuardConfig,
	allocatedCap decimal.Decimal,
) *Hand {
	if maxUnits <= 0 {
		maxUnits = 1
	}
	var ring []decimal.Decimal
	if edgeRisk.WindowTrades > 0 {
		ring = make([]decimal.Decimal, edgeRisk.WindowTrades)
	}
	return &Hand{
		id:          id,
		helmID:      helmID,
		log:         slog.With("hand_id", id.String(), "helm_id", helmID.String()),
		helmRuntime: rt,
		strategy:    strat,
		tactician:   tact,
		limiter:     rate.NewLimiter(rate.Every(1*time.Second), 5),
		cfg: handConfig{
			pyramid:         pyramid,
			maxUnits:        maxUnits,
			signalTTL:       signalTTL,
			orderType:       orderType,
			limitTimeoutSec: limitTimeoutSec,
			limitFallback:   limitFallback,
			futuresConfig:   futuresConfig,
			isFutures:       futuresConfig != nil,
		},
		guard: handEdgeGuard{
			cfg:  edgeRisk,
			ring: ring,
		},
		allocatedCap:    allocatedCap,
		leverageApplied: make(map[string]bool),
		Signals:         make(chan Signal, 1),
		UrgentSignals:   make(chan Signal, 4),
		fillSignal:      make(chan struct{}, 1),
		pollCh:          make(chan pollBatch, 1),
		placeResultCh:   make(chan *pendingPlace, 16),
		limitTimeoutCh:  make(chan *pendingLimitTimeout, 4),
		seenFills:       make(map[string]time.Time),
		wsFillCache:     make(map[string]cachedWsFill),

		partialApplied:  make(map[string]partialAppliedState),
		orders:          make([]domain.Order, 0, 256),
		pendingExits:    make(map[string]exitPending),
		exitLevels:      make(map[string]exitLevel),
		pos:             position.NewHandPositions(pyramid, maxUnits),
		pendingOrderPos: make(map[string]string),
		pendingCancels:  make(map[string]struct{}),
		exitCancelCh:    make(chan string, 8),
		health:          HandHealth{Status: "stopped"},
	}
}

// SignalTTLFor returns the per-hand signal TTL from domain config.
// Default is 10s when SignalTTLSec is 0; negative values disable the check.
func SignalTTLFor(b *domain.Hand) time.Duration {
	switch {
	case b.SignalTTLSec < 0:
		return 0
	case b.SignalTTLSec > 0:
		return time.Duration(b.SignalTTLSec) * time.Second
	default:
		return 10 * time.Second
	}
}

// BuildHandComponents translates a Hand into a Strategy + Tactician.
func BuildHandComponents(b *domain.Hand) (strategy.Strategy, *tactics.Tactician) {
	minStrength := b.Strategy.MinStrength
	if minStrength <= 0 {
		minStrength = 0.3
	}

	strat := strategy.NewSignalFollower(minStrength)

	sizingMode := tactics.SizingFixedFractional
	switch b.Position.SizeMode {
	case domain.SizeModeFixedQty:
		sizingMode = tactics.SizingFixedQty
	case domain.SizeModeQuoteQty:
		sizingMode = tactics.SizingQuoteQty
	case domain.SizeModeVolatility:
		sizingMode = tactics.SizingVolatility
	case domain.SizeModePercentEquity:
		sizingMode = tactics.SizingPercentEquity
	}

	strengthSizing := b.Position.StrengthSizing == nil || *b.Position.StrengthSizing
	sc := tactics.SizingConfig{
		Mode:            sizingMode,
		UnitPct:         b.Position.UnitPct,
		RiskPerTradePct: b.Position.RiskPerTradePct,
		MaxPositionPct:  b.Position.MaxPositionPct,
		FixedQty:        b.Position.FixedQty,
		FixedQuoteQty:   b.Position.FixedQuoteQty,
		StrengthSizing:  strengthSizing,
	}
	tact := tactics.New(sc)

	return strat, tact
}

// restorePosition re-applies HandPositions reconstructed from poslog replay.
// Called by the reconciler after startup to bring the in-memory hand in sync
// with what actually happened at the exchange while the app was down.
// currentPrice comes from ListPositions and is used to seed the Portfolio.
func (h *Hand) restorePosition(hp *position.HandPositions, currentPrice decimal.Decimal) {
	pos := hp.ToPosition(h.id.String(), h.helmID.String(), currentPrice)

	// Restore each active leg into the portfolio without generating new orders.
	for _, leg := range hp.ActiveLegs() {
		h.helmRuntime.Portfolio.RestorePosition(leg.Symbol, leg.Side, leg.Qty, leg.EntryPrice, currentPrice, leg.DeployedCapital, leg.OpenedAt)
	}

	h.mu.Lock()
	// Take ownership of the replayed position state.
	h.pos = hp

	// Rebuild pending order → position mapping for legs still awaiting a fill.
	// Also re-register into the HelmRuntime orderHandMap so that fill routing
	// and trade.filled events carry the correct hand_id after restart.
	// (ReconcileOrders tracks open orders with an empty handID because it only
	// has exchange order IDs — this corrects those entries.)
	for _, leg := range hp.ActiveLegs() {
		if leg.HasPendingOrder() {
			h.pendingOrderPos[leg.PendingOrderID] = leg.TradeID
			h.helmRuntime.TrackOrder(leg.PendingOrderID, h.id.String())
		}
	}

	// Rebuild local exit-level guards.
	// PlacedAt is set to now so fetchBracketStates applies the bracketPollGrace window
	// after restart. Without this, restored brackets are polled immediately on the first
	// tick — a transient "not_found" from the exchange triggers a false external-close
	// and writes KindPositionOrphaned to the poslog before the position can be confirmed.
	// Pyramid: SL/TP from pos (latest signal levels); non-pyramid: per-leg.
	restoredAt := time.Now()
	if h.cfg.pyramid {
		if pl := hp.PrimaryLeg(); pl != nil && (pos.StopLoss.IsPositive() || pos.TakeProfit.IsPositive()) {
			h.exitLevels[pl.TradeID] = exitLevel{
				Symbol:           pos.Symbol,
				Side:             pos.Side,
				StopLoss:         pos.StopLoss,
				TakeProfit:       pos.TakeProfit,
				ExchangeOrderIDs: pl.ExchangeOrderIDs,
				GroupID:          pl.GroupID,
				PlacedAt:         restoredAt,
			}
		}
	} else {
		for _, leg := range hp.ActiveLegs() {
			if leg.StopLoss.IsPositive() || leg.TakeProfit.IsPositive() {
				h.exitLevels[leg.TradeID] = exitLevel{
					Symbol:           leg.Symbol,
					Side:             leg.Side,
					StopLoss:         leg.StopLoss,
					TakeProfit:       leg.TakeProfit,
					ExchangeOrderIDs: leg.ExchangeOrderIDs,
					GroupID:          leg.GroupID,
					PlacedAt:         restoredAt,
				}
			}
		}
	}

	// Re-register bracket order IDs in the helm's orderHandMap so fill routing and
	// HandleExitOrderCanceled work correctly after restart.
	for _, leg := range hp.ActiveLegs() {
		for _, id := range leg.ExchangeOrderIDs {
			h.helmRuntime.TrackOrder(id, h.id.String())
		}
	}
	h.mu.Unlock()
}

// RestorePnL rebuilds metrics.totalPnL/totalCommission/winCount/lossCount on startup.
//
// Priority:
//  1. SQL aggregate (PnLSummer) — single query, preferred.
//  2. JetStream drain (TradeLog.Since) — O(N trades), fallback when PnLSummer is nil.
//  3. poslog PositionClosed events — last resort when both above are unavailable.
func (h *Hand) RestorePnL(ctx context.Context, events []poslog.Event) {
	// Fast path: single SQL aggregate query — no JetStream drain needed.
	if ps := h.helmRuntime.PnLSummer; ps != nil {
		totalPnL, totalCommission, wins, losses, err := ps.SumHandPnL(ctx, h.id)
		if err == nil {
			h.metrics.mu.Lock()
			h.metrics.totalPnL = totalPnL
			h.metrics.totalCommission = totalCommission
			h.metrics.winCount = wins
			h.metrics.lossCount = losses
			h.metrics.mu.Unlock()
			h.log.Info("hand: PnL restored from postgres",
				"total_pnl", totalPnL, "total_commission", totalCommission,
				"wins", wins, "losses", losses,
			)
			return
		}
		h.log.Warn("hand: PnL SQL query failed, falling back to JetStream drain", "err", err)
	}

	if tl := h.helmRuntime.TradeLog; tl != nil {
		trades, err := tl.Since(ctx, h.id.String(), time.Time{})
		if err == nil && len(trades) > 0 {
			var totalPnL, totalCommission decimal.Decimal
			var wins, losses int64
			for _, t := range trades {
				pnl, _ := decimal.NewFromString(t.GrossPnL)
				comm, _ := decimal.NewFromString(t.Commission)
				totalPnL = totalPnL.Add(pnl)
				totalCommission = totalCommission.Add(comm)
				// t.Commission is total fees (entry+exit) — net directly, no per-leg split needed.
				netPnL := pnl.Sub(comm)
				if netPnL.IsPositive() {
					wins++
				} else if netPnL.IsNegative() {
					losses++
				}
			}
			h.metrics.mu.Lock()
			h.metrics.totalPnL = totalPnL
			h.metrics.totalCommission = totalCommission
			h.metrics.winCount = wins
			h.metrics.lossCount = losses
			h.metrics.mu.Unlock()
			h.log.Info("hand: PnL restored from JetStream trade log",
				"trades", len(trades),
				"total_pnl", totalPnL, "total_commission", totalCommission,
				"wins", wins, "losses", losses,
			)
			return
		}
		if err != nil {
			h.log.Warn("hand: JetStream trade log unavailable, falling back to poslog", "err", err)
		}
	}

	// Fallback: poslog PositionClosed events (commission may be zero for old events).
	var totalPnL, totalCommission decimal.Decimal
	var wins, losses int64
	for _, e := range events {
		if e.Kind != poslog.KindPositionClosed {
			continue
		}
		var p poslog.PositionClosedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		pnl, _ := decimal.NewFromString(p.RealizedPnL)
		totalPnL = totalPnL.Add(pnl)
		commission, _ := decimal.NewFromString(p.Commission)
		totalCommission = totalCommission.Add(commission)

		// netPnL nets out both sides' commission, same derivation as applyExitFill:
		// entry fee is backed out of DeployedCapital (= sum(qty*price + fee) across
		// all entry fills), since PositionClosedPayload has no separate entry-fee field.
		netPnL := pnl.Sub(commission)
		qty, _ := decimal.NewFromString(p.Qty)
		entryPrice, _ := decimal.NewFromString(p.EntryPrice)
		deployedCapital, _ := decimal.NewFromString(p.DeployedCapital)
		if qty.IsPositive() && deployedCapital.IsPositive() {
			if entryCommission := deployedCapital.Sub(qty.Mul(entryPrice)); entryCommission.IsPositive() {
				netPnL = netPnL.Sub(entryCommission)
			}
		}
		if netPnL.IsPositive() {
			wins++
		} else if netPnL.IsNegative() {
			losses++
		}
	}
	h.metrics.mu.Lock()
	h.metrics.totalPnL = totalPnL
	h.metrics.totalCommission = totalCommission
	h.metrics.winCount = wins
	h.metrics.lossCount = losses
	h.metrics.mu.Unlock()
	h.log.Info("hand: PnL restored from poslog (fallback)",
		"total_pnl", totalPnL, "total_commission", totalCommission,
		"wins", wins, "losses", losses,
	)
}

// RestoreGuard rebuilds the sliding-window edge-risk guard (guard.ring/head/full/
// consecLoss) from the hand's last WindowTrades closed trades in PostgreSQL. Without
// this, checkEdgeRisk's ring buffer is always fresh/zero-valued after any restart
// (NewHand builds a zero-valued handEdgeGuard unconditionally) — a hand mid-way
// through a losing streak (e.g. 4/5 toward MaxConsecLoss) would silently have that
// streak reset to 0 on restart. That defeats the guard exactly when a losing streak
// is in progress.
//
// consecLoss is deliberately bounded by the same WindowTrades query, not the full
// trade history: any win within the window resets the streak to 0 anyway, and a
// streak longer than WindowTrades would already have tripped MaxTotalLossPct/
// MaxAvgLossPct first in practice — so nobody configures MaxConsecLoss > WindowTrades.
//
// Only rebuilds state — does not re-run checkEdgeRisk's breach evaluation or
// auto-stop. If a threshold was already breached before shutdown, checkEdgeRisk would
// have auto-stopped the hand at that time (persisted status reflects it); this only
// ensures the NEXT live closing fill after restart evaluates against correctly
// restored history instead of an empty window.
func (h *Hand) RestoreGuard(ctx context.Context) {
	if h.guard.cfg.WindowTrades == 0 {
		return
	}
	ps := h.helmRuntime.PnLSummer
	if ps == nil {
		return
	}

	closes, err := ps.RecentClosedPnL(ctx, h.id, h.guard.cfg.WindowTrades)
	if err != nil {
		h.log.Warn("hand: edge-risk guard restore query failed", "err", err)
		return
	}
	if len(closes) == 0 {
		return
	}

	for _, pnl := range closes {
		h.guard.push(pnl)
	}

	// Count trailing losses from the most recent close backward, same rule
	// checkEdgeRisk uses live: negative pnl increments the streak, a non-negative
	// close resets it.
	consecLoss := 0
	for i := len(closes) - 1; i >= 0; i-- {
		if !closes[i].IsNegative() {
			break
		}
		consecLoss++
	}
	h.guard.consecLoss = consecLoss

	h.log.Info("hand: edge-risk guard restored from postgres",
		"closes_replayed", len(closes), "consec_loss", consecLoss,
		"window_trades", h.guard.cfg.WindowTrades)
}

// RestoreCounters rebuilds the activity counters (signals/orders) from the persisted
// event log on startup, in one aggregate query. PnL/win/loss come from RestorePnL.
//
// Every counter is event-backed 1:1 with a code; latestSignalLagMs is a live gauge
// (lag of the most recent signal) and is intentionally left at 0 — restoring a stale
// point-in-time lag is meaningless.
func (h *Hand) RestoreCounters(ctx context.Context) {
	ec := h.helmRuntime.EventCounter
	if ec == nil {
		return
	}
	counts, err := ec.CountHandEvents(ctx, h.id)
	if err != nil {
		h.log.Warn("hand: counter restore failed", "err", err)
		return
	}
	if len(counts) == 0 {
		return
	}
	var filtered int64
	for _, c := range []int{
		CodeSignalStale, CodeSignalHelmPaused, CodeSignalRateLimited,
		CodeSignalDoNothing, CodeSignalMaxUnits, CodeSignalRejected, CodeSignalNoPosition,
	} {
		filtered += counts[c]
	}
	h.metrics.signalsReceived.Store(counts[CodeSignalReceived])
	h.metrics.signalsFiltered.Store(filtered)
	h.metrics.signalsDropped.Store(counts[CodeSignalDropped])
	h.metrics.tradesApproved.Store(counts[CodeTradeApproved])
	h.metrics.ordersPlaced.Store(counts[CodeOrderPlaced])
	h.metrics.ordersFilled.Store(counts[CodeOrderFilled])
	h.metrics.ordersFailed.Store(counts[CodeOrderFailed])
	h.log.Info("hand: counters restored from event log",
		"signals", counts[CodeSignalReceived], "filtered", filtered,
		"approved", counts[CodeTradeApproved], "placed", counts[CodeOrderPlaced],
		"filled", counts[CodeOrderFilled], "failed", counts[CodeOrderFailed],
		"dropped", counts[CodeSignalDropped],
	)
}

// applyFuturesLeverage calls SetLeverage on the exchange if the hand is configured
// for futures and the exchange supports it. Non-blocking on failure (just logs).
func (h *Hand) applyFuturesLeverage(ctx context.Context, symbol string, futures *domain.FuturesConfig) {
	if futures == nil || futures.Leverage <= 0 {
		return
	}
	setter, ok := h.helmRuntime.Exchange.(exchange.LeverageSetter)
	if !ok {
		return
	}
	marginType := string(futures.MarginType)
	if marginType == "" {
		marginType = "isolated"
	}
	if err := setter.SetLeverage(ctx, h.helmRuntime.Creds, symbol, futures.Leverage, marginType); err != nil {
		h.log.Warn("hand: set leverage failed (non-fatal)", "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType, "err", err)
	} else {
		h.log.Info("hand: leverage set", "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType)
		h.helmRuntime.EmitEvent(natsapi.HelmEvent{
			HandID: h.id.String(),
			Code:   CodeHandLeverageSet,
			Symbol: symbol,
			Reason: fmt.Sprintf("leverage=%d margin_type=%s", futures.Leverage, marginType),
			Msg:    "hand: leverage & margin configured",
		})
	}
}
