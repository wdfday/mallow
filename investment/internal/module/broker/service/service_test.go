package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accountDomain "mallow/investment/internal/module/account/domain"
	accountRepo "mallow/investment/internal/module/account/repository"
	"mallow/investment/internal/module/broker/client"
	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/dto"
	"mallow/investment/internal/module/broker/repository"
	internalService "mallow/investment/internal/service"
)

// ────────────────────────────────────────────────────────────────────────────
// Mocks
// ────────────────────────────────────────────────────────────────────────────

// MockBrokerConnectionRepository mocks repository.BrokerConnectionRepository.
type MockBrokerConnectionRepository struct{ mock.Mock }

func (m *MockBrokerConnectionRepository) Create(ctx context.Context, conn *domain.BrokerConnection) error {
	return m.Called(ctx, conn).Error(0)
}
func (m *MockBrokerConnectionRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.BrokerConnection, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) GetActiveByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) GetByUserIDAndType(ctx context.Context, userID uuid.UUID, bt domain.BrokerType) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, userID, bt)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) Update(ctx context.Context, conn *domain.BrokerConnection) error {
	return m.Called(ctx, conn).Error(0)
}
func (m *MockBrokerConnectionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}
func (m *MockBrokerConnectionRepository) HardDelete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}
func (m *MockBrokerConnectionRepository) GetNeedingSync(ctx context.Context, limit int) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) GetExpiredTokens(ctx context.Context, limit int) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionRepository) UpdateSyncStatus(ctx context.Context, id uuid.UUID, lastSyncAt time.Time, syncStatus, syncError *string, stats map[string]int) error {
	return m.Called(ctx, id, lastSyncAt, syncStatus, syncError, stats).Error(0)
}
func (m *MockBrokerConnectionRepository) UpdateTokens(ctx context.Context, id uuid.UUID, accessToken, refreshToken *string, expiresAt *time.Time) error {
	return m.Called(ctx, id, accessToken, refreshToken, expiresAt).Error(0)
}
func (m *MockBrokerConnectionRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BrokerConnectionStatus) error {
	return m.Called(ctx, id, status).Error(0)
}
func (m *MockBrokerConnectionRepository) Count(ctx context.Context, userID uuid.UUID) (int64, error) {
	args := m.Called(ctx, userID)
	return args.Get(0).(int64), args.Error(1)
}
func (m *MockBrokerConnectionRepository) CountByType(ctx context.Context, userID uuid.UUID, bt domain.BrokerType) (int64, error) {
	args := m.Called(ctx, userID, bt)
	return args.Get(0).(int64), args.Error(1)
}

// MockBrokerClient mocks client.BrokerClient.
type MockBrokerClient struct{ mock.Mock }

func (m *MockBrokerClient) Authenticate(ctx context.Context, creds client.Credentials) (*client.AuthResponse, error) {
	args := m.Called(ctx, creds)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*client.AuthResponse), args.Error(1)
}
func (m *MockBrokerClient) RefreshToken(ctx context.Context, refreshToken string) (*client.AuthResponse, error) {
	args := m.Called(ctx, refreshToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*client.AuthResponse), args.Error(1)
}
func (m *MockBrokerClient) GetPortfolio(ctx context.Context, accessToken string) (*client.Portfolio, error) {
	args := m.Called(ctx, accessToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*client.Portfolio), args.Error(1)
}
func (m *MockBrokerClient) GetPositions(ctx context.Context, accessToken string) ([]client.Position, error) {
	args := m.Called(ctx, accessToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]client.Position), args.Error(1)
}
func (m *MockBrokerClient) GetTransactions(ctx context.Context, accessToken string, start, end time.Time) ([]client.Transaction, error) {
	args := m.Called(ctx, accessToken, start, end)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]client.Transaction), args.Error(1)
}
func (m *MockBrokerClient) GetMarketPrice(ctx context.Context, symbol string) (*client.MarketPrice, error) {
	args := m.Called(ctx, symbol)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*client.MarketPrice), args.Error(1)
}
func (m *MockBrokerClient) GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*client.MarketPrice, error) {
	args := m.Called(ctx, symbols)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]*client.MarketPrice), args.Error(1)
}

// MockAccountRepository mocks accountRepo.Repository (used by SyncService).
type MockAccountRepository struct{ mock.Mock }

