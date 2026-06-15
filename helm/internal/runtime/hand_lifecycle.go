package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/position"
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
			h.pendingOrderPos[leg.PendingOrderID] = leg.PositionID
			h.helmRuntime.TrackOrder(leg.PendingOrderID, h.id.String())
		}
	}

	// Rebuild local exit-level guards.
	// Pyramid: SL/TP from pos (latest signal levels); non-pyramid: per-leg.
	if h.cfg.pyramid {
		if pos.StopLoss.IsPositive() || pos.TakeProfit.IsPositive() {
			var bracketIDs []string
			if pl := hp.PrimaryLeg(); pl != nil {
				bracketIDs = pl.ExchangeOrderIDs
			}
			h.exitLevels[pos.Symbol] = exitLevel{
				Side:             pos.Side,
				StopLoss:         pos.StopLoss,
				TakeProfit:       pos.TakeProfit,
				ExchangeOrderIDs: bracketIDs,
			}
		}
	} else {
		for _, leg := range hp.ActiveLegs() {
			if leg.StopLoss.IsPositive() || leg.TakeProfit.IsPositive() {
				h.exitLevels[leg.Symbol] = exitLevel{
					Side:             leg.Side,
					StopLoss:         leg.StopLoss,
					TakeProfit:       leg.TakeProfit,
					ExchangeOrderIDs: leg.ExchangeOrderIDs,
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
			slog.Info("hand: PnL restored from postgres",
				"hand_id", h.id,
				"total_pnl", totalPnL, "total_commission", totalCommission,
				"wins", wins, "losses", losses,
			)
			return
		}
		slog.Warn("hand: PnL SQL query failed, falling back to JetStream drain", "hand_id", h.id, "err", err)
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
				if pnl.IsPositive() {
					wins++
				} else if pnl.IsNegative() {
					losses++
				}
			}
			h.metrics.mu.Lock()
			h.metrics.totalPnL = totalPnL
			h.metrics.totalCommission = totalCommission
			h.metrics.winCount = wins
			h.metrics.lossCount = losses
			h.metrics.mu.Unlock()
			slog.Info("hand: PnL restored from JetStream trade log",
				"hand_id", h.id, "trades", len(trades),
				"total_pnl", totalPnL, "total_commission", totalCommission,
				"wins", wins, "losses", losses,
			)
			return
		}
		if err != nil {
			slog.Warn("hand: JetStream trade log unavailable, falling back to poslog", "hand_id", h.id, "err", err)
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
		if pnl.IsPositive() {
			wins++
		} else if pnl.IsNegative() {
			losses++
		}
		commission, _ := decimal.NewFromString(p.Commission)
		totalCommission = totalCommission.Add(commission)
	}
	h.metrics.mu.Lock()
	h.metrics.totalPnL = totalPnL
	h.metrics.totalCommission = totalCommission
	h.metrics.winCount = wins
	h.metrics.lossCount = losses
	h.metrics.mu.Unlock()
	slog.Info("hand: PnL restored from poslog (fallback)",
		"hand_id", h.id,
		"total_pnl", totalPnL, "total_commission", totalCommission,
		"wins", wins, "losses", losses,
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
		slog.Warn("hand: set leverage failed (non-fatal)", "hand_id", h.id, "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType, "err", err)
	} else {
		slog.Info("hand: leverage set", "hand_id", h.id, "symbol", symbol,
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
