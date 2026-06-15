package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/helm/domain"
)

// RuntimeSpawner is the port for managing runtime lifecycle.
type RuntimeSpawner interface {
	// Spawn creates a runtime for cfg. exchCfg is transient — never stored to DB.
	// Initial capital is always zero; first sync updates it from the exchange.
	Spawn(cfg *domain.Helm, exchCfg domain.ExchangeConfig) error
	Teardown(id uuid.UUID) []string
	Pause(id uuid.UUID) (wasRunning []string, err error)
	Resume(id uuid.UUID) (toRestart []string, err error)
	// ResetHalt clears the risk-manager halt flag on the named runtime.
	ResetHalt(id uuid.UUID) error
	// UpdateRiskConfig refreshes the live risk parameters for the named runtime.
	UpdateRiskConfig(id uuid.UUID, risk domain.RiskConfig) error
	// SyncOne triggers an async portfolio sync for the given orchestrator (fire-and-forget).
	SyncOne(id uuid.UUID)
	// RotateCreds updates credentials in-place and reconnects the WS stream.
	// Running hands are not interrupted.
	RotateCreds(id uuid.UUID, newCreds exchange.Credentials)
	// PurgeHelmData removes all JetStream messages scoped to this helm/account.
	// Called during hard-delete so no audit data lingers after the user removes a broker connection.
	PurgeHelmData(helmID, accountID uuid.UUID)
}

// CredentialFetcher fetches decrypted exchange credentials for a given account.
// Implemented by broker/service.BrokerConnectionService.
type CredentialFetcher interface {
	GetCredentialsByAccountID(ctx context.Context, accountID string) (domain.ExchangeConfig, error)
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
	// DeleteBotsByHelm hard-deletes all Hand rows for a helm from the DB.
	// Called during helm teardown so broker connection delete is a clean sweep.
	DeleteBotsByHelm(helmID uuid.UUID) error
}

// DataPurger removes all audit-trail rows from PostgreSQL for a given helm/account.
// Called during hard-delete so the DB is fully cleaned up alongside JetStream.
type DataPurger interface {
	PurgeByHelm(helmID, accountID uuid.UUID) error
}

// Service handles CRUD for helm configs (helms table).
// Trade decision logic lives in runtime.HelmRuntime.ProcessTrade.
type Service struct {
	repo    domain.HelmRepo
	spawner RuntimeSpawner
	hands   HandLifecycle
	creds   CredentialFetcher
	purger  DataPurger // optional; nil = skip PG audit cleanup
}

// New creates a Service.
func New(repo domain.HelmRepo, spawner RuntimeSpawner) *Service {
	return &Service{repo: repo, spawner: spawner}
}

// SetHandLifecycle injects the HandLifecycle port (breaks init cycle: hand.Service → Registry → Service).
func (s *Service) SetHandLifecycle(bl HandLifecycle) {
	s.hands = bl
}

// SetCredentialFetcher injects the credential-fetching port used by Enable to spawn runtimes.
func (s *Service) SetCredentialFetcher(cf CredentialFetcher) {
	s.creds = cf
}

// SetDataPurger injects the DataPurger used during helm hard-delete.
func (s *Service) SetDataPurger(p DataPurger) {
	s.purger = p
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
