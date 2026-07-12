package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"mallow/helm/internal/infra/exchange/client"
	"mallow/helm/internal/infra/natsapi"
	accountDomain "mallow/helm/internal/module/account/domain"
	accountRepo "mallow/helm/internal/module/account/repository"
	"mallow/helm/internal/module/broker/domain"
	"mallow/helm/internal/module/broker/dto"
	"mallow/helm/internal/module/broker/repository"
	internalService "mallow/helm/internal/service"
	pkgshared "mallow/pkg/shared"
)

// BrokerRegistry maps broker types to their client implementations.
type BrokerRegistry map[domain.BrokerType]client.BrokerClient

type brokerConnectionService struct {
	repo        repository.BrokerConnectionRepository
	accountRepo accountRepo.Repository
	encrypt     *internalService.EncryptionService
	clients     BrokerRegistry
	helmCreator HelmCreator
}

// NewBrokerConnectionService creates a new broker connection service.
func NewBrokerConnectionService(
	repo repository.BrokerConnectionRepository,
	accRepo accountRepo.Repository,
	encrypt *internalService.EncryptionService,
	clients BrokerRegistry,
	helmCreator HelmCreator,
) BrokerConnectionService {
	return &brokerConnectionService{
		repo:        repo,
		accountRepo: accRepo,
		encrypt:     encrypt,
		clients:     clients,
		helmCreator: helmCreator,
	}
}

func (s *brokerConnectionService) getBrokerClient(t domain.BrokerType) (client.BrokerClient, error) {
	c, ok := s.clients[t]
	if !ok {
		return nil, fmt.Errorf("unsupported broker type: %s", t)
	}
	return c, nil
}

func (s *brokerConnectionService) Create(ctx context.Context, req *dto.CreateBrokerConnectionServiceRequest) (*domain.BrokerConnection, error) {
	if !req.IsPaper {
		return nil, pkgshared.NewAppError("LIVE_TRADING_DISABLED", "Live trading is not available yet. Please use paper trading.", 403)
	}

	bc, err := s.getBrokerClient(req.BrokerType)
	if err != nil {
		return nil, err
	}

	creds := client.Credentials{
		APIKey:      req.APIKey,
		APISecret:   req.APISecret,
		IsPaper:     req.IsPaper,
		Passphrase:  req.Passphrase,
		AccountType: req.AccountType,
	}

	// Validate credentials then fetch initial portfolio balance.
	if err := bc.Validate(ctx, creds); err != nil {
		return nil, mapBrokerError("failed to authenticate with broker", err)
	}

	portfolio, err := bc.GetPortfolio(ctx, creds)
	if err != nil {
		return nil, mapBrokerError("failed to fetch initial portfolio", err)
	}

	encAPIKey, err := s.encrypt.Encrypt(req.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt API key: %w", err)
	}
	encAPISecret, err := s.encrypt.Encrypt(req.APISecret)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt API secret: %w", err)
	}

	conn := &domain.BrokerConnection{
		ID:         uuid.Must(uuid.NewV7()),
		UserID:     req.UserID,
		BrokerType: req.BrokerType,
		BrokerName: req.BrokerName,
		Status:     domain.BrokerConnectionStatusActive,
		IsPaper:    req.IsPaper,
		APIKey:     encAPIKey,
		APISecret:  encAPISecret,
		Notes:      req.Notes,
	}

	if req.Passphrase != nil {
		enc, err := s.encrypt.Encrypt(*req.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt passphrase: %w", err)
		}
		conn.Passphrase = &enc
	}

	if err := s.repo.Create(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to save broker connection: %w", err)
	}

	// Detect sub-accounts. Brokers that implement MultiAccountDetector (e.g. Binance)
	// may return multiple account types from one API key. Others fall back to a single
	// account derived from GetPortfolio.
	type subAccount struct {
		accountType string
		cash        decimal.Decimal
	}
	var subAccounts []subAccount

	if detector, ok := bc.(client.MultiAccountDetector); ok {
		detected, detErr := detector.DetectAccounts(ctx, creds)
		if detErr != nil {
			slog.Warn("broker: DetectAccounts failed, falling back to single account", "err", detErr)
		} else {
			for _, d := range detected {
				subAccounts = append(subAccounts, subAccount{accountType: d.AccountType, cash: d.CashBalance})
			}
		}
	}
	if len(subAccounts) == 0 {
		subAccounts = []subAccount{{accountType: req.AccountType, cash: portfolio.CashBalance}}
	}

	pp := ""
	if creds.Passphrase != nil {
		pp = *creds.Passphrase
	}
	for _, sub := range subAccounts {
		s.ensureSubAccount(ctx, conn, sub.accountType, creds.APIKey, creds.APISecret, pp)
	}

	return conn, nil
}

