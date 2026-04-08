package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"orchestrator/internal/module/orchesrator/domain"
)

// CreateForAccountReq is the input for auto-creating an orchestrator when an account is linked.
type CreateForAccountReq struct {
	UserID    uuid.UUID
	AccountID uuid.UUID
	Name      string
	Capital   decimal.Decimal
	Exchange  domain.ExchangeConfig
	Risk      domain.RiskConfig
}

// CreateForAccount persists a new orchestrator config (disabled by default) and spawns its runtime.
// Called by the NATS account.linked event handler.
func (s *Service) CreateForAccount(req CreateForAccountReq) (*domain.OrchestratorConfig, error) {
	if req.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id is required")
	}
	if req.AccountID == uuid.Nil {
		return nil, fmt.Errorf("account_id is required")
	}
	if req.Name == "" {
		req.Name = "My Orchestrator"
	}
	req.Risk.Defaults()

	cfg := &domain.OrchestratorConfig{
		ID:        uuid.New(),
		UserID:    req.UserID,
		AccountID: req.AccountID,
		Name:      req.Name,
		Capital:   req.Capital,
		Exchange:  req.Exchange,
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
	if s.bots != nil && len(botIDs) > 0 {
		s.bots.StopBots(botIDs)
	}
	return s.repo.Delete(cfg.ID)
}

// UpdateReq is the patch payload for updating an orchestrator config.
type UpdateReq struct {
	Name    string
	Capital decimal.Decimal
	Risk    *domain.RiskConfig
	Status  string
}

// Update patches an orchestrator config.
func (s *Service) Update(id uuid.UUID, req UpdateReq) (*domain.OrchestratorConfig, error) {
	var updated *domain.OrchestratorConfig
	err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		if req.Name != "" {
			o.Name = req.Name
		}
		if req.Capital.IsPositive() {
			o.Capital = req.Capital
		}
		if req.Risk != nil {
			o.Risk = *req.Risk
		}
		updated = o
		return nil
	})
	return updated, err
}

// Delete removes an orchestrator config and tears down its runtime.
func (s *Service) Delete(id uuid.UUID) error {
	botIDs := s.spawner.Teardown(id)
	if s.bots != nil && len(botIDs) > 0 {
		s.bots.StopBots(botIDs)
	}
	return s.repo.Delete(id)
}