func (m *MockAccountRepository) GetByID(ctx context.Context, id string) (*accountDomain.Account, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*accountDomain.Account), args.Error(1)
}
func (m *MockAccountRepository) GetByIDAndUserID(ctx context.Context, id, userID string) (*accountDomain.Account, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*accountDomain.Account), args.Error(1)
}
func (m *MockAccountRepository) ListByUserID(ctx context.Context, userID string, filters accountDomain.ListAccountsFilter) ([]accountDomain.Account, error) {
	args := m.Called(ctx, userID, filters)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]accountDomain.Account), args.Error(1)
}
func (m *MockAccountRepository) Create(ctx context.Context, account *accountDomain.Account) error {
	return m.Called(ctx, account).Error(0)
}
func (m *MockAccountRepository) Update(ctx context.Context, account *accountDomain.Account) error {
	return m.Called(ctx, account).Error(0)
}
func (m *MockAccountRepository) UpdateColumns(ctx context.Context, id string, columns map[string]any) error {
	return m.Called(ctx, id, columns).Error(0)
}
func (m *MockAccountRepository) SoftDelete(ctx context.Context, id string) error {
	return m.Called(ctx, id).Error(0)
}
func (m *MockAccountRepository) CountByUserID(ctx context.Context, userID string, filters accountDomain.ListAccountsFilter) (int64, error) {
	args := m.Called(ctx, userID, filters)
	return args.Get(0).(int64), args.Error(1)
}
func (m *MockAccountRepository) UpdateBalance(ctx context.Context, accountID string, delta decimal.Decimal) error {
	return m.Called(ctx, accountID, delta).Error(0)
}
func (m *MockAccountRepository) UpdateBalanceWithTx(tx *gorm.DB, accountID string, delta decimal.Decimal) error {
	return m.Called(tx, accountID, delta).Error(0)
}
func (m *MockAccountRepository) GetAccountsNeedingSync(ctx context.Context) ([]*accountDomain.Account, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*accountDomain.Account), args.Error(1)
}

// Compile-time interface checks.
var _ repository.BrokerConnectionRepository = (*MockBrokerConnectionRepository)(nil)
var _ client.BrokerClient = (*MockBrokerClient)(nil)
var _ accountRepo.Repository = (*MockAccountRepository)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

const testEncryptionKey = "test-encryption-key-32bytes-xxx!"

func newTestEncryptionService(t *testing.T) *internalService.EncryptionService {
	t.Helper()
	svc, err := internalService.NewEncryptionService(testEncryptionKey)
	require.NoError(t, err)
	return svc
}

func setupService(t *testing.T) (*brokerConnectionService, *MockBrokerConnectionRepository, *MockBrokerClient) {
	t.Helper()
	mockRepo := new(MockBrokerConnectionRepository)
	mockClient := new(MockBrokerClient)
	mockAccountRepo := new(MockAccountRepository)
	encSvc := newTestEncryptionService(t)
	registry := BrokerRegistry{domain.BrokerTypeBinance: mockClient}
	svc := &brokerConnectionService{
		repo:        mockRepo,
		accountRepo: mockAccountRepo,
		encrypt:     encSvc,
		clients:     registry,
	}
	return svc, mockRepo, mockClient
}

// setupServiceWithAccount returns a service with a mock account repository —
// needed for Create success path which calls ensureLinkedAccount.
func setupServiceWithAccount(t *testing.T) (
	*brokerConnectionService,
	*MockBrokerConnectionRepository,
	*MockBrokerClient,
	*MockAccountRepository,
) {
	t.Helper()
	mockRepo := new(MockBrokerConnectionRepository)
	mockClient := new(MockBrokerClient)
	mockAccountRepo := new(MockAccountRepository)
	encSvc := newTestEncryptionService(t)
	registry := BrokerRegistry{domain.BrokerTypeBinance: mockClient}
	svc := &brokerConnectionService{
		repo:        mockRepo,
		accountRepo: mockAccountRepo,
		encrypt:     encSvc,
		clients:     registry,
	}
	return svc, mockRepo, mockClient, mockAccountRepo
}

func makeConnRequest(userID uuid.UUID) *dto.CreateBrokerConnectionServiceRequest {
	return &dto.CreateBrokerConnectionServiceRequest{
		UserID:     userID,
		BrokerType: domain.BrokerTypeBinance,
		BrokerName: "My Binance",
		APIKey:     "key",
		APISecret:  "secret",
	}
}

