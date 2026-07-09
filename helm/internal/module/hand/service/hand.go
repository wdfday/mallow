package service

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

// Get returns a HandSummary for any hand (live or terminal).
// For live hands: includes runtime health and metrics.
// For terminal hands: returns the persisted snapshot from DB.
func (s *Service) Get(id uuid.UUID) (domain.HandSummary, error) {
	data, err := s.repo.Get(id)
	if err != nil {
		return domain.HandSummary{}, err
	}
	if rt, err := s.registry.Get(data.HelmID); err == nil {
		if h, _, ok := rt.GetHandEntry(id.String()); ok {
			return runtime.BuildHandSummary(h, data), nil
		}
	}
	return data.SummaryFromDB(), nil
}

// GetHand returns the live Hand runner for the given ID, or nil if not in memory.
func (s *Service) GetHand(id uuid.UUID) *runtime.Hand {
	data, err := s.repo.Get(id)
	if err != nil {
		return nil
	}
	rt, err := s.registry.Get(data.HelmID)
	if err != nil {
		return nil
	}
	h, _, ok := rt.GetHandEntry(id.String())
	if !ok {
		return nil
	}
	return h
}

// List returns all hands: live from registry + terminal from DB.
func (s *Service) List() []domain.HandSummary {
	all := s.repo.All()
	out := make([]domain.HandSummary, 0, len(all))
	for _, data := range all {
		if rt, err := s.registry.Get(data.HelmID); err == nil {
			if h, _, ok := rt.GetHandEntry(data.ID.String()); ok {
				out = append(out, runtime.BuildHandSummary(h, data))
				continue
			}
		}
		out = append(out, data.SummaryFromDB())
	}
	return out
}

// ListLive returns only hands currently wired into any HelmRuntime.
func (s *Service) ListLive() []domain.HandSummary {
	var out []domain.HandSummary
	for _, rt := range s.registry.All() {
		out = append(out, rt.LiveHandSummaries()...)
	}
	return out
}

// ListByHelm returns all hands for a helm: live from registry, terminal from DB.
func (s *Service) ListByHelm(orchID uuid.UUID) []domain.HandSummary {
	all := s.repo.AllByHelm(orchID)
	out := make([]domain.HandSummary, 0, len(all))
	rt, _ := s.registry.Get(orchID)
	for _, data := range all {
		if rt != nil {
			if h, _, ok := rt.GetHandEntry(data.ID.String()); ok {
				out = append(out, runtime.BuildHandSummary(h, data))
				continue
			}
		}
		out = append(out, data.SummaryFromDB())
	}
	return out
}

// ListByHelmLive returns only live (in-memory) hands for a given helm.
func (s *Service) ListByHelmLive(orchID uuid.UUID) []domain.HandSummary {
	rt, err := s.registry.Get(orchID)
	if err != nil {
		return nil
	}
	return rt.LiveHandSummaries()
}

// ListByHelms returns hands across several helms in one shot.
//   - live=false: one DB query (WHERE helm_id IN) + per-hand runtime enrich.
//   - live=true:  only hands wired into each helm's runtime (in-memory, no DB).
//
// Caller is responsible for ownership checks per helm before calling.
func (s *Service) ListByHelms(helmIDs []uuid.UUID, live bool) []domain.HandSummary {
	if len(helmIDs) == 0 {
		return []domain.HandSummary{}
	}
	if live {
		out := make([]domain.HandSummary, 0)
		for _, id := range helmIDs {
			if rt, err := s.registry.Get(id); err == nil {
				out = append(out, rt.LiveHandSummaries()...)
			}
		}
		return out
	}

	// Full: single batched DB read, then enrich live ones from their runtime.
	all := s.repo.AllByHelms(helmIDs)
	out := make([]domain.HandSummary, 0, len(all))
	rtCache := make(map[uuid.UUID]*runtime.HelmRuntime, len(helmIDs))
	for _, data := range all {
		rt, ok := rtCache[data.HelmID]
		if !ok {
			rt, _ = s.registry.Get(data.HelmID)
			rtCache[data.HelmID] = rt
		}
		if rt != nil {
			if h, _, found := rt.GetHandEntry(data.ID.String()); found {
				out = append(out, runtime.BuildHandSummary(h, data))
				continue
			}
		}
		out = append(out, data.SummaryFromDB())
	}
	return out
}

