package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/middleware"
	authdomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/profile/dto"
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ==================== Mocks ====================

type MockProfileService struct {
	mock.Mock
}

func (m *MockProfileService) CreateProfile(ctx context.Context, userID string, req dto.CreateProfileRequest) (*domain.UserProfile, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.UserProfile), args.Error(1)
}

func (m *MockProfileService) CreateDefaultProfile(ctx context.Context, userID string) (*domain.UserProfile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.UserProfile), args.Error(1)
}

func (m *MockProfileService) GetProfile(ctx context.Context, userID string) (*domain.UserProfile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.UserProfile), args.Error(1)
}

func (m *MockProfileService) UpdateProfile(ctx context.Context, userID string, req dto.UpdateProfileRequest) (*domain.UserProfile, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.UserProfile), args.Error(1)
}

// ==================== Test Setup ====================

func setupProfileTest() (*gin.Engine, *MockProfileService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mockProfileService := new(MockProfileService)
	handler := NewHandler(mockProfileService)

	// Middleware to inject user (simulating auth middleware)
	router.Use(func(c *gin.Context) {
		// Inject a test user into context
		userID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
		authUser := authdomain.AuthUser{
			ID:       userID,
			Username: "test@example.com",
			Role:     userdomain.UserRoleUser,
		}
		c.Set(middleware.UserKey, authUser)
		c.Next()
	})

	// Register profile routes
	profile := router.Group("/api/v1/profile")
	{
		profile.GET("/me", handler.getProfile)
		profile.PUT("/me", handler.updateProfile)
	}

	return router, mockProfileService
}

// ==================== Tests ====================

