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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"log/slog"

	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/dto"
	"mallow/investment/internal/module/broker/service"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock service
// ────────────────────────────────────────────────────────────────────────────

type MockBrokerConnectionService struct{ mock.Mock }

func (m *MockBrokerConnectionService) Create(ctx context.Context, req *dto.CreateBrokerConnectionServiceRequest) (*domain.BrokerConnection, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionService) GetByID(ctx context.Context, id, userID uuid.UUID) (*domain.BrokerConnection, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionService) List(ctx context.Context, userID uuid.UUID, filters *service.ListFilters) ([]*domain.BrokerConnection, error) {
	args := m.Called(ctx, userID, filters)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionService) Update(ctx context.Context, id, userID uuid.UUID, req *service.UpdateBrokerConnectionRequest) (*domain.BrokerConnection, error) {
	args := m.Called(ctx, id, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionService) Delete(ctx context.Context, id, userID uuid.UUID) error {
	return m.Called(ctx, id, userID).Error(0)
}
func (m *MockBrokerConnectionService) Activate(ctx context.Context, id, userID uuid.UUID) error {
	return m.Called(ctx, id, userID).Error(0)
}
func (m *MockBrokerConnectionService) Deactivate(ctx context.Context, id, userID uuid.UUID) error {
	return m.Called(ctx, id, userID).Error(0)
}
func (m *MockBrokerConnectionService) RefreshToken(ctx context.Context, id, userID uuid.UUID) (*domain.BrokerConnection, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.BrokerConnection), args.Error(1)
}
func (m *MockBrokerConnectionService) TestConnection(ctx context.Context, id, userID uuid.UUID) error {
	return m.Called(ctx, id, userID).Error(0)
}
func (m *MockBrokerConnectionService) ReBroker(ctx context.Context, accountID, newBrokerID, userID uuid.UUID) error {
	return m.Called(ctx, accountID, newBrokerID, userID).Error(0)
}

