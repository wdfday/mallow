package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"log/slog"

	"mallow/investment/internal/middleware"
	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	accountservice "mallow/investment/internal/module/account/service"
	"mallow/investment/internal/shared"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock service
// ────────────────────────────────────────────────────────────────────────────

type MockAccountService struct{ mock.Mock }

func (m *MockAccountService) CreateAccount(ctx context.Context, userID string, req accountdto.CreateAccountRequest) (*domain.Account, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Account), args.Error(1)
}
func (m *MockAccountService) CreateDefaultCashAccount(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}
func (m *MockAccountService) GetByID(ctx context.Context, id, userID string) (*domain.Account, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Account), args.Error(1)
}
func (m *MockAccountService) GetByUserID(ctx context.Context, userID string, req accountdto.ListAccountsRequest) ([]domain.Account, int64, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]domain.Account), args.Get(1).(int64), args.Error(2)
}
func (m *MockAccountService) UpdateAccount(ctx context.Context, id, userID string, req accountdto.UpdateAccountRequest) (*domain.Account, error) {
	args := m.Called(ctx, id, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Account), args.Error(1)
}
func (m *MockAccountService) UpdateAvailableBalance(ctx context.Context, accountID uuid.UUID, balance decimal.Decimal) error {
	return m.Called(ctx, accountID, balance).Error(0)
}
func (m *MockAccountService) DeleteAccount(ctx context.Context, id, userID string) error {
	return m.Called(ctx, id, userID).Error(0)
}

// compile-time check
var _ accountservice.Service = (*MockAccountService)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// makeTestRequest builds a gin engine with mock service and executes a request.
// authUser is the user injected into context; pass nil for unauthenticated requests.
func makeTestRequest(
	t *testing.T,
	mockSvc accountservice.Service,
	method, path string,
	body any,
	authUser *middleware.AuthUser,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewHandler(mockSvc, slog.Default())

	if authUser != nil {
		r.Use(func(c *gin.Context) {
			c.Set("current_user", *authUser)
			c.Set("user_id", authUser.ID.String())
			c.Next()
		})
	}

	r.GET("/api/v1/investment/accounts", h.getMyAccounts)
	r.GET("/api/v1/investment/accounts/:id", h.getAccount)

	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func testUser() *middleware.AuthUser {
	return &middleware.AuthUser{
		ID:    uuid.New(),
		Role:  "user",
		Email: "test@example.com",
	}
}

func sampleAccount(userID uuid.UUID) *domain.Account {
	return &domain.Account{
		ID:          uuid.New(),
		UserID:      userID,
		AccountName: "Test Cash",
		AccountType: domain.AccountTypeCash,
		Currency:    domain.CurrencyVND,
		IsActive:    true,
	}
}

func TestGetMyAccounts_Success(t *testing.T) {
	mockSvc := new(MockAccountService)
	user := testUser()
	accounts := []domain.Account{*sampleAccount(user.ID)}

	mockSvc.On("GetByUserID", mock.Anything, user.ID.String(), accountdto.ListAccountsRequest{}).
		Return(accounts, int64(1), nil)

	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/accounts", nil, user)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	mockSvc.AssertExpectations(t)
}

func TestGetMyAccounts_NoUser(t *testing.T) {
	mockSvc := new(MockAccountService)
	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/accounts", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestGetMyAccounts_ServiceError(t *testing.T) {
	mockSvc := new(MockAccountService)
	user := testUser()

	mockSvc.On("GetByUserID", mock.Anything, user.ID.String(), accountdto.ListAccountsRequest{}).
		Return(nil, int64(0), fmt.Errorf("db error"))

	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/accounts", nil, user)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetMyAccounts_Empty(t *testing.T) {
	mockSvc := new(MockAccountService)
	user := testUser()

	mockSvc.On("GetByUserID", mock.Anything, user.ID.String(), accountdto.ListAccountsRequest{}).
		Return([]domain.Account{}, int64(0), nil)

	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/accounts", nil, user)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: getAccount
// ────────────────────────────────────────────────────────────────────────────

func TestGetAccount_Success(t *testing.T) {
	mockSvc := new(MockAccountService)
	user := testUser()
	account := sampleAccount(user.ID)

	mockSvc.On("GetByID", mock.Anything, account.ID.String(), user.ID.String()).
		Return(account, nil)

	path := fmt.Sprintf("/api/v1/investment/accounts/%s", account.ID)
	w := makeTestRequest(t, mockSvc, http.MethodGet, path, nil, user)
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestGetAccount_NoUser(t *testing.T) {
	mockSvc := new(MockAccountService)
	accountID := uuid.New()

	path := fmt.Sprintf("/api/v1/investment/accounts/%s", accountID)
	w := makeTestRequest(t, mockSvc, http.MethodGet, path, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestGetAccount_NotFound(t *testing.T) {
	mockSvc := new(MockAccountService)
	user := testUser()
	accountID := uuid.New()

	mockSvc.On("GetByID", mock.Anything, accountID.String(), user.ID.String()).
		Return(nil, shared.ErrNotFound)

	path := fmt.Sprintf("/api/v1/investment/accounts/%s", accountID)
	w := makeTestRequest(t, mockSvc, http.MethodGet, path, nil, user)
	assert.Equal(t, http.StatusNotFound, w.Code)
	mockSvc.AssertExpectations(t)
}
