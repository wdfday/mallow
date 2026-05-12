package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	"mallow/investment/internal/shared"
)

// MockRepository is a mock implementation of repository.Repository
type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) GetByID(ctx context.Context, id string) (*domain.Account, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Account), args.Error(1)
}

func (m *MockRepository) GetByIDAndUserID(ctx context.Context, id, userID string) (*domain.Account, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Account), args.Error(1)
}

func (m *MockRepository) ListByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) ([]domain.Account, error) {
	args := m.Called(ctx, userID, filters)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.Account), args.Error(1)
}

func (m *MockRepository) Create(ctx context.Context, account *domain.Account) error {
	args := m.Called(ctx, account)
	return args.Error(0)
}

func (m *MockRepository) Update(ctx context.Context, account *domain.Account) error {
	args := m.Called(ctx, account)
	return args.Error(0)
}

func (m *MockRepository) UpdateColumns(ctx context.Context, id string, columns map[string]any) error {
	args := m.Called(ctx, id, columns)
	return args.Error(0)
}

func (m *MockRepository) SoftDelete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) CountByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) (int64, error) {
	args := m.Called(ctx, userID, filters)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockRepository) GetAccountsNeedingSync(ctx context.Context) ([]*domain.Account, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Account), args.Error(1)
}

func (m *MockRepository) UpdateBalance(ctx context.Context, accountID string, balanceDelta decimal.Decimal) error {
	args := m.Called(ctx, accountID, balanceDelta)
	return args.Error(0)
}

func (m *MockRepository) UpdateBalanceWithTx(tx *gorm.DB, accountID string, balanceDelta decimal.Decimal) error {
	args := m.Called(tx, accountID, balanceDelta)
	return args.Error(0)
}

// setupService creates a new service with mock repository for testing
func setupService() (*accountService, *MockRepository) {
	mockRepo := new(MockRepository)
	logger := slog.Default()
	svc := &accountService{
		repo:   mockRepo,
		logger: logger,
	}
	return svc, mockRepo
}