// ensureSubAccount creates the Account + Helm for one sub-account type if they
// don't already exist. Called both from Create and SyncAllAccounts.
// All errors are logged and treated as non-fatal.
func (s *brokerConnectionService) ensureSubAccount(ctx context.Context, conn *domain.BrokerConnection, accountType, apiKey, apiSecret, passphrase string) {
	account, err := s.ensureLinkedAccount(ctx, conn, accountType)
	if err != nil {
		slog.Warn("broker: failed to create linked account (non-fatal)",
			"broker_connection_id", conn.ID, "account_type", accountType, "err", err)
		return
	}

	// Binance USDM futures sub-accounts are served by the fbinance exchange adapter
	// (separate FAPI client). Remap the broker type so the Helm routes correctly.
	helmBrokerType := string(conn.BrokerType)
	if conn.BrokerType == domain.BrokerTypeBinance && accountType == "futures_usdm" {
		helmBrokerType = string(domain.BrokerTypeFBinance)
	}
	if err := s.helmCreator.AutoCreateForAccount(context.Background(), HelmAutoCreateReq{
		UserID:      conn.UserID,
		AccountID:   account.ID,
		AccountName: account.AccountName,
		BrokerType:  helmBrokerType,
		AccountType: string(account.AccountType),
		APIKey:      apiKey,
		APISecret:   apiSecret,
		Passphrase:  passphrase,
		Paper:       conn.IsPaper,
	}); err != nil {
		slog.Warn("broker: auto helm create failed (non-fatal)", "account_id", account.ID, "err", err)
	}
}

// SyncAllAccounts iterates every active broker connection and ensures all
// detected sub-accounts have a matching Account + Helm row. Idempotent — safe
// to call on every startup.
func (s *brokerConnectionService) SyncAllAccounts(ctx context.Context) error {
	conns, err := s.repo.GetAllActive(ctx)
	if err != nil {
		return fmt.Errorf("broker sync: list active connections: %w", err)
	}
	synced := 0
	for _, conn := range conns {
		bc, err := s.getBrokerClient(conn.BrokerType)
		if err != nil {
			slog.Debug("broker sync: skipping unsupported type", "broker_type", conn.BrokerType)
			continue
		}
		detector, ok := bc.(client.MultiAccountDetector)
		if !ok {
			continue // single-account exchange, nothing to discover
		}
		creds, err := s.buildCreds(conn)
		if err != nil {
			slog.Warn("broker sync: decrypt creds failed", "id", conn.ID, "err", err)
			continue
		}
		detected, err := detector.DetectAccounts(ctx, creds)
		if err != nil {
			slog.Warn("broker sync: DetectAccounts failed", "id", conn.ID, "err", err)
			continue
		}
		pp := ""
		if creds.Passphrase != nil {
			pp = *creds.Passphrase
		}
		for _, d := range detected {
			s.ensureSubAccount(ctx, conn, d.AccountType, creds.APIKey, creds.APISecret, pp)
		}
		synced++
	}
	slog.Info("broker sync: startup account sync complete",
		"total_connections", len(conns), "multi_account_synced", synced)
	return nil
}