func (s *Service) Create(cfg domain.HandConfig) (domain.HandSummary, error) {
	if cfg.Market == domain.MarketTypeFutures {
		return domain.HandSummary{}, fmt.Errorf("futures trading is not yet supported")
	}
	if cfg.OrderType == domain.OrderTypeLimit {
		return domain.HandSummary{}, fmt.Errorf("limit order type is not yet supported — use market")
	}
	if cfg.Name == "" {
		return domain.HandSummary{}, fmt.Errorf("hand name is required")
	}
	if cfg.HelmID == uuid.Nil {
		return domain.HandSummary{}, fmt.Errorf("helm_id is required")
	}
	if len(cfg.Symbols) == 0 {
		return domain.HandSummary{}, fmt.Errorf("at least one symbol is required")
	}
	for _, sym := range cfg.Symbols {
		if sym == "" {
			return domain.HandSummary{}, fmt.Errorf("symbols must not contain empty strings")
		}
	}
	if !cfg.AllocatedCapital.IsPositive() {
		return domain.HandSummary{}, fmt.Errorf("allocated capital must be greater than zero")
	}
	if err := cfg.Strategy.Validate(); err != nil {
		return domain.HandSummary{}, err
	}
	cfg.Defaults()
	if err := validateSizingConfig(cfg.Position); err != nil {
		return domain.HandSummary{}, err
	}

	rt, err := s.registry.Get(cfg.HelmID)
	if err != nil {
		return domain.HandSummary{}, fmt.Errorf("helm runtime not found: %w", err)
	}
	if cfg.Market == domain.MarketTypeFutures {
		if ft, ok := rt.Exchange.(interface{ SupportsFutures() bool }); !ok || !ft.SupportsFutures() {
			return domain.HandSummary{}, fmt.Errorf("exchange %q does not support futures trading", rt.Exchange.Name())
		}
		if cfg.Futures != nil && cfg.Futures.MarginType == domain.MarginTypeIsolated {
			if iso, ok := rt.Exchange.(exchange.IsolatedMarginTrader); !ok || !iso.SupportsIsolatedMargin() {
				return domain.HandSummary{}, fmt.Errorf("exchange %q does not support isolated margin — use cross margin", rt.Exchange.Name())
			}
		}
	} else if st, ok := rt.Exchange.(exchange.SpotSupportChecker); ok && !st.SupportsSpot() {
		// Futures-only exchange (e.g. fbinance) — its PlaceOrder always hits the futures
		// API, so a spot hand here would silently trade futures instead. See SpotSupportChecker.
		return domain.HandSummary{}, fmt.Errorf("exchange %q does not support spot trading — use market: futures", rt.Exchange.Name())
	}

	if err := s.heraldValidate(cfg, rt); err != nil {
		return domain.HandSummary{}, err
	}
	id := s.repo.GenerateID()
	data := &domain.Hand{
		ID:        id,
		HelmID:    cfg.HelmID,
		Status:    domain.HandStatusStopped,
		CreatedAt: time.Now().UTC(),
	}
	data.ApplyConfig(cfg)

	if err := s.repo.Save(data); err != nil {
		return domain.HandSummary{}, fmt.Errorf("save hand: %w", err)
	}

	strat, tact := runtime.BuildHandComponents(data)
	hand := runtime.NewHand(id, cfg.HelmID, rt, strat, tact, data.Position.Pyramid, data.Position.MaxUnits, runtime.SignalTTLFor(data), data.Futures, data.OrderType, data.LimitTimeoutSec, data.LimitFallback, data.Guard, data.AllocatedCapital)
	setMeta(hand, data)
	rt.AddHand(hand, data)

	slog.Info("hand created", "id", id, "name", data.Name, "helm_id", data.HelmID)
	return runtime.BuildHandSummary(hand, data), nil
}

