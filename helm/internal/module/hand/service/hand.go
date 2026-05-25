package service

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

func (s *Service) Get(id uuid.UUID) (*runtime.HandRef, error) { return s.getOrLoad(id) }

// GetSummary returns the HandSummary for any hand, including terminal ones stored only in DB.
func (s *Service) GetSummary(id uuid.UUID) (*domain.HandSummary, error) {
	s.mu.RLock()
	bi, ok := s.hands[id]
	s.mu.RUnlock()
	if ok {
		s := bi.Summary()
		return &s, nil
	}
	// Not in memory — may be a terminal hand; read directly from DB.
	data, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if !data.Status.IsTerminal() {
		return nil, fmt.Errorf("hand %q not found in runtime", id)
	}
	s2 := data.SummaryFromDB()
	return &s2, nil
}

func (s *Service) GetHand(id uuid.UUID) *runtime.Hand {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return nil
	}
	return bi.Runner
}

func (s *Service) List() []domain.HandSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.HandSummary, 0, len(s.hands))
	for _, bi := range s.hands {
		out = append(out, bi.Summary())
	}
	return out
}

func (s *Service) ListByHelm(orchID uuid.UUID) []domain.HandSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.HandSummary
	for _, bi := range s.hands {
		if bi.Data.HelmID == orchID {
			out = append(out, bi.Summary())
		}
	}
	return out
}

func (s *Service) Create(cfg domain.HandConfig) (*runtime.HandRef, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("hand name is required")
	}
	if cfg.HelmID == uuid.Nil {
		return nil, fmt.Errorf("helm_id is required")
	}
	if len(cfg.Symbols) == 0 {
		return nil, fmt.Errorf("at least one symbol is required")
	}
	for _, sym := range cfg.Symbols {
		if sym == "" {
			return nil, fmt.Errorf("symbols must not contain empty strings")
		}
	}
	if !cfg.AllocatedCapital.IsPositive() {
		return nil, fmt.Errorf("allocated capital must be greater than zero")
	}
	if err := cfg.Strategy.Validate(); err != nil {
		return nil, err
	}
	cfg.Defaults()
	if err := validateSizingConfig(cfg.Position); err != nil {
		return nil, err
	}

	rt, err := s.registry.Get(cfg.HelmID)
	if err != nil {
		return nil, fmt.Errorf("helm runtime not found: %w", err)
	}
	if cfg.Market == domain.MarketTypeFutures {
		ft, ok := rt.Exchange.(interface{ SupportsFutures() bool })
		if !ok || !ft.SupportsFutures() {
			return nil, fmt.Errorf("exchange %q does not support futures trading", rt.Exchange.Name())
		}
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
		return nil, fmt.Errorf("save hand: %w", err)
	}

	strat, tact := runtime.BuildHandComponents(data)
	hand := runtime.NewHand(id, cfg.HelmID, rt, strat, tact, data.Position.Pyramid, data.Position.MaxUnits, runtime.SignalTTLFor(data), data.Futures, data.OrderType, data.LimitTimeoutSec, data.LimitFallback, data.Risk, data.AllocatedCapital)
	setMeta(hand, data)
	rt.AddHand(hand)
	bi := &runtime.HandRef{Data: data, Runner: hand, Exchange: rt.Exchange}
	s.mu.Lock()
	s.hands[id] = bi
	s.mu.Unlock()

	slog.Info("hand created", "id", id, "name", data.Name, "helm_id", data.HelmID)
	return bi, nil
}

// Update patches mutable fields: Name, Position sizing, and Risk exit rules.
// Symbols, Strategy, Type, and Market are immutable after creation.
// The hand must be stopped before updating.
func (s *Service) Update(id uuid.UUID, patch domain.HandConfig) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Runner.IsRunning() {
		return fmt.Errorf("hand %q is running — stop it first", id)
	}

	orchID := bi.Data.HelmID

	// Phase 1: Persist to DB — pure domain mutation, no runtime side-effects.
	if err := s.repo.Update(id, func(d *domain.Hand) error {
		if patch.Name != "" {
			d.Name = patch.Name
		}
		if patch.Position.SizeMode != "" {
			d.Position = patch.Position
		}
		if patch.Risk != (domain.HandRiskConfig{}) {
			d.Risk = patch.Risk
		}
		return nil
	}); err != nil {
		return err
	}

	// Phase 2: Reload and rebuild in-memory hand with updated config.
	updated, err := s.repo.Get(id)
	if err != nil {
		slog.Warn("hand updated in DB but reload failed — in-memory state stale", "id", id, "err", err)
		return nil
	}
	bi.Data = updated

	if rt, _ := s.registry.Get(orchID); rt != nil {
		rt.RemoveHand(id.String())
		strat, tact := runtime.BuildHandComponents(updated)
		bi.Runner = runtime.NewHand(updated.ID, orchID, rt, strat, tact, updated.Position.Pyramid, updated.Position.MaxUnits, runtime.SignalTTLFor(updated), updated.Futures, updated.OrderType, updated.LimitTimeoutSec, updated.LimitFallback, updated.Risk, updated.AllocatedCapital)
		setMeta(bi.Runner, updated)
		rt.AddHand(bi.Runner)
	}

	slog.Info("hand updated", "id", id, "name", updated.Name)
	return nil
}

func (s *Service) Delete(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Runner.IsRunning() {
		return fmt.Errorf("hand %q is running — stop it first", id)
	}
	if rt, _ := s.registry.Get(bi.Data.HelmID); rt != nil {
		rt.RemoveHand(id.String())
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.hands, id)
	s.mu.Unlock()
	slog.Info("hand deleted", "id", id)
	return nil
}

// validateSizingConfig enforces cross-field invariants for sizing modes.
// Called after Defaults() so SizeMode is always non-empty.
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
	}
	return nil
}

func setMeta(hand *runtime.Hand, b *domain.Hand) {
	if len(b.Symbols) > 0 {
		hand.Symbol = b.Symbols[0]
	}
	hand.StrategyName = "script"
	hand.CapitalPct = b.Position.MaxPositionPct
}
