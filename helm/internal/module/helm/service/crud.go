package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/helm/domain"
	helmDto "mallow/helm/internal/module/helm/dto"
)

// CreateForAccount ensures exactly one Helm exists for the given account and that its
// runtime is spawned. Idempotent: if a Helm already exists for the account it re-spawns
// the runtime (safe no-op if already running) and returns the existing record.
// Called by the NATS helm.accounts.linked event handler.
func (s *Service) CreateForAccount(req helmDto.CreateForAccountReq) (*domain.Helm, error) {
	if req.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id is required")
	}
	if req.AccountID == uuid.Nil {
		return nil, fmt.Errorf("account_id is required")
	}

	// Build transient exchange config (credentials never persisted).
	exchCfg := req.Exchange
	exchCfg.APIKey = req.Creds.APIKey
	exchCfg.APISecret = req.Creds.APISecret
	exchCfg.Passphrase = req.Creds.Passphrase

	// Idempotency: if a Helm already exists for this account, ensure its runtime
	// is running and return the existing record instead of creating a duplicate.
	if existing, err := s.repo.GetByAccountID(req.AccountID); err == nil {
		if spawnErr := s.spawner.Spawn(existing, exchCfg, decimal.Zero); spawnErr != nil {
			return existing, fmt.Errorf("re-spawn existing runtime: %w", spawnErr)
		}
		s.spawner.SyncOne(existing.ID)
		return existing, nil
	}

	if req.Name == "" {
		req.Name = "My Helm"
	}
	req.Portfolio.Defaults()
	req.Risk.Defaults()

	cfg := &domain.Helm{
		ID:         uuid.New(),
		UserID:     req.UserID,
		AccountID:  req.AccountID,
		Name:       req.Name,
		BrokerType: req.Exchange.BrokerType,
		Portfolio:  req.Portfolio,
		Risk:       req.Risk,
		Enabled:    false,
		Status:     "active",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.repo.Save(cfg); err != nil {
		return nil, fmt.Errorf("save helm: %w", err)
	}
	if err := s.spawner.Spawn(cfg, exchCfg, decimal.Zero); err != nil {
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
	Portfolio *domain.PortfolioConfig
	Risk      *domain.RiskConfig
}

// Update patches an orchestrator config and refreshes live risk parameters.
func (s *Service) Update(id uuid.UUID, req helmDto.UpdateReq) (*domain.Helm, error) {
	var updated *domain.Helm
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		if req.Name != "" {
			o.Name = req.Name
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