// TestGetProfile tests get profile endpoint
func TestGetProfile(t *testing.T) {
	t.Run("Success - Get existing profile", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		occupation := "Software Engineer"
		monthlyIncome := 50000000.0

		profile := &domain.UserProfile{
			ID:               uuid.New(),
			UserID:           uuid.MustParse(userID),
			Occupation:       &occupation,
			MonthlyIncomeAvg: &monthlyIncome,
			RiskTolerance:    domain.RiskToleranceModerate,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		}

		mockService.On("GetProfile", mock.Anything, userID).Return(profile, nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/profile/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.NotNil(t, response["data"])

		data := response["data"].(map[string]interface{})
		assert.Equal(t, occupation, data["occupation"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Profile not found", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"

		mockService.On("GetProfile", mock.Anything, userID).
			Return(nil, shared.ErrProfileNotFound)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/profile/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		mockService.AssertExpectations(t)
	})
}

// TestUpdateProfile tests update profile endpoint
func TestUpdateProfile(t *testing.T) {
	t.Run("Success - Update profile with occupation", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		newOccupation := "Senior Software Engineer"
		newIncome := 70000000.0

		reqBody := dto.UpdateProfileRequest{
			Occupation:       &newOccupation,
			MonthlyIncomeAvg: &newIncome,
		}

		updatedProfile := &domain.UserProfile{
			ID:               uuid.New(),
			UserID:           uuid.MustParse(userID),
			Occupation:       &newOccupation,
			MonthlyIncomeAvg: &newIncome,
			RiskTolerance:    domain.RiskToleranceModerate,
			UpdatedAt:        time.Now(),
		}

		mockService.On("UpdateProfile", mock.Anything, userID, mock.MatchedBy(func(req dto.UpdateProfileRequest) bool {
			return *req.Occupation == newOccupation && *req.MonthlyIncomeAvg == newIncome
		})).Return(updatedProfile, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, "Profile updated successfully", response["message"])

		data := response["data"].(map[string]interface{})
		assert.Equal(t, newOccupation, data["occupation"])

		mockService.AssertExpectations(t)
	})

	t.Run("Success - Update risk tolerance", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		newRiskTolerance := domain.RiskToleranceAggressive
		newRiskToleranceStr := string(newRiskTolerance)

		reqBody := dto.UpdateProfileRequest{
			RiskTolerance: &newRiskToleranceStr,
		}

		updatedProfile := &domain.UserProfile{
			ID:            uuid.New(),
			UserID:        uuid.MustParse(userID),
			RiskTolerance: newRiskTolerance,
			UpdatedAt:     time.Now(),
		}

		mockService.On("UpdateProfile", mock.Anything, userID, mock.MatchedBy(func(req dto.UpdateProfileRequest) bool {
			return *req.RiskTolerance == string(newRiskTolerance)
		})).Return(updatedProfile, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		mockService.AssertExpectations(t)
	})

	t.Run("Success - Complete onboarding", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		onboardingCompleted := true

		reqBody := dto.UpdateProfileRequest{
			OnboardingCompleted: &onboardingCompleted,
		}

		now := time.Now()
		updatedProfile := &domain.UserProfile{
			ID:                    uuid.New(),
			UserID:                uuid.MustParse(userID),
			OnboardingCompleted:   true,
			OnboardingCompletedAt: &now,
			UpdatedAt:             time.Now(),
		}

		mockService.On("UpdateProfile", mock.Anything, userID, mock.MatchedBy(func(req dto.UpdateProfileRequest) bool {
			return *req.OnboardingCompleted == true
		})).Return(updatedProfile, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		data := response["data"].(map[string]interface{})
		assert.Equal(t, true, data["onboarding_completed"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Invalid request body", func(t *testing.T) {
		router, _ := setupProfileTest()

		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

	})

	t.Run("Error - Profile not found", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		newOccupation := "Software Engineer"

		reqBody := dto.UpdateProfileRequest{
			Occupation: &newOccupation,
		}

		mockService.On("UpdateProfile", mock.Anything, userID, mock.AnythingOfType("dto.UpdateProfileRequest")).
			Return(nil, shared.ErrProfileNotFound)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)

		mockService.AssertExpectations(t)
	})
}

// TestUpdateProfileValidation tests validation logic
func TestUpdateProfileValidation(t *testing.T) {
	t.Run("Update with empty body should succeed (no changes)", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"

		reqBody := dto.UpdateProfileRequest{}

		existingProfile := &domain.UserProfile{
			ID:            uuid.New(),
			UserID:        uuid.MustParse(userID),
			RiskTolerance: domain.RiskToleranceModerate,
			UpdatedAt:     time.Now(),
		}

		mockService.On("UpdateProfile", mock.Anything, userID, mock.AnythingOfType("dto.UpdateProfileRequest")).
			Return(existingProfile, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile/me", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		mockService.AssertExpectations(t)
	})
}

// TestProfileResponseFormat tests response format
func TestProfileResponseFormat(t *testing.T) {
	t.Run("Response includes all expected fields", func(t *testing.T) {
		router, mockService := setupProfileTest()

		userID := "550e8400-e29b-41d4-a716-446655440000"
		occupation := "Software Engineer"
		industry := "Technology"
		monthlyIncome := 50000000.0
		creditScore := 750
		emergencyFund := 6.0

		profile := &domain.UserProfile{
			ID:                  uuid.New(),
			UserID:              uuid.MustParse(userID),
			Occupation:          &occupation,
			Industry:            &industry,
			MonthlyIncomeAvg:    &monthlyIncome,
			CreditScore:         &creditScore,
			EmergencyFundMonths: &emergencyFund,
			RiskTolerance:       domain.RiskToleranceModerate,
			InvestmentHorizon:   domain.InvestmentHorizonMedium,
			OnboardingCompleted: true,
			CurrencyPrimary:     "VND",
			CurrencySecondary:   "USD",
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
		}

		mockService.On("GetProfile", mock.Anything, userID).Return(profile, nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/profile/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		data := response["data"].(map[string]interface{})
		assert.Equal(t, occupation, data["occupation"])
		assert.Equal(t, industry, data["industry"])
		assert.Equal(t, float64(monthlyIncome), data["monthly_income_avg"])
		assert.Equal(t, float64(creditScore), data["credit_score"])
		assert.Equal(t, emergencyFund, data["emergency_fund_months"])
		assert.Equal(t, string(domain.RiskToleranceModerate), data["risk_tolerance"])
		assert.Equal(t, true, data["onboarding_completed"])

		mockService.AssertExpectations(t)
	})
}
