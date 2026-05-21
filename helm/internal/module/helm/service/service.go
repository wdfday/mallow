package service

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/helm/domain"
)

// RuntimeSpawner is the port for managing runtime lifecycle.
type RuntimeSpawner interface {
	// Spawn creates a runtime for cfg. exchCfg and capital are transient — never stored to DB.
	Spawn(cfg *domain.Helm, exchCfg domain.ExchangeConfig, capital decimal.Decimal) error
	Teardown(id uuid.UUID) []string
	Pause(id uuid.UUID) (wasRunning []string, err error)
	Resume(id uuid.UUID) (toRestart []string, err error)
	// ResetHalt clears the risk-manager halt flag on the named runtime.
	ResetHalt(id uuid.UUID) error
	// UpdateRiskConfig refreshes the live risk parameters for the named runtime.
	UpdateRiskConfig(id uuid.UUID, portfolio domain.PortfolioConfig, risk domain.RiskConfig) error
	// SyncOne triggers an async portfolio sync for the given orchestrator (fire-and-forget).
	SyncOne(id uuid.UUID)
}

// HandLifecycle is the port for cascading start/stop/kill to hands.
// Implemented by hand/service.Service.
type HandLifecycle interface {
	StopBots(ids []string)
	StartBots(ids []string)
	KillBots(ids []string)
	// ReleaseBots stops hands without flattening positions (positions become orphaned).
	ReleaseBots(ids []string)
	// PurgeBots removes hands from the in-memory map after their helm is deleted.
	PurgeBots(ids []string)
}

// Service handles CRUD for helm configs (helms table).
// Trade decision logic lives in runtime.HelmRuntime.ProcessTrade.
type Service struct {
	repo    domain.HelmRepo
	spawner RuntimeSpawner
	hands   HandLifecycle
}

// New creates a Service.
func New(repo domain.HelmRepo, spawner RuntimeSpawner) *Service {
	return &Service{repo: repo, spawner: spawner}
}

// SetHandLifecycle injects the HandLifecycle port (breaks init cycle: hand.Service → Registry → Service).
func (s *Service) SetHandLifecycle(bl HandLifecycle) {
	s.hands = bl
}

// Get returns a single orchestrator config.
func (s *Service) Get(id uuid.UUID) (*domain.Helm, error) {
	return s.repo.Get(id)
}

// GetByAccount returns the orchestrator config for an investment account.
func (s *Service) GetByAccount(accountID uuid.UUID) (*domain.Helm, error) {
	return s.repo.GetByAccountID(accountID)
}

// List returns all orchestrator configs (admin use).
func (s *Service) List() ([]*domain.Helm, error) {
	return s.repo.All()
}

// ListByUser returns orchestrators owned by the given user.
func (s *Service) ListByUser(userID uuid.UUID) ([]*domain.Helm, error) {
	return s.repo.AllByUser(userID)
}

// CheckOwner returns an error if the orchestrator does not belong to userID.
func (s *Service) CheckOwner(id, userID uuid.UUID) error {
	cfg, err := s.repo.Get(id)
	if err != nil {
		return err
	}
	if cfg.UserID != userID {
		return fmt.Errorf("helm %q not found", id)
	}
	return nil
}