// ensureLinkedAccount creates an Account row linked to this broker connection
// if one doesn't already exist.
func (s *brokerConnectionService) ensureLinkedAccount(ctx context.Context, conn *domain.BrokerConnection, accountTypeOverride string) (*accountDomain.Account, error) {
	accounts, err := s.accountRepo.ListByUserID(ctx, conn.UserID.String(), accountDomain.ListAccountsFilter{})
	if err != nil {
		return nil, err
	}
	accountType, currency := accountTypeForBroker(conn.BrokerType)
	if accountTypeOverride != "" {
		accountType = accountDomain.AccountType(accountTypeOverride)
	}

	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil &&
			*accounts[i].BrokerConnectionID == conn.ID &&
			accounts[i].AccountType == accountType {
			return &accounts[i], nil // already exists for this sub-account type
		}
	}

	accountName := fmt.Sprintf("%s %s", conn.BrokerName, string(accountType))

	account := &accountDomain.Account{
		ID:                 uuid.Must(uuid.NewV7()),
		UserID:             conn.UserID,
		AccountName:        accountName,
		AccountType:        accountType,
		Currency:           currency,
		IsActive:           true,
		IncludeInNetWorth:  true,
		BrokerConnectionID: &conn.ID,
	}
	if err := s.accountRepo.Create(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

// GetCredentialsByAccountID looks up the broker connection for accountID, decrypts the
// credentials, and returns a CredentialsFetchResp ready for spawning a helm runtime.
// This replaces the old investment.accounts.credentials NATS round-trip.
func (s *brokerConnectionService) GetCredentialsByAccountID(ctx context.Context, accountID string) (natsapi.CredentialsFetchResp, error) {
	id, err := uuid.Parse(accountID)
	if err != nil {
		return natsapi.CredentialsFetchResp{}, fmt.Errorf("invalid account_id: %w", err)
	}
	account, err := s.accountRepo.GetByID(ctx, id.String())
	if err != nil {
		return natsapi.CredentialsFetchResp{}, fmt.Errorf("account not found: %w", err)
	}
	if account.BrokerConnectionID == nil {
		return natsapi.CredentialsFetchResp{}, fmt.Errorf("account %s has no broker connection", accountID)
	}
	conn, err := s.repo.GetByID(ctx, *account.BrokerConnectionID)
	if err != nil {
		return natsapi.CredentialsFetchResp{}, fmt.Errorf("broker connection not found: %w", err)
	}
	creds, err := s.buildCreds(conn)
	if err != nil {
		return natsapi.CredentialsFetchResp{}, fmt.Errorf("failed to decrypt credentials: %w", err)
	}
	passphrase := ""
	if creds.Passphrase != nil {
		passphrase = *creds.Passphrase
	}
	return natsapi.CredentialsFetchResp{
		APIKey:      creds.APIKey,
		APISecret:   creds.APISecret,
		Passphrase:  passphrase,
		BrokerType:  string(conn.BrokerType),
		AccountType: string(account.AccountType),
		Paper:       conn.IsPaper,
	}, nil
}

func accountTypeForBroker(bt domain.BrokerType) (accountDomain.AccountType, accountDomain.Currency) {
	switch bt {
	case domain.BrokerTypeOKX, domain.BrokerTypeBybit:
		return accountDomain.AccountTypeUnified, accountDomain.CurrencyUSD
	default:
		return accountDomain.AccountTypeSpot, accountDomain.CurrencyUSD
	}
}

func (s *brokerConnectionService) GetByID(ctx context.Context, id, userID uuid.UUID) (*domain.BrokerConnection, error) {
	conn, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("broker connection not found")
		}
		return nil, err
	}
	if conn.UserID != userID {
		return nil, fmt.Errorf("broker connection not found")
	}
	return conn, nil
}

func (s *brokerConnectionService) List(ctx context.Context, userID uuid.UUID, filters *ListFilters) ([]*domain.BrokerConnection, error) {
	if filters == nil {
		filters = &ListFilters{}
	}
	all, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var result []*domain.BrokerConnection
	for _, c := range all {
		if s.matchesFilters(c, filters) {
			result = append(result, c)
		}
	}
	return result, nil
}

