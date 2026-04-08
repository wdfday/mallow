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

// TestCreateAccount tests account creation
func TestCreateAccount(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()

	t.Run("create basic cash account successfully", func(t *testing.T) {
		svc, mockRepo := setupService()
		accountType := "cash"

		req := accountdto.CreateAccountRequest{
			AccountName: "My Cash",
			AccountType: accountType,
		}

		createdAccount := &domain.Account{
			ID:                uuid.New(),
			UserID:            uuid.MustParse(userID),
			AccountName:       "My Cash",
			AccountType:       domain.AccountTypeCash,
			CurrentBalance:    decimal.Zero,
			Currency:          domain.CurrencyVND,
			IsActive:          true,
			IsPrimary:         false,
			IncludeInNetWorth: true,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, mock.AnythingOfType("string"), userID).Return(createdAccount, nil)

		result, err := svc.CreateAccount(ctx, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("create account with all optional fields", func(t *testing.T) {
		svc, mockRepo := setupService()
		accountType := "bank"
		institutionName := "Test Bank"
		currentBalance := 10000000.0
		availableBalance := 9500000.0
		currency := "USD"
		accountNumberMasked := "****1234"
		isActive := true
		isPrimary := false
		includeInNetWorth := true

		req := accountdto.CreateAccountRequest{
			AccountName:         "My Bank Account",
			AccountType:         accountType,
			InstitutionName:     &institutionName,
			CurrentBalance:      &currentBalance,
			AvailableBalance:    &availableBalance,
			Currency:            &currency,
			AccountNumberMasked: &accountNumberMasked,
			IsActive:            &isActive,
			IsPrimary:           &isPrimary,
			IncludeInNetWorth:   &includeInNetWorth,
		}

		createdAccount := &domain.Account{
			ID:          uuid.New(),
			UserID:      uuid.MustParse(userID),
			AccountName: "My Bank Account",
			AccountType: domain.AccountTypeBank,
			Currency:    domain.Currency(currency),
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, mock.AnythingOfType("string"), userID).Return(createdAccount, nil)

		result, err := svc.CreateAccount(ctx, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("create primary account", func(t *testing.T) {
		svc, mockRepo := setupService()
		accountType := "cash"
		isPrimary := true

		req := accountdto.CreateAccountRequest{
			AccountName: "Primary Cash",
			AccountType: accountType,
			IsPrimary:   &isPrimary,
		}

		createdAccount := &domain.Account{
			ID:          uuid.New(),
			UserID:      uuid.MustParse(userID),
			AccountName: "Primary Cash",
			AccountType: domain.AccountTypeCash,
			IsPrimary:   true,
		}

		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{IsPrimary: &isPrimary}).Return([]domain.Account{}, nil)
		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, mock.AnythingOfType("string"), userID).Return(createdAccount, nil)

		result, err := svc.CreateAccount(ctx, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail with invalid user ID", func(t *testing.T) {
		svc, _ := setupService()
		accountType := "cash"

		req := accountdto.CreateAccountRequest{
			AccountName: "My Cash",
			AccountType: accountType,
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
		accountType := "cash"

		req := accountdto.CreateAccountRequest{
			AccountName: "My Cash",
			AccountType: accountType,
		}

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(errors.New("database error"))

		result, err := svc.CreateAccount(ctx, userID, req)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

// TestCreateDefaultCashAccount tests default cash account creation
func TestCreateDefaultCashAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("create default cash account successfully", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(nil)

		err := svc.CreateDefaultCashAccount(ctx, userID)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail with invalid user ID", func(t *testing.T) {
		svc, _ := setupService()

		err := svc.CreateDefaultCashAccount(ctx, "invalid-uuid")

		require.Error(t, err)
		assert.Equal(t, shared.ErrBadRequest.Code, err.(*shared.AppError).Code)
	})

	t.Run("fail when repository create fails", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.Account")).Return(errors.New("database error"))

		err := svc.CreateDefaultCashAccount(ctx, userID)

		require.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}

// TestGetByID tests getting account by ID
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
			AccountType: domain.AccountTypeCash,
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

// TestGetByUserID tests listing accounts by user ID
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
				AccountName: "Account 1",
				AccountType: domain.AccountTypeCash,
			},
			{
				ID:          uuid.New(),
				UserID:      uuid.MustParse(userID),
				AccountName: "Account 2",
				AccountType: domain.AccountTypeBank,
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

	t.Run("list accounts with filters", func(t *testing.T) {
		svc, mockRepo := setupService()

		accountType := "cash"
		isActive := true
		isPrimary := true

		req := accountdto.ListAccountsRequest{
			AccountType: &accountType,
			IsActive:    &isActive,
			IsPrimary:   &isPrimary,
		}

		expectedAccounts := []domain.Account{
			{
				ID:          uuid.New(),
				UserID:      uuid.MustParse(userID),
				AccountName: "Primary Cash",
				AccountType: domain.AccountTypeCash,
				IsActive:    true,
				IsPrimary:   true,
			},
		}

		filters := domain.ListAccountsFilter{
			AccountType:    func() *domain.AccountType { at := domain.AccountTypeCash; return &at }(),
			IsActive:       &isActive,
			IsPrimary:      &isPrimary,
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

// TestUpdateAccount tests account updates
func TestUpdateAccount(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()

	t.Run("update account name successfully", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:          uuid.MustParse(accountID),
			UserID:      uuid.MustParse(userID),
			AccountName: "Old Name",
			AccountType: domain.AccountTypeCash,
		}

		newName := "New Name"
		req := accountdto.UpdateAccountRequest{
			AccountName: &newName,
		}

		updatedAccount := &domain.Account{
			ID:          uuid.MustParse(accountID),
			UserID:      uuid.MustParse(userID),
			AccountName: newName,
			AccountType: domain.AccountTypeCash,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil).Once()
		mockRepo.On("UpdateColumns", ctx, accountID, map[string]any{
			"account_name": newName,
		}).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(updatedAccount, nil).Once()

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.NoError(t, err)
		assert.Equal(t, newName, result.AccountName)
		mockRepo.AssertExpectations(t)
	})

	t.Run("update multiple fields", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:             uuid.MustParse(accountID),
			UserID:         uuid.MustParse(userID),
			AccountName:    "Old Name",
			AccountType:    domain.AccountTypeCash,
			CurrentBalance: decimal.Zero,
		}

		newName := "Updated Account"
		newBalanceF := 5000000.0
		newBalance := decimal.NewFromFloat(newBalanceF)
		isActive := false

		req := accountdto.UpdateAccountRequest{
			AccountName:    &newName,
			CurrentBalance: &newBalanceF,
			IsActive:       &isActive,
		}

		updatedAccount := &domain.Account{
			ID:             uuid.MustParse(accountID),
			UserID:         uuid.MustParse(userID),
			AccountName:    newName,
			CurrentBalance: newBalance,
			IsActive:       false,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil).Once()
		mockRepo.On("UpdateColumns", ctx, accountID, mock.AnythingOfType("map[string]interface {}")).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(updatedAccount, nil).Once()

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("update to primary account", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:        uuid.MustParse(accountID),
			UserID:    uuid.MustParse(userID),
			IsPrimary: false,
		}

		isPrimary := true
		req := accountdto.UpdateAccountRequest{
			IsPrimary: &isPrimary,
		}

		updatedAccount := &domain.Account{
			ID:        uuid.MustParse(accountID),
			UserID:    uuid.MustParse(userID),
			IsPrimary: true,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil).Once()
		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{IsPrimary: &isPrimary}).Return([]domain.Account{}, nil)
		mockRepo.On("UpdateColumns", ctx, accountID, map[string]any{"is_primary": true}).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(updatedAccount, nil).Once()

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.NoError(t, err)
		assert.True(t, result.IsPrimary)
		mockRepo.AssertExpectations(t)
	})

	t.Run("no updates returns existing account", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:     uuid.MustParse(accountID),
			UserID: uuid.MustParse(userID),
		}

		req := accountdto.UpdateAccountRequest{}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil).Once()

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.NoError(t, err)
		assert.Equal(t, existingAccount, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("account not found", func(t *testing.T) {
		svc, mockRepo := setupService()

		newName := "New Name"
		req := accountdto.UpdateAccountRequest{
			AccountName: &newName,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, shared.ErrNotFound)

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail with invalid account type", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:     uuid.MustParse(accountID),
			UserID: uuid.MustParse(userID),
		}

		invalidType := "invalid_type"
		req := accountdto.UpdateAccountRequest{
			AccountType: &invalidType,
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil)

		result, err := svc.UpdateAccount(ctx, accountID, userID, req)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

// TestDeleteAccount tests account deletion
func TestDeleteAccount(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()

	t.Run("delete account successfully", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:     uuid.MustParse(accountID),
			UserID: uuid.MustParse(userID),
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil)
		mockRepo.On("SoftDelete", ctx, accountID).Return(nil)

		err := svc.DeleteAccount(ctx, accountID, userID)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("account not found", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, shared.ErrNotFound)

		err := svc.DeleteAccount(ctx, accountID, userID)

		require.Error(t, err)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error on get", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, errors.New("database error"))

		err := svc.DeleteAccount(ctx, accountID, userID)

		require.Error(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error on delete", func(t *testing.T) {
		svc, mockRepo := setupService()

		existingAccount := &domain.Account{
			ID:     uuid.MustParse(accountID),
			UserID: uuid.MustParse(userID),
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount, nil)
		mockRepo.On("SoftDelete", ctx, accountID).Return(errors.New("database error"))

		err := svc.DeleteAccount(ctx, accountID, userID)

		require.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}
