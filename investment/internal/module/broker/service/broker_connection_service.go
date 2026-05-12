package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	accountDomain "mallow/investment/internal/module/account/domain"
	accountRepo "mallow/investment/internal/module/account/repository"
	"mallow/investment/internal/module/broker/client"
	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/dto"
	"mallow/investment/internal/module/broker/repository"
	internalService "mallow/investment/internal/service"
	pkgshared "mallow/pkg/shared"
)

// accountLinkedEvent mirrors natsapi.AccountLinkedEvent in helm.
// Credentials are intentionally excluded — helm fetches them on demand via NATS.
type accountLinkedEvent struct {
	AccountID  string          `json:"account_id"`
	UserID     string          `json:"user_id"`
	Name       string          `json:"name"`
	Capital    decimal.Decimal `json:"capital"`
	BrokerType string          `json:"broker_type"`
	Demo       bool            `json:"demo,omitempty"`
}

// credentialsFetchReq is the request payload for investment.accounts.credentials.
type credentialsFetchReq struct {
	AccountID string `json:"account_id"`
}

// credentialsFetchResp is the NATS reply payload for credential fetch requests.
type credentialsFetchResp struct {
	OK    bool            `json:"ok"`
	Data  *credentialData `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type credentialData struct {
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

// accountUnlinkedEvent mirrors natsapi.AccountUnlinkedEvent.
type accountUnlinkedEvent struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
}

// BrokerRegistry maps broker types to their client implementations.
type BrokerRegistry map[domain.BrokerType]client.BrokerClient

type brokerConnectionService struct {
	repo        repository.BrokerConnectionRepository
	accountRepo accountRepo.Repository
	encrypt     *internalService.EncryptionService
	clients     BrokerRegistry
	nc          *nats.Conn
}

// NewBrokerConnectionService creates a new broker connection service.
func NewBrokerConnectionService(
	repo repository.BrokerConnectionRepository,
	accRepo accountRepo.Repository,
	encrypt *internalService.EncryptionService,
	clients BrokerRegistry,
	nc *nats.Conn,
) BrokerConnectionService {
	return &brokerConnectionService{
		repo:        repo,
		accountRepo: accRepo,
		encrypt:     encrypt,
		clients:     clients,
		nc:          nc,
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
	bc, err := s.getBrokerClient(req.BrokerType)
	if err != nil {
		return nil, err
	}

	creds := client.Credentials{
		APIKey:     req.APIKey,
		APISecret:  req.APISecret,
		IsPaper:    req.IsPaper,
		Passphrase: req.Passphrase,
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
		ID:         uuid.New(),
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

	// Create linked Account for portfolio tracking with initial balance from broker.
	account, err := s.ensureLinkedAccount(ctx, conn, portfolio.CashBalance)
	if err != nil {
		slog.Warn("broker: failed to create linked account (non-fatal)", "broker_connection_id", conn.ID, "err", err)
	}

	// Notify orchestrator to spawn a runtime for this account.
	if account != nil {
		s.publishLinked(account.ID.String(), conn.UserID.String(), conn.BrokerName, string(conn.BrokerType), portfolio.CashBalance, req.IsPaper)
	}

	return conn, nil
}

// ensureLinkedAccount creates an Account row linked to this broker connection
// if one doesn't already exist. Called once on initial broker connection creation.
func (s *brokerConnectionService) ensureLinkedAccount(ctx context.Context, conn *domain.BrokerConnection, initialBalance decimal.Decimal) (*accountDomain.Account, error) {
	accounts, err := s.accountRepo.ListByUserID(ctx, conn.UserID.String(), accountDomain.ListAccountsFilter{})
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
			return &accounts[i], nil // already exists
		}
	}

	accountType, currency := accountTypeForBroker(conn.BrokerType)

	accountName := conn.BrokerName
	if conn.ExternalAccountNumber != nil {
		accountName = fmt.Sprintf("%s - %s", conn.BrokerName, *conn.ExternalAccountNumber)
	}

	account := &accountDomain.Account{
		ID:                 uuid.New(),
		UserID:             conn.UserID,
		AccountName:        accountName,
		AccountType:        accountType,
		Currency:           currency,
		CurrentBalance:     initialBalance,
		IsActive:           true,
		IncludeInNetWorth:  true,
		BrokerConnectionID: &conn.ID,
	}
	if err := s.accountRepo.Create(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *brokerConnectionService) publishLinked(accountID, userID, name, brokerType string, capital decimal.Decimal, demo bool) {
	if s.nc == nil {
		return
	}
	ev := accountLinkedEvent{
		AccountID:  accountID,
		UserID:     userID,
		Name:       name,
		Capital:    capital,
		BrokerType: brokerType,
		Demo:       demo,
	}
	data, _ := json.Marshal(ev)
	if err := s.nc.Publish("helm.accounts.linked", data); err != nil {
		slog.Warn("broker: failed to publish account linked event", "account_id", accountID, "err", err)
	}
}

// SubscribeCredentials subscribes to investment.accounts.credentials so helm can
// fetch decrypted broker credentials on demand (at runtime spawn time).
func (s *brokerConnectionService) SubscribeCredentials(nc *nats.Conn) error {
	_, err := nc.Subscribe("investment.accounts.credentials", s.handleCredentialsFetch)
	return err
}

func (s *brokerConnectionService) handleCredentialsFetch(msg *nats.Msg) {
	var req credentialsFetchReq
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		s.replyCredsErr(msg, "invalid request")
		return
	}
	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		s.replyCredsErr(msg, "invalid account_id")
		return
	}
	ctx := context.Background()
	account, err := s.accountRepo.GetByID(ctx, accountID.String())
	if err != nil {
		s.replyCredsErr(msg, "account not found")
		return
	}
	if account.BrokerConnectionID == nil {
		s.replyCredsErr(msg, "account has no broker connection")
		return
	}
	conn, err := s.repo.GetByID(ctx, *account.BrokerConnectionID)
	if err != nil {
		s.replyCredsErr(msg, "broker connection not found")
		return
	}
	creds, err := s.buildCreds(conn)
	if err != nil {
		s.replyCredsErr(msg, "failed to decrypt credentials")
		return
	}
	passphrase := ""
	if creds.Passphrase != nil {
		passphrase = *creds.Passphrase
	}
	resp, _ := json.Marshal(credentialsFetchResp{OK: true, Data: &credentialData{
		APIKey:     creds.APIKey,
		APISecret:  creds.APISecret,
		Passphrase: passphrase,
	}})
	_ = msg.Respond(resp)
}

func (s *brokerConnectionService) replyCredsErr(msg *nats.Msg, errMsg string) {
	resp, _ := json.Marshal(credentialsFetchResp{OK: false, Error: errMsg})
	_ = msg.Respond(resp)
}

func (s *brokerConnectionService) publishUnlinked(accountID, userID string) {
	if s.nc == nil {
		return
	}
	ev := accountUnlinkedEvent{AccountID: accountID, UserID: userID}
	data, _ := json.Marshal(ev)
	if err := s.nc.Publish("helm.accounts.unlinked", data); err != nil {
		slog.Warn("broker: failed to publish account unlinked event", "account_id", accountID, "err", err)
	}
}

func accountTypeForBroker(bt domain.BrokerType) (accountDomain.AccountType, accountDomain.Currency) {
	switch bt {
	case domain.BrokerTypeOKX, domain.BrokerTypeBybit:
		// OKX UTA and Bybit UTA use a single unified margin pool
		return accountDomain.AccountTypeUnified, accountDomain.CurrencyUSD
	default:
		// Alpaca, Binance and others default to spot (segregated cash account)
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
		return nil, fmt.Errorf("unauthorized access to broker connection")
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

	// Credentials changed → notify orchestrator to respawn (helm fetches new creds via NATS on next spawn).
	if credentialsChanged {
		accounts, _ := s.accountRepo.ListByUserID(ctx, userID.String(), accountDomain.ListAccountsFilter{})
		for i := range accounts {
			if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
				accountID := accounts[i].ID.String()
				s.publishUnlinked(accountID, userID.String())
				s.publishLinked(accountID, userID.String(), conn.BrokerName, string(conn.BrokerType),
					accounts[i].CurrentBalance, conn.IsPaper)
				break
			}
		}
	}

	return conn, nil
}

// ReBroker changes the broker connection linked to an existing account.
// The old runtime is torn down and a new one is spawned with the new broker's credentials.
func (s *brokerConnectionService) ReBroker(ctx context.Context, accountID, newBrokerID, userID uuid.UUID) error {
	// Load and authorize the account.
	account, err := s.accountRepo.GetByID(ctx, accountID.String())
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}
	if account.UserID != userID {
		return fmt.Errorf("unauthorized")
	}

	// Load and authorize the new broker connection.
	newBroker, err := s.GetByID(ctx, newBrokerID, userID)
	if err != nil {
		return fmt.Errorf("broker connection not found: %w", err)
	}

	// Tear down the old runtime if the account was linked to a broker.
	if account.BrokerConnectionID != nil {
		s.publishUnlinked(accountID.String(), userID.String())
	}

	// Update the account's broker link.
	account.BrokerConnectionID = &newBrokerID
	if err := s.accountRepo.Update(ctx, account); err != nil {
		return fmt.Errorf("failed to update account broker link: %w", err)
	}

	// Spawn new runtime (helm fetches fresh credentials via NATS at spawn time).
	s.publishLinked(accountID.String(), userID.String(), newBroker.BrokerName, string(newBroker.BrokerType),
		account.CurrentBalance, newBroker.IsPaper)

	return nil
}

func (s *brokerConnectionService) Delete(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}

	// Find linked account before deleting.
	var linkedAccount *accountDomain.Account
	accounts, _ := s.accountRepo.ListByUserID(ctx, userID.String(), accountDomain.ListAccountsFilter{})
	for i := range accounts {
		if accounts[i].BrokerConnectionID != nil && *accounts[i].BrokerConnectionID == conn.ID {
			linkedAccount = &accounts[i]
			break
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	// Deactivate the linked account — preserve history but mark it offline.
	if linkedAccount != nil {
		linkedAccount.IsActive = false
		linkedAccount.BrokerConnectionID = nil
		if err := s.accountRepo.Update(ctx, linkedAccount); err != nil {
			slog.Warn("broker: failed to deactivate linked account (non-fatal)", "account_id", linkedAccount.ID, "err", err)
		}
		s.publishUnlinked(linkedAccount.ID.String(), userID.String())
	}
	return nil
}

func (s *brokerConnectionService) Activate(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}
	conn.Status = domain.BrokerConnectionStatusActive
	return s.repo.Update(ctx, conn)
}

func (s *brokerConnectionService) Deactivate(ctx context.Context, id, userID uuid.UUID) error {
	conn, err := s.GetByID(ctx, id, userID)
	if err != nil {
		return err
	}
	conn.Status = domain.BrokerConnectionStatusDisconnected
	return s.repo.Update(ctx, conn)
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