func (s *brokerConnectionService) Update(ctx context.Context, id, userID uuid.UUID, req *UpdateBrokerConnectionRequest) (*domain.BrokerConnection, error) {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return nil, err
	}

	credentialsChanged := req.APIKey != nil || req.APISecret != nil || req.Passphrase != nil

	if req.BrokerName != nil {
		conn.BrokerName = *req.BrokerName
	}
	if req.Notes != nil {
		conn.Notes = req.Notes
	}
	if req.APIKey != nil {
		enc, err := s.encrypt.Encrypt(*req.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt API key: %w", err)
		}
		conn.APIKey = enc
	}
	if req.APISecret != nil {
		enc, err := s.encrypt.Encrypt(*req.APISecret)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt API secret: %w", err)
		}
		conn.APISecret = enc
	}
	if req.Passphrase != nil {
		enc, err := s.encrypt.Encrypt(*req.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt passphrase: %w", err)
		}
		conn.Passphrase = &enc
	}

	if err := s.repo.Update(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to update broker connection: %w", err)
	}

	// Credentials changed → respawn the linked helm runtime with updated credentials.
	if credentialsChanged {
		accounts, _ := s.accountRepo.ListByUserID(ctx, conn.UserID.String(), accountDomain.ListAccountsFilter{})
		creds, err := s.buildCreds(conn)
		if err != nil {
			slog.Warn("broker: credentials update respawn skipped — decrypt failed", "conn_id", conn.ID, "err", err)
		} else {
			pp := ""
			if creds.Passphrase != nil {
				pp = *creds.Passphrase
			}
			for i := range accounts {
				if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
					if err := s.helmCreator.AutoCreateForAccount(ctx, HelmAutoCreateReq{
						UserID:      conn.UserID,
						AccountID:   accounts[i].ID,
						AccountName: accounts[i].AccountName,
						BrokerType:  string(conn.BrokerType),
						AccountType: string(accounts[i].AccountType),
						APIKey:      creds.APIKey,
						APISecret:   creds.APISecret,
						Passphrase:  pp,
						Paper:       conn.IsPaper,
					}); err != nil {
						slog.Warn("broker: helm respawn after credentials update failed", "account_id", accounts[i].ID, "err", err)
					}
				}
			}
		}
	}

	return conn, nil
}

// RotateKey validates new credentials against the broker, replaces the stored
// key/secret/passphrase, sets status to active, and respawns the helm runtime.
func (s *brokerConnectionService) RotateKey(ctx context.Context, id, userID uuid.UUID, req *RotateKeyRequest) (*domain.BrokerConnection, error) {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return nil, err
	}

	bc, err := s.getBrokerClient(conn.BrokerType)
	if err != nil {
		return nil, err
	}

	newCreds := client.Credentials{
		APIKey:     req.APIKey,
		APISecret:  req.APISecret,
		Passphrase: req.Passphrase,
		IsPaper:    conn.IsPaper,
	}

	// Validate before touching the DB — fail fast if the new key is bad.
	// KNOWN GAP: this only checks the new key works, not that it's the same
	// underlying exchange account as the old one — see "Known gap" under Rotate
	// Key in BUSINESS.md. Rotate reuses this connection's Account/Helm/hand
	// history unconditionally.
	if err := bc.Validate(ctx, newCreds); err != nil {
		return nil, mapBrokerError("new credentials rejected by broker", err)
	}

	encKey, err := s.encrypt.Encrypt(req.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt API key: %w", err)
	}
	encSecret, err := s.encrypt.Encrypt(req.APISecret)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt API secret: %w", err)
	}
	conn.APIKey = encKey
	conn.APISecret = encSecret

	if req.Passphrase != nil {
		enc, err := s.encrypt.Encrypt(*req.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt passphrase: %w", err)
		}
		conn.Passphrase = &enc
	} else {
		conn.Passphrase = nil
	}

	conn.Status = domain.BrokerConnectionStatusActive

	if err := s.repo.Update(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to save rotated credentials: %w", err)
	}

	// Update in-flight credentials and reconnect WS stream without interrupting hands.
	accounts, _ := s.accountRepo.ListByUserID(ctx, conn.UserID.String(), accountDomain.ListAccountsFilter{})
	pp := ""
	if req.Passphrase != nil {
		pp = *req.Passphrase
	}
	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
			rotateReq := HelmAutoCreateReq{
				UserID:      conn.UserID,
				AccountID:   accounts[i].ID,
				AccountName: accounts[i].AccountName,
				BrokerType:  string(conn.BrokerType),
				AccountType: string(accounts[i].AccountType),
				APIKey:      req.APIKey,
				APISecret:   req.APISecret,
				Passphrase:  pp,
				Paper:       conn.IsPaper,
			}
			if err := s.helmCreator.RotateCredsForAccount(ctx, accounts[i].ID, rotateReq); err != nil {
				slog.Warn("broker: helm creds rotate failed (non-fatal)", "account_id", accounts[i].ID, "err", err)
			}
			// Auto-resume if the helm was halted by a credential error — new key is valid.
			// No-op if helm is in any other state (respects user-initiated pauses).
			if err := s.helmCreator.ResumeErrorForAccount(ctx, accounts[i].ID); err != nil {
				slog.Warn("broker: helm auto-resume after rotate-key failed (non-fatal)", "account_id", accounts[i].ID, "err", err)
			}
		}
	}

	return conn, nil
}