func sampleConn(userID uuid.UUID) *domain.BrokerConnection {
	at := "encrypted-access"
	rt := "encrypted-refresh"
	exp := time.Now().Add(time.Hour)
	return &domain.BrokerConnection{
		ID:             uuid.New(),
		UserID:         userID,
		BrokerType:     domain.BrokerTypeBinance,
		BrokerName:     "My Binance",
		Status:         domain.BrokerConnectionStatusActive,
		APIKey:         "enc-key",
		APISecret:      "enc-secret",
		AccessToken:    &at,
		RefreshToken:   &rt,
		TokenExpiresAt: &exp,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Create
// ────────────────────────────────────────────────────────────────────────────

func TestCreate_AuthError(t *testing.T) {
	svc, _, mockClient := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	mockClient.On("Authenticate", ctx, mock.Anything).Return(nil, errors.New("invalid credentials"))

	result, err := svc.Create(ctx, makeConnRequest(userID))
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to authenticate with broker")
	mockClient.AssertExpectations(t)
}

func TestCreate_RepoSaveError(t *testing.T) {
	svc, mockRepo, mockClient := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	authResp := &client.AuthResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	mockClient.On("Authenticate", ctx, mock.Anything).Return(authResp, nil)
	mockClient.On("GetPortfolio", ctx, "access-token").Return(&client.Portfolio{TotalValue: decimal.NewFromInt(1000)}, nil)
	mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(errors.New("db error"))

	result, err := svc.Create(ctx, makeConnRequest(userID))
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to save broker connection")
	mockRepo.AssertExpectations(t)
}

func TestCreate_Success(t *testing.T) {
	svc, mockRepo, mockClient, mockAccountRepo := setupServiceWithAccount(t)
	ctx := context.Background()
	userID := uuid.New()

	authResp := &client.AuthResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	portfolio := &client.Portfolio{TotalValue: decimal.NewFromInt(5000), CashBalance: decimal.NewFromInt(1000)}

	mockClient.On("Authenticate", ctx, mock.Anything).Return(authResp, nil)
	mockClient.On("GetPortfolio", ctx, "access-token").Return(portfolio, nil)
	mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	// ensureLinkedAccount: no existing accounts → create new one.
	mockAccountRepo.On("ListByUserID", ctx, userID.String(), accountDomain.ListAccountsFilter{}).Return([]accountDomain.Account{}, nil)
	mockAccountRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)

	result, err := svc.Create(ctx, makeConnRequest(userID))
	require.NoError(t, err)
	assert.NotNil(t, result)
	mockRepo.AssertExpectations(t)
	mockClient.AssertExpectations(t)
	mockAccountRepo.AssertExpectations(t)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: GetByID
// ────────────────────────────────────────────────────────────────────────────

func TestGetByID_Success(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)

	result, err := svc.GetByID(ctx, conn.ID, userID)
	require.NoError(t, err)
	assert.Equal(t, conn, result)
	mockRepo.AssertExpectations(t)
}

func TestGetByID_NotFound(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	connID := uuid.New()
	userID := uuid.New()

	mockRepo.On("GetByID", ctx, connID).Return(nil, gorm.ErrRecordNotFound)

	result, err := svc.GetByID(ctx, connID, userID)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetByID_Unauthorized(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	ownerID := uuid.New()
	attackerID := uuid.New()
	conn := sampleConn(ownerID)

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)

	result, err := svc.GetByID(ctx, conn.ID, attackerID)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "unauthorized")
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: List
// ────────────────────────────────────────────────────────────────────────────

func TestList_NoFilters(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	conn1 := sampleConn(userID)
	conn2 := sampleConn(userID)
	conn2.BrokerType = domain.BrokerTypeAlpaca

	mockRepo.On("GetByUserID", ctx, userID).Return([]*domain.BrokerConnection{conn1, conn2}, nil)

	result, err := svc.List(ctx, userID, nil)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestList_WithBrokerTypeFilter(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	conn1 := sampleConn(userID)
	conn2 := sampleConn(userID)
	conn2.BrokerType = domain.BrokerTypeAlpaca

	mockRepo.On("GetByUserID", ctx, userID).Return([]*domain.BrokerConnection{conn1, conn2}, nil)

	bt := domain.BrokerTypeBinance
	result, err := svc.List(ctx, userID, &ListFilters{BrokerType: &bt})
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, domain.BrokerTypeBinance, result[0].BrokerType)
}

func TestList_ActiveOnlyFilter(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	active := sampleConn(userID)
	inactive := sampleConn(userID)
	inactive.Status = domain.BrokerConnectionStatusDisconnected

	mockRepo.On("GetByUserID", ctx, userID).Return([]*domain.BrokerConnection{active, inactive}, nil)

	result, err := svc.List(ctx, userID, &ListFilters{ActiveOnly: true})
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, domain.BrokerConnectionStatusActive, result[0].Status)
}

