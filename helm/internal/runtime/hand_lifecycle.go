package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
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
	edgeRisk domain.HandRiskConfig,
	allocatedCap decimal.Decimal,
) *Hand {
	if maxUnits <= 0 {
		maxUnits = 1
	}
	var tradeRing []decimal.Decimal
	if edgeRisk.WindowTrades > 0 {
		tradeRing = make([]decimal.Decimal, edgeRisk.WindowTrades)
	}
	return &Hand{
		id:              id,
		helmID:          helmID,
		helmRuntime:     rt,
		strategy:        strat,
		tactician:       tact,
		limiter:         rate.NewLimiter(rate.Every(1*time.Second), 5),
		pyramid:         pyramid,
		maxUnits:        maxUnits,
		futuresConfig:   futuresConfig,
		signalTTL:       signalTTL,
		orderType:       orderType,
		limitTimeoutSec: limitTimeoutSec,
		limitFallback:   limitFallback,
		edgeRisk:        edgeRisk,
		allocatedCap:    allocatedCap,
		tradeRing:       tradeRing,
		leverageApplied: make(map[string]bool),
		Signals:         make(chan Signal, 1),
		UrgentSignals:   make(chan Signal, 4),
		fillCh:          make(chan exchange.OrderEvent, 8),
		seenFills:       make(map[string]struct{}),
		orders:          make([]domain.Order, 0, 256),
		pendingExits:    make(map[string]exitPending),
		exitLevels:      make(map[string]exitLevel),
		pos:             position.NewHandPositions(pyramid, maxUnits),
		pendingOrderPos: make(map[string]string),
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

	sc := tactics.SizingConfig{
		Mode:             sizingMode,
		AllocatedCapital: b.AllocatedCapital,
		UnitCapital:      b.Position.UnitCapital,
		UnitPct:          b.Position.UnitPct,
		RiskPerTradePct:  b.Position.RiskPerTradePct,
		MaxPositionPct:   b.Position.MaxPositionPct,
		FixedQty:         b.Position.FixedQty,
		FixedQuoteQty:    b.Position.FixedQuoteQty,
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
		h.helmRuntime.Portfolio.RestorePosition(leg.Symbol, leg.Side, leg.Qty, leg.EntryPrice, currentPrice, leg.OpenedAt)
	}

	h.mu.Lock()
	// Take ownership of the replayed position state.
	h.pos = hp

	// Rebuild pending order → position mapping for legs still awaiting a fill.
	for _, leg := range hp.ActiveLegs() {
		if leg.HasPendingOrder() {
			h.pendingOrderPos[leg.PendingOrderID] = leg.PositionID
		}
	}

	// Rebuild local exit-level guards.
	// Pyramid: SL/TP from pos (latest signal levels); non-pyramid: per-leg.
	if h.pyramid {
		if pos.StopLoss.IsPositive() || pos.TakeProfit.IsPositive() {
			h.exitLevels[pos.Symbol] = exitLevel{
				Side:       pos.Side,
				StopLoss:   pos.StopLoss,
				TakeProfit: pos.TakeProfit,
			}
		}
	} else {
		for _, leg := range hp.ActiveLegs() {
			if leg.StopLoss.IsPositive() || leg.TakeProfit.IsPositive() {
				h.exitLevels[leg.Symbol] = exitLevel{
					Side:       leg.Side,
					StopLoss:   leg.StopLoss,
					TakeProfit: leg.TakeProfit,
				}
			}
		}
	}
	h.mu.Unlock()
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