func (s *brokerConnectionService) Delete(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}

	// Collect ALL accounts linked to this connection (Binance can have spot + futures_usdm).
	var linkedAccounts []accountDomain.Account
	accounts, _ := s.accountRepo.ListByUserID(ctx, userID.String(), accountDomain.ListAccountsFilter{})
	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
			linkedAccounts = append(linkedAccounts, accounts[i])
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	for i := range linkedAccounts {
		acc := &linkedAccounts[i]
		// Full sweep: runtime teardown → hand rows → helm row → JetStream subjects.
		if err := s.helmCreator.AutoDeleteForAccount(ctx, acc.ID); err != nil {
			slog.Warn("broker: helm teardown failed (non-fatal)", "account_id", acc.ID, "err", err)
		}
		// Hard-delete the account row — user no longer wants any data.
		if err := s.accountRepo.HardDelete(ctx, acc.ID.String()); err != nil {
			slog.Warn("broker: failed to hard-delete linked account (non-fatal)", "account_id", acc.ID, "err", err)
		}
	}
	return nil
}

func (s *brokerConnectionService) Activate(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}
	// error means the broker already rejected these credentials 3x — flipping the
	// status back to active here would resume trading without ever re-checking them.
	// rotate-key is the only path that revalidates against the exchange, so route
	// recovery through it instead of trusting a bare activate call.
	if conn.Status == domain.BrokerConnectionStatusError {
		return pkgshared.NewAppError(
			"BROKER_CONNECTION_CREDENTIAL_ERROR",
			"This broker connection has a credential error and cannot be reactivated directly — rotate the API key via /rotate-key first",
			http.StatusConflict,
		)
	}
	conn.Status = domain.BrokerConnectionStatusActive
	return s.repo.Update(ctx, conn)
}

func (s *brokerConnectionService) Deactivate(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}
	if conn.Status == domain.BrokerConnectionStatusDisconnected {
		return pkgshared.NewAppError(
			"BROKER_CONNECTION_ALREADY_INACTIVE",
			"This broker connection is already deactivated",
			http.StatusConflict,
		)
	}
	conn.Status = domain.BrokerConnectionStatusDisconnected
	if err := s.repo.Update(ctx, conn); err != nil {
		return err
	}
	// Pause the live helm so it stops accepting signals while the connection is inactive.
	// If the helm was already halted by a credential error, leave it as-is: pausing here
	// would overwrite HelmStatusError with HelmStatusPaused and erase the error signal
	// the UI relies on to prompt a rotate-key.
	accounts, _ := s.accountRepo.ListByUserID(ctx, conn.UserID.String(), accountDomain.ListAccountsFilter{})
	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
			if err := s.helmCreator.PauseForAccount(ctx, accounts[i].ID); err != nil {
				slog.Warn("broker: deactivate — helm pause failed (non-fatal)", "account_id", accounts[i].ID, "err", err)
			}
		}
	}
	return nil
}