// compile-time check
var _ service.BrokerConnectionService = (*MockBrokerConnectionService)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// makeTestRequest creates a request with user_id pre-injected via a test wrapper engine.
func makeTestRequest(t *testing.T, mockSvc service.BrokerConnectionService, method, path string, body interface{}, userID string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewBrokerConnectionHandler(mockSvc, slog.Default())

	// Inject user_id if provided.
	if userID != "" {
		r.Use(func(c *gin.Context) {
			c.Set("user_id", userID)
			c.Next()
		})
	}

	// Public.
	r.GET("/api/v1/investment/broker-connections/providers", h.ListProviders)
	// Protected (guard in tests is the middleware above).
	r.POST("/api/v1/investment/broker-connections/binance", h.CreateBinance)
	r.POST("/api/v1/investment/broker-connections/okx", h.CreateOKX)
	r.POST("/api/v1/investment/broker-connections/alpaca", h.CreateAlpaca)
	r.POST("/api/v1/investment/broker-connections/bybit", h.CreateBybit)
	r.GET("/api/v1/investment/broker-connections", h.List)
	r.GET("/api/v1/investment/broker-connections/:id", h.GetByID)
	r.PUT("/api/v1/investment/broker-connections/:id", h.Update)
	r.DELETE("/api/v1/investment/broker-connections/:id", h.Delete)
	r.POST("/api/v1/investment/broker-connections/:id/activate", h.Activate)
	r.POST("/api/v1/investment/broker-connections/:id/deactivate", h.Deactivate)
	r.POST("/api/v1/investment/broker-connections/:id/refresh-token", h.RefreshToken)
	r.POST("/api/v1/investment/broker-connections/:id/test", h.TestConnection)

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

func sampleConn(userID uuid.UUID) *domain.BrokerConnection {
	return &domain.BrokerConnection{
		ID:         uuid.New(),
		UserID:     userID,
		BrokerType: domain.BrokerTypeBinance,
		BrokerName: "My Binance",
		Status:     domain.BrokerConnectionStatusActive,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: ListProviders
// ────────────────────────────────────────────────────────────────────────────

func TestListProviders(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/broker-connections/providers", nil, "")
	assert.Equal(t, http.StatusOK, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: CreateBinance
// ────────────────────────────────────────────────────────────────────────────

func TestCreateBinance_NoUser(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	body := map[string]interface{}{"api_key": "k", "api_secret": "s", "broker_name": "Binance"}
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/binance", body, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCreateBinance_BadRequest(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New().String()
	// Missing required fields.
	body := map[string]interface{}{"broker_name": "Binance"} // missing api_key and api_secret
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/binance", body, userID)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateBinance_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("Create", mock.Anything, mock.Anything).Return(conn, nil)

	body := map[string]interface{}{
		"broker_name": "My Binance",
		"api_key":     "test-key",
		"api_secret":  "test-secret",
	}
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/binance", body, userID.String())
	assert.Equal(t, http.StatusCreated, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestCreateBinance_ServiceError(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()

	mockSvc.On("Create", mock.Anything, mock.Anything).Return(nil, fmt.Errorf("auth failed"))

	body := map[string]interface{}{
		"broker_name": "My Binance",
		"api_key":     "bad-key",
		"api_secret":  "bad-secret",
	}
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/binance", body, userID.String())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockSvc.AssertExpectations(t)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: List
// ────────────────────────────────────────────────────────────────────────────

func TestList_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("List", mock.Anything, userID, mock.AnythingOfType("*service.ListFilters")).Return([]*domain.BrokerConnection{conn}, nil)

	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/broker-connections", nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestList_NoUser(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/broker-connections", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: GetByID
// ────────────────────────────────────────────────────────────────────────────

func TestGetByID_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("GetByID", mock.Anything, conn.ID, userID).Return(conn, nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodGet, path, nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestGetByID_BadUUID(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()

	w := makeTestRequest(t, mockSvc, http.MethodGet, "/api/v1/investment/broker-connections/not-a-uuid", nil, userID.String())
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetByID_NotFound(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	connID := uuid.New()

	mockSvc.On("GetByID", mock.Anything, connID, userID).Return(nil, fmt.Errorf("broker connection not found"))

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s", connID)
	w := makeTestRequest(t, mockSvc, http.MethodGet, path, nil, userID.String())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockSvc.AssertExpectations(t)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Update
// ────────────────────────────────────────────────────────────────────────────

func TestUpdate_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("Update", mock.Anything, conn.ID, userID, mock.AnythingOfType("*service.UpdateBrokerConnectionRequest")).Return(conn, nil)

	newName := "Updated"
	body := map[string]interface{}{"broker_name": newName}
	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodPut, path, body, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestUpdate_BadUUID(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()

	w := makeTestRequest(t, mockSvc, http.MethodPut, "/api/v1/investment/broker-connections/bad-id", map[string]interface{}{}, userID.String())
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Delete
// ────────────────────────────────────────────────────────────────────────────

func TestDelete_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("Delete", mock.Anything, conn.ID, userID).Return(nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodDelete, path, nil, userID.String())
	assert.Equal(t, http.StatusNoContent, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestDelete_NoUser(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	connID := uuid.New()

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s", connID)
	w := makeTestRequest(t, mockSvc, http.MethodDelete, path, nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: Activate / Deactivate
// ────────────────────────────────────────────────────────────────────────────

func TestActivate_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("Activate", mock.Anything, conn.ID, userID).Return(nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/activate", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestDeactivate_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("Deactivate", mock.Anything, conn.ID, userID).Return(nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/deactivate", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: RefreshToken
// ────────────────────────────────────────────────────────────────────────────

func TestRefreshToken_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("RefreshToken", mock.Anything, conn.ID, userID).Return(conn, nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/refresh-token", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestRefreshToken_Error(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	connID := uuid.New()

	mockSvc.On("RefreshToken", mock.Anything, connID, userID).Return(nil, fmt.Errorf("no refresh token"))

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/refresh-token", connID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Tests: TestConnection
// ────────────────────────────────────────────────────────────────────────────

func TestTestConnectionHandler_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)

	mockSvc.On("TestConnection", mock.Anything, conn.ID, userID).Return(nil)

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/test", conn.ID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTestConnectionHandler_Failed(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	connID := uuid.New()

	mockSvc.On("TestConnection", mock.Anything, connID, userID).Return(fmt.Errorf("connection test failed"))

	path := fmt.Sprintf("/api/v1/investment/broker-connections/%s/test", connID)
	w := makeTestRequest(t, mockSvc, http.MethodPost, path, nil, userID.String())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// Verify CreateOKX, CreateAlpaca, CreateBybit similarly parse broker-specific fields.

func TestCreateOKX_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)
	conn.BrokerType = domain.BrokerTypeOKX

	mockSvc.On("Create", mock.Anything, mock.Anything).Return(conn, nil)

	body := map[string]interface{}{
		"broker_name": "My OKX",
		"api_key":     "k",
		"api_secret":  "s",
		"passphrase":  "pp",
	}
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/okx", body, userID.String())
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestCreateAlpaca_Success(t *testing.T) {
	mockSvc := new(MockBrokerConnectionService)
	userID := uuid.New()
	conn := sampleConn(userID)
	conn.BrokerType = domain.BrokerTypeAlpaca

	mockSvc.On("Create", mock.Anything, mock.Anything).Return(conn, nil)

	body := map[string]interface{}{
		"broker_name": "My Alpaca",
		"api_key":     "k",
		"api_secret":  "s",
	}
	w := makeTestRequest(t, mockSvc, http.MethodPost, "/api/v1/investment/broker-connections/alpaca", body, userID.String())
	assert.Equal(t, http.StatusCreated, w.Code)
}