// Update patches mutable fields: Name, Position sizing, and Guard exit rules.
// Symbols, Strategy, Type, and Market are immutable after creation.
// The hand must be stopped before updating.
func (s *Service) Update(id uuid.UUID, patch domain.HandConfig) error {
	data, err := s.repo.Get(id)
	if err != nil {
		return err
	}
	helmID := data.HelmID

	if rt, err := s.registry.Get(helmID); err == nil {
		if h, _, ok := rt.GetHandEntry(id.String()); ok && h.IsRunning() {
			return fmt.Errorf("hand %q is running — stop it first", id)
		}
	}

	if err := s.repo.Update(id, func(d *domain.Hand) error {
		if patch.Name != "" {
			d.Name = patch.Name
		}
		if patch.Position.SizeMode != "" {
			d.Position = patch.Position
		}
		if patch.Guard != (domain.HandGuardConfig{}) {
			d.Guard = patch.Guard
		}
		return nil
	}); err != nil {
		return err
	}

	updated, err := s.repo.Get(id)
	if err != nil {
		slog.Warn("hand updated in DB but reload failed — in-memory state stale", "id", id, "err", err)
		return nil
	}

	if rt, err := s.registry.Get(helmID); err == nil {
		rt.RemoveHand(id.String())
		strat, tact := runtime.BuildHandComponents(updated)
		hand := runtime.NewHand(updated.ID, helmID, rt, strat, tact, updated.Position.Pyramid, updated.Position.MaxUnits, runtime.SignalTTLFor(updated), updated.Futures, updated.OrderType, updated.LimitTimeoutSec, updated.LimitFallback, updated.Guard, updated.AllocatedCapital)
		setMeta(hand, updated)
		rt.AddHand(hand, updated)
	}

	slog.Info("hand updated", "id", id, "name", updated.Name)
	return nil
}

// AllocateCapital adds delta to the hand's allocated capital and updates both DB and runtime.
func (s *Service) AllocateCapital(id, helmID uuid.UUID, delta decimal.Decimal) (decimal.Decimal, error) {
	data, err := s.repo.Get(id)
	if err != nil {
		return decimal.Zero, err
	}

	newCapital := data.AllocatedCapital.Add(delta)
	if !newCapital.IsPositive() {
		return decimal.Zero, fmt.Errorf("new allocated capital must be greater than zero")
	}

	if err := s.repo.Update(id, func(d *domain.Hand) error {
		d.AllocatedCapital = newCapital
		return nil
	}); err != nil {
		return decimal.Zero, err
	}

	if rt, err := s.registry.Get(helmID); err == nil {
		rt.SetAllocatedCapitalOnHand(id.String(), newCapital)
	}

	return newCapital, nil
}

// validateSizingConfig enforces cross-field invariants for sizing modes.
func validateSizingConfig(p domain.PositionConfig) error {
	switch p.SizeMode {
	case "fixed_qty":
		if p.FixedQty.IsZero() {
			return fmt.Errorf("position: fixed_qty mode requires fixed_qty > 0")
		}
	case "quote_qty":
		if p.FixedQuoteQty.IsZero() {
			return fmt.Errorf("position: quote_qty mode requires fixed_quote_qty > 0")
		}
	case "volatility":
		if p.RiskPerTradePct == 0 {
			return fmt.Errorf("position: volatility mode requires risk_per_trade_pct > 0")
		}
	case "fixed_fractional":
		if p.RiskPerTradePct == 0 {
			return fmt.Errorf("position: fixed_fractional mode requires risk_per_trade_pct > 0")
		}
	case "percent_equity":
		if p.UnitPct == 0 {
			return fmt.Errorf("position: percent_equity mode requires unit_pct > 0")
		}
	}
	return nil
}

func setMeta(hand *runtime.Hand, b *domain.Hand) {
	if len(b.Symbols) > 0 {
		hand.Symbol = b.Symbols[0]
	}
	hand.StrategyName = "script"
	hand.Timeframe = b.Strategy.Timeframe
	hand.CapitalPct = b.Position.MaxPositionPct
}