// MarkCredentialError marks the broker connection linked to accountID as error status.
// Internal use only — called by the runtime credential-error hook (no ownership check).
func (s *brokerConnectionService) MarkCredentialError(ctx context.Context, accountID uuid.UUID, reason string) error {
	account, err := s.accountRepo.GetByID(ctx, accountID.String())
	if err != nil {
		return fmt.Errorf("credential error: account not found: %w", err)
	}
	if account.BrokerConnectionID == nil {
		return fmt.Errorf("credential error: account %s has no broker connection", accountID)
	}
	conn, err := s.repo.GetByID(ctx, *account.BrokerConnectionID)
	if err != nil {
		return fmt.Errorf("credential error: broker connection not found: %w", err)
	}
	conn.Status = domain.BrokerConnectionStatusError
	if updateErr := s.repo.Update(ctx, conn); updateErr != nil {
		return fmt.Errorf("credential error: failed to persist error status: %w", updateErr)
	}
	slog.Warn("broker: credential error detected — connection marked error",
		"broker_connection_id", conn.ID, "account_id", accountID, "reason", reason)
	// Also mark the helm as error so the UI surfaces the state clearly.
	if err := s.helmCreator.MarkErrorForAccount(ctx, accountID); err != nil {
		slog.Warn("broker: credential error — helm status update failed (non-fatal)", "account_id", accountID, "err", err)
	}
	return nil
}

func (s *brokerConnectionService) TestConnection(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}
	bc, err := s.getBrokerClient(conn.BrokerType)
	if err != nil {
		return err
	}
	creds, err := s.buildCreds(conn)
	if err != nil {
		return err
	}
	if err := bc.Validate(ctx, creds); err != nil {
		return mapBrokerError("broker connection test failed", err)
	}
	return nil
}

// buildCreds decrypts the stored credentials for an existing connection.
func (s *brokerConnectionService) buildCreds(conn *domain.BrokerConnection) (client.Credentials, error) {
	apiKey, err := s.encrypt.Decrypt(conn.APIKey)
	if err != nil {
		return client.Credentials{}, fmt.Errorf("failed to decrypt API key: %w", err)
	}
	apiSecret, err := s.encrypt.Decrypt(conn.APISecret)
	if err != nil {
		return client.Credentials{}, fmt.Errorf("failed to decrypt API secret: %w", err)
	}
	creds := client.Credentials{
		APIKey:    apiKey,
		APISecret: apiSecret,
		IsPaper:   conn.IsPaper,
	}
	if conn.Passphrase != nil {
		pp, err := s.encrypt.Decrypt(*conn.Passphrase)
		if err != nil {
			return client.Credentials{}, fmt.Errorf("failed to decrypt passphrase: %w", err)
		}
		creds.Passphrase = &pp
	}
	return creds, nil
}

func (s *brokerConnectionService) matchesFilters(conn *domain.BrokerConnection, f *ListFilters) bool {
	if f.BrokerType != nil && conn.BrokerType != *f.BrokerType {
		return false
	}
	if f.Status != nil && conn.Status != *f.Status {
		return false
	}
	if f.ActiveOnly && conn.Status != domain.BrokerConnectionStatusActive {
		return false
	}
	return true
}

func mapBrokerError(message string, err error) error {
	if err == nil {
		return nil
	}

	wrapped := fmt.Errorf("%s: %w", message, err)
	detail := err.Error()
	lower := strings.ToLower(detail)

	switch {
	case strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "not authorized"),
		strings.Contains(lower, "invalid api key"),
		strings.Contains(lower, "invalid signature"),
		strings.Contains(lower, "invalid credentials"),
		strings.Contains(lower, "invalid passphrase"),
		strings.Contains(lower, "http 401"):
		return pkgshared.NewAppError("BROKER_AUTH_FAILED", "Broker credentials are invalid", 422).
			WithDetails("message", message).
			WithDetails("broker_error", detail).
			WithError(wrapped)
	case strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "http 403"):
		return pkgshared.NewAppError("BROKER_ACCESS_DENIED", "Broker access denied", 422).
			WithDetails("message", message).
			WithDetails("broker_error", detail).
			WithError(wrapped)
	case strings.Contains(lower, "bad request"),
		strings.Contains(lower, "invalid parameter"),
		strings.Contains(lower, "validation"),
		strings.Contains(lower, "http 400"),
		strings.Contains(lower, "http 422"),
		strings.Contains(lower, "unprocessable"):
		return pkgshared.ErrBadRequest.
			WithDetails("message", message).
			WithDetails("broker_error", detail).
			WithError(wrapped)
	default:
		return wrapped
	}
}
