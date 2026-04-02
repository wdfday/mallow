package service

import (
	"fmt"

	"github.com/google/uuid"

	"orchestrator/internal/module/orchesrator/domain"
)

// RuntimeSpawner is the port for managing runtime lifecycle.
type RuntimeSpawner interface {
	Spawn(cfg *domain.OrchestratorConfig) error
	Teardown(id uuid.UUID) []string
	Pause(id uuid.UUID) (wasRunning []string, err error)
	Resume(id uuid.UUID) (toRestart []string, err error)
	// SyncOne triggers an async portfolio sync for the given orchestrator (fire-and-forget).
	SyncOne(id uuid.UUID)
}

// BotLifecycle is the port for cascading start/stop to bots.
// Implemented by worker/service.Service.
type BotLifecycle interface {
	StopBots(ids []string)
	StartBots(ids []string)
}

// Service handles CRUD for orchestrator configs (orchestrators table).
// Trade decision logic lives in runtime.OrchestratorRuntime.ProcessTrade.
type Service struct {
	repo    domain.OrchestratorRepo
	spawner RuntimeSpawner
	bots    BotLifecycle
}

// New creates a Service.
func New(repo domain.OrchestratorRepo, spawner RuntimeSpawner) *Service {
	return &Service{repo: repo, spawner: spawner}
}

// SetBotLifecycle injects the BotLifecycle port (breaks init cycle: worker.Service → Registry → Service).
func (s *Service) SetBotLifecycle(bl BotLifecycle) {
	s.bots = bl
}

// Get returns a single orchestrator config.
func (s *Service) Get(id uuid.UUID) (*domain.OrchestratorConfig, error) {
	return s.repo.Get(id)
}

// GetByAccount returns the orchestrator config for an investment account.
func (s *Service) GetByAccount(accountID uuid.UUID) (*domain.OrchestratorConfig, error) {
	return s.repo.GetByAccountID(accountID)
}

// List returns all orchestrator configs (admin use).
func (s *Service) List() ([]*domain.OrchestratorConfig, error) {
	return s.repo.All()
}

// ListByUser returns orchestrators owned by the given user.
func (s *Service) ListByUser(userID uuid.UUID) ([]*domain.OrchestratorConfig, error) {
	return s.repo.AllByUser(userID)
}

// CheckOwner returns an error if the orchestrator does not belong to userID.
func (s *Service) CheckOwner(id, userID uuid.UUID) error {
	cfg, err := s.repo.Get(id)
	if err != nil {
		return err
	}
	if cfg.UserID != userID {
		return fmt.Errorf("orchestrator %q not found", id)
	}
	return nil
}