func TestCreateAccount(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()

	t.Run("create spot account successfully", func(t *testing.T) {
		svc, mockRepo := setupService()

		req := accountdto.CreateAccountRequest{
			AccountName: "Binance Spot",
			AccountType: "spot",
		}

		createdAccount := &domain.Account{
			ID:                uuid.New(),
			UserID:            uuid.MustParse(userID),
			AccountName:       "Binance Spot",
			AccountType:       domain.AccountTypeSpot,
			CurrentBalance:    decimal.Zero,
			Currency:          domain.CurrencyUSD,
			IsActive:          true,
			IncludeInNetWorth: true,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, mock.AnythingOfType("string"), userID).Return(createdAccount, nil)

		result, err := svc.CreateAccount(ctx, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, domain.AccountTypeSpot, result.AccountType)
		mockRepo.AssertExpectations(t)
	})

	t.Run("create unified account with optional fields", func(t *testing.T) {
		svc, mockRepo := setupService()
		institutionName := "OKX"
		currentBalance := 5000.0
		currency := "USD"
		isActive := true
		includeInNetWorth := true

		req := accountdto.CreateAccountRequest{
			AccountName:       "OKX Unified",
			AccountType:       "unified",
			InstitutionName:   &institutionName,
			CurrentBalance:    &currentBalance,
			Currency:          &currency,
			IsActive:          &isActive,
			IncludeInNetWorth: &includeInNetWorth,
		}

		createdAccount := &domain.Account{
			ID:          uuid.New(),
			UserID:      uuid.MustParse(userID),
			AccountName: "OKX Unified",
			AccountType: domain.AccountTypeUnified,
			Currency:    domain.CurrencyUSD,
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, mock.AnythingOfType("string"), userID).Return(createdAccount, nil)

		result, err := svc.CreateAccount(ctx, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail with invalid user ID", func(t *testing.T) {
		svc, _ := setupService()

		req := accountdto.CreateAccountRequest{
			AccountName: "Spot",
			AccountType: "spot",
		}

		result, err := svc.CreateAccount(ctx, "invalid-uuid", req)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrBadRequest.Code, err.(*shared.AppError).Code)
	})

	t.Run("fail with invalid account type", func(t *testing.T) {
		svc, _ := setupService()

		req := accountdto.CreateAccountRequest{
			AccountName: "My Account",
			AccountType: "invalid_type",
		}

		result, err := svc.CreateAccount(ctx, userID, req)

		require.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("fail when repository create fails", func(t *testing.T) {
		svc, mockRepo := setupService()

		req := accountdto.CreateAccountRequest{
			AccountName: "Spot",
			AccountType: "spot",
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(errors.New("database error"))

		result, err := svc.CreateAccount(ctx, userID, req)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestGetByID(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()

	t.Run("get account successfully", func(t *testing.T) {
		svc, mockRepo := setupService()

		expectedAccount := &domain.Account{
			ID:          uuid.MustParse(accountID),
			UserID:      uuid.MustParse(userID),
			AccountName: "Test Account",
			AccountType: domain.AccountTypeSpot,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(expectedAccount, nil)

		result, err := svc.GetByID(ctx, accountID, userID)

		require.NoError(t, err)
		assert.Equal(t, expectedAccount, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("account not found", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, shared.ErrNotFound)

		result, err := svc.GetByID(ctx, accountID, userID)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, errors.New("database error"))

		result, err := svc.GetByID(ctx, accountID, userID)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestGetByUserID(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()

	t.Run("list accounts successfully", func(t *testing.T) {
		svc, mockRepo := setupService()

		req := accountdto.ListAccountsRequest{}

		expectedAccounts := []domain.Account{
			{
				ID:          uuid.New(),
				UserID:      uuid.MustParse(userID),
				AccountName: "Binance Spot",
				AccountType: domain.AccountTypeSpot,
			},
			{
				ID:          uuid.New(),
				UserID:      uuid.MustParse(userID),
				AccountName: "OKX Unified",
				AccountType: domain.AccountTypeUnified,
			},
		}

		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{
			IncludeDeleted: false,
		}).Return(expectedAccounts, nil)
		mockRepo.On("CountByUserID", ctx, userID, domain.ListAccountsFilter{
			IncludeDeleted: false,
		}).Return(int64(2), nil)

		accounts, total, err := svc.GetByUserID(ctx, userID, req)

		require.NoError(t, err)
		assert.Len(t, accounts, 2)
		assert.Equal(t, int64(2), total)
		mockRepo.AssertExpectations(t)
	})

	t.Run("list accounts with account_type filter", func(t *testing.T) {
		svc, mockRepo := setupService()

		accountType := "spot"
		isActive := true

		req := accountdto.ListAccountsRequest{
			AccountType: &accountType,
			IsActive:    &isActive,
		}

		expectedAccounts := []domain.Account{
			{
				ID:          uuid.New(),
				UserID:      uuid.MustParse(userID),
				AccountName: "Binance Spot",
				AccountType: domain.AccountTypeSpot,
				IsActive:    true,
			},
		}

		atVal := domain.AccountTypeSpot
		filters := domain.ListAccountsFilter{
			AccountType:    &atVal,
			IsActive:       &isActive,
			IncludeDeleted: false,
		}

		mockRepo.On("ListByUserID", ctx, userID, filters).Return(expectedAccounts, nil)
		mockRepo.On("CountByUserID", ctx, userID, filters).Return(int64(1), nil)

		accounts, total, err := svc.GetByUserID(ctx, userID, req)

		require.NoError(t, err)
		assert.Len(t, accounts, 1)
		assert.Equal(t, int64(1), total)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail with invalid account type", func(t *testing.T) {
		svc, _ := setupService()

		invalidType := "invalid_type"
		req := accountdto.ListAccountsRequest{
			AccountType: &invalidType,
		}

		accounts, total, err := svc.GetByUserID(ctx, userID, req)

		require.Error(t, err)
		assert.Nil(t, accounts)
		assert.Equal(t, int64(0), total)
	})

	t.Run("repository error on list", func(t *testing.T) {
		svc, mockRepo := setupService()

		req := accountdto.ListAccountsRequest{}

		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{
			IncludeDeleted: false,
		}).Return(nil, errors.New("database error"))

		accounts, total, err := svc.GetByUserID(ctx, userID, req)

		require.Error(t, err)
		assert.Nil(t, accounts)
		assert.Equal(t, int64(0), total)
		mockRepo.AssertExpectations(t)
	})
}