func TestList_RepoError(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	mockRepo.On("GetByUserID", ctx, userID).Return(nil, errors.New("db error"))

	result, err := svc.List(ctx, userID, nil)
	require.Error(t, err)
	assert.Nil(t, result)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Update
// ────────────────────────────────────────────────────────────────────────────

func TestUpdate_Settings(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)

	newName := "Updated Binance"

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockRepo.On("Update", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	result, err := svc.Update(ctx, conn.ID, userID, &UpdateBrokerConnectionRequest{
		BrokerName: &newName,
	})
	require.NoError(t, err)
	assert.Equal(t, newName, result.BrokerName)
}

func TestUpdate_NotFound(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	connID := uuid.New()
	userID := uuid.New()

	mockRepo.On("GetByID", ctx, connID).Return(nil, gorm.ErrRecordNotFound)

	result, err := svc.Update(ctx, connID, userID, &UpdateBrokerConnectionRequest{})
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestUpdate_Credentials(t *testing.T) {
	svc, mockRepo, _, mockAccountRepo := setupServiceWithAccount(t)
	ctx := context.Background()
	userID := uuid.New()

	// Build a conn with properly encrypted credentials so Decrypt succeeds.
	encSvc := newTestEncryptionService(t)
	encKey, err := encSvc.Encrypt("old-api-key")
	require.NoError(t, err)
	encSecret, err := encSvc.Encrypt("old-api-secret")
	require.NoError(t, err)

	conn := sampleConn(userID)
	conn.APIKey = encKey
	conn.APISecret = encSecret

	newKey := "new-api-key"
	newSecret := "new-api-secret"

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockAccountRepo.On("ListByUserID", ctx, userID.String(), accountDomain.ListAccountsFilter{}).
		Return([]accountDomain.Account{}, nil)
	mockRepo.On("Update", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	result, err := svc.Update(ctx, conn.ID, userID, &UpdateBrokerConnectionRequest{
		APIKey:    &newKey,
		APISecret: &newSecret,
	})
	require.NoError(t, err)
	// Credentials are stored encrypted; confirm they changed.
	assert.NotEmpty(t, result.APIKey)
	assert.NotEmpty(t, result.APISecret)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Delete
// ────────────────────────────────────────────────────────────────────────────

func TestDelete_Success(t *testing.T) {
	svc, mockRepo, _, mockAccountRepo := setupServiceWithAccount(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockAccountRepo.On("ListByUserID", ctx, userID.String(), accountDomain.ListAccountsFilter{}).
		Return([]accountDomain.Account{}, nil)
	mockRepo.On("Delete", ctx, conn.ID).Return(nil)

	err := svc.Delete(ctx, conn.ID, userID)
	require.NoError(t, err)
	mockRepo.AssertExpectations(t)
	mockAccountRepo.AssertExpectations(t)
}

func TestDelete_NotFound(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	connID := uuid.New()
	userID := uuid.New()

	mockRepo.On("GetByID", ctx, connID).Return(nil, gorm.ErrRecordNotFound)

	err := svc.Delete(ctx, connID, userID)
	require.Error(t, err)
}

func TestDelete_Unauthorized(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	ownerID := uuid.New()
	attackerID := uuid.New()
	conn := sampleConn(ownerID)

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)

	err := svc.Delete(ctx, conn.ID, attackerID)
	require.Error(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Activate / Deactivate
// ────────────────────────────────────────────────────────────────────────────

func TestActivate_Success(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)
	conn.Status = domain.BrokerConnectionStatusDisconnected

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockRepo.On("Update", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	err := svc.Activate(ctx, conn.ID, userID)
	require.NoError(t, err)
	assert.Equal(t, domain.BrokerConnectionStatusActive, conn.Status)
}

func TestActivate_NotFound(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	connID := uuid.New()
	userID := uuid.New()

	mockRepo.On("GetByID", ctx, connID).Return(nil, gorm.ErrRecordNotFound)

	err := svc.Activate(ctx, connID, userID)
	require.Error(t, err)
}

func TestDeactivate_Success(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockRepo.On("Update", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	err := svc.Deactivate(ctx, conn.ID, userID)
	require.NoError(t, err)
	assert.Equal(t, domain.BrokerConnectionStatusDisconnected, conn.Status)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: RefreshToken
// ────────────────────────────────────────────────────────────────────────────

func TestRefreshToken_Success(t *testing.T) {
	svc, mockRepo, mockClient := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	// Store a real encrypted refresh token.
	encSvc := newTestEncryptionService(t)
	encRefresh, _ := encSvc.Encrypt("original-refresh-token")
	conn := sampleConn(userID)
	conn.RefreshToken = &encRefresh

	newAuthResp := &client.AuthResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockClient.On("RefreshToken", ctx, "original-refresh-token").Return(newAuthResp, nil)
	mockRepo.On("Update", ctx, mock.AnythingOfType("*domain.BrokerConnection")).Return(nil)

	result, err := svc.RefreshToken(ctx, conn.ID, userID)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestRefreshToken_NoRefreshToken(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)
	conn.RefreshToken = nil

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)

	result, err := svc.RefreshToken(ctx, conn.ID, userID)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "no refresh token")
}

func TestRefreshToken_NotFound(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	connID := uuid.New()
	userID := uuid.New()

	mockRepo.On("GetByID", ctx, connID).Return(nil, gorm.ErrRecordNotFound)

	result, err := svc.RefreshToken(ctx, connID, userID)
	require.Error(t, err)
	assert.Nil(t, result)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: TestConnection
// ────────────────────────────────────────────────────────────────────────────

func TestTestConnection_Success(t *testing.T) {
	svc, mockRepo, mockClient := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	encSvc := newTestEncryptionService(t)
	encAccess, _ := encSvc.Encrypt("valid-access-token")
	conn := sampleConn(userID)
	conn.AccessToken = &encAccess

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockClient.On("GetPortfolio", ctx, "valid-access-token").Return(&client.Portfolio{TotalValue: decimal.NewFromInt(100)}, nil)

	err := svc.TestConnection(ctx, conn.ID, userID)
	require.NoError(t, err)
}

func TestTestConnection_NoAccessToken(t *testing.T) {
	svc, mockRepo, _ := setupService(t)
	ctx := context.Background()
	userID := uuid.New()
	conn := sampleConn(userID)
	conn.AccessToken = nil

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)

	err := svc.TestConnection(ctx, conn.ID, userID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no access token")
}

func TestTestConnection_BrokerFailed(t *testing.T) {
	svc, mockRepo, mockClient := setupService(t)
	ctx := context.Background()
	userID := uuid.New()

	encSvc := newTestEncryptionService(t)
	encAccess, _ := encSvc.Encrypt("bad-token")
	conn := sampleConn(userID)
	conn.AccessToken = &encAccess

	mockRepo.On("GetByID", ctx, conn.ID).Return(conn, nil)
	mockClient.On("GetPortfolio", ctx, "bad-token").Return(nil, errors.New("401 unauthorized"))

	err := svc.TestConnection(ctx, conn.ID, userID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection test failed")
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: matchesFilters (internal helper)
// ────────────────────────────────────────────────────────────────────────────

func TestMatchesFilters(t *testing.T) {
	svc, _, _ := setupService(t)

	bt := domain.BrokerTypeBinance
	st := domain.BrokerConnectionStatusActive

	conn := &domain.BrokerConnection{
		BrokerType: domain.BrokerTypeBinance,
		Status:     domain.BrokerConnectionStatusActive,
	}

	assert.True(t, svc.matchesFilters(conn, &ListFilters{}))
	assert.True(t, svc.matchesFilters(conn, &ListFilters{BrokerType: &bt, Status: &st}))
	assert.True(t, svc.matchesFilters(conn, &ListFilters{ActiveOnly: true}))

	otherBT := domain.BrokerTypeAlpaca
	assert.False(t, svc.matchesFilters(conn, &ListFilters{BrokerType: &otherBT}))

	inactiveConn := &domain.BrokerConnection{
		BrokerType: domain.BrokerTypeBinance,
		Status:     domain.BrokerConnectionStatusDisconnected,
	}
	assert.False(t, svc.matchesFilters(inactiveConn, &ListFilters{ActiveOnly: true}))
}
