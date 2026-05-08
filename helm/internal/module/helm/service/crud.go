package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/helm/domain"
)

// CreateForAccountReq is the input for auto-creating an orchestrator when an account is linked.
type CreateForAccountReq struct {
	UserID    uuid.UUID
	AccountID uuid.UUID
	Name      string
	Capital   decimal.Decimal
	Exchange  domain.ExchangeConfig
	Portfolio domain.PortfolioConfig
	Risk      domain.RiskConfig
}

// CreateForAccount persists a new orchestrator config (disabled by default) and spawns its runtime.
// Called by the NATS account.linked event handler.
func (s *Service) CreateForAccount(req CreateForAccountReq) (*domain.HelmConfig, error) {
	if req.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id is required")
	}
	if req.AccountID == uuid.Nil {
		return nil, fmt.Errorf("account_id is required")
	}
	if req.Name == "" {
		req.Name = "My Orchestrator"
	}
	req.Portfolio.Defaults()
	req.Risk.Defaults()

	cfg := &domain.HelmConfig{
		ID:        uuid.New(),
		UserID:    req.UserID,
		AccountID: req.AccountID,
		Name:      req.Name,
		Capital:   req.Capital.InexactFloat64(),
		Exchange:  req.Exchange,
		Portfolio: req.Portfolio,
		Risk:      req.Risk,
		Enabled:   false,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.repo.Save(cfg); err != nil {
		return nil, fmt.Errorf("save orchestrator: %w", err)
	}
	if err := s.spawner.Spawn(cfg); err != nil {
		return cfg, fmt.Errorf("spawn runtime (config saved): %w", err)
	}
	s.spawner.SyncOne(cfg.ID)
	return cfg, nil
}

// DeleteForAccount tears down and removes the orchestrator linked to accountID.
// Called by the NATS account.unlinked event handler.
func (s *Service) DeleteForAccount(accountID uuid.UUID) error {
	cfg, err := s.repo.GetByAccountID(accountID)
	if err != nil {
		return err
	}
	botIDs := s.spawner.Teardown(cfg.ID)
	if s.hands != nil {
		if len(botIDs) > 0 {
			s.hands.StopBots(botIDs) // deregister from herald + stop running bots
		}
		s.hands.PurgeBots(botIDs) // remove from in-memory map
	}
	return s.repo.Delete(cfg.ID)
}

// UpdateReq is the patch payload for updating an orchestrator config.
type UpdateReq struct {
	Name      string
	Capital   decimal.Decimal
	Portfolio *domain.PortfolioConfig
	Risk      *domain.RiskConfig
}

// Update patches an orchestrator config and refreshes live risk parameters.
func (s *Service) Update(id uuid.UUID, req UpdateReq) (*domain.HelmConfig, error) {
	var updated *domain.HelmConfig
	if err := s.repo.Update(id, func(o *domain.HelmConfig) error {
		if req.Name != "" {
			o.Name = req.Name
		}
		if req.Capital.IsPositive() {
			o.Capital = req.Capital.InexactFloat64()
		}
		if req.Portfolio != nil {
			o.Portfolio = *req.Portfolio
		}
		if req.Risk != nil {
			o.Risk = *req.Risk
		}
		updated = o
		return nil
	}); err != nil {
		return nil, err
	}
	// Refresh live risk manager so new limits take effect immediately.
	if req.Portfolio != nil || req.Risk != nil {
		_ = s.spawner.UpdateRiskConfig(id, updated.Portfolio, updated.Risk)
	}
	return updated, nil
}

// Delete removes an orchestrator config and tears down its runtime.
func (s *Service) Delete(id uuid.UUID) error {
	botIDs := s.spawner.Teardown(id)
	if s.hands != nil {
		if len(botIDs) > 0 {
			s.hands.StopBots(botIDs)
		}
		s.hands.PurgeBots(botIDs)
	}
	return s.repo.Delete(id)
}
