package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/profile/dto"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// CreateProfile — additional error paths
// ---------------------------------------------------------------------------

func TestCreateProfile_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	mockRepo := new(MockProfileRepository)
	svc := NewService(mockRepo)
	userID := uuid.New()

	occupation := "Engineer"
	existingProfile := &domain.UserProfile{
		UserID:     userID,
		Occupation: &occupation,
	}
	// GetByUserID returns a profile → conflict
	mockRepo.On("GetByUserID", ctx, userID.String()).Return(existingProfile, nil)

	result, err := svc.CreateProfile(ctx, userID.String(), dto.CreateProfileRequest{})

	assert.Error(t, err)
	assert.Nil(t, result)
	appErr, ok := err.(*shared.AppError)
	if ok {
		assert.Equal(t, shared.ErrCodeConflict, appErr.Code)
	}
	mockRepo.AssertExpectations(t)
}

func TestCreateProfile_RepositoryCheckError(t *testing.T) {
	ctx := context.Background()
	mockRepo := new(MockProfileRepository)
	svc := NewService(mockRepo)
	userID := uuid.New()

	// Repo returns an error that is NOT profile-not-found → internal error
	mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, errors.New("db connection lost"))

	result, err := svc.CreateProfile(ctx, userID.String(), dto.CreateProfileRequest{})

	assert.Error(t, err)
	assert.Nil(t, result)
	mockRepo.AssertExpectations(t)
}

func TestCreateProfile_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	invalidStr := func(v string) *string { return &v }

	cases := []struct {
		name  string
		req   dto.CreateProfileRequest
		field string
	}{
		{
			name:  "invalid income_stability",
			req:   dto.CreateProfileRequest{IncomeStability: invalidStr("erratic")},
			field: "income_stability",
		},
		{
			name:  "invalid risk_tolerance",
			req:   dto.CreateProfileRequest{RiskTolerance: invalidStr("not-valid")},
			field: "risk_tolerance",
		},
		{
			name:  "invalid investment_horizon",
			req:   dto.CreateProfileRequest{InvestmentHorizon: invalidStr("never")},
			field: "investment_horizon",
		},
		{
			name:  "invalid investment_experience",
			req:   dto.CreateProfileRequest{InvestmentExperience: invalidStr("rocket-scientist")},
			field: "investment_experience",
		},
		{
			name:  "invalid budget_method",
			req:   dto.CreateProfileRequest{BudgetMethod: invalidStr("guessing")},
			field: "budget_method",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockRepo := new(MockProfileRepository)
			svc := NewService(mockRepo)
			userID := uuid.New()

			// Profile not found, so creation is attempted → hits validation
			mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound)

			result, err := svc.CreateProfile(ctx, userID.String(), tc.req)

			assert.Error(t, err)
			assert.Nil(t, result)

			appErr, ok := err.(*shared.AppError)
			assert.True(t, ok, "expected a structured AppError, not a generic error")
			assert.Equal(t, http.StatusBadRequest, appErr.StatusCode, "invalid enum input must map to 400, not fall through to 500")
			assert.Equal(t, tc.field, appErr.Details["field"])

			mockRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateProfile — additional validation error paths
// ---------------------------------------------------------------------------

func TestUpdateProfile_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	invalidStr := func(v string) *string { return &v }

	cases := []struct {
		name  string
		req   dto.UpdateProfileRequest
		field string
	}{
		{
			name:  "empty full_name",
			req:   dto.UpdateProfileRequest{FullName: invalidStr("   ")},
			field: "full_name",
		},
		{
			name:  "invalid risk_tolerance",
			req:   dto.UpdateProfileRequest{RiskTolerance: invalidStr("extreme")},
			field: "risk_tolerance",
		},
		{
			name:  "invalid investment_horizon",
			req:   dto.UpdateProfileRequest{InvestmentHorizon: invalidStr("forever")},
			field: "investment_horizon",
		},
		{
			name:  "invalid investment_experience",
			req:   dto.UpdateProfileRequest{InvestmentExperience: invalidStr("pro-trader")},
			field: "investment_experience",
		},
		{
			name:  "invalid budget_method",
			req:   dto.UpdateProfileRequest{BudgetMethod: invalidStr("magic")},
			field: "budget_method",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockRepo := new(MockProfileRepository)
			svc := NewService(mockRepo)
			userID := uuid.New().String()

			result, err := svc.UpdateProfile(ctx, userID, tc.req)

			assert.Error(t, err)
			assert.Nil(t, result)

			appErr, ok := err.(*shared.AppError)
			assert.True(t, ok, "expected a structured AppError, not a generic error")
			assert.Equal(t, http.StatusBadRequest, appErr.StatusCode, "invalid input must map to 400, not fall through to 500")
			assert.Equal(t, tc.field, appErr.Details["field"])

			// No repo methods should be called when validation fails.
			mockRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateProfile — UpdateColumns internal error
// ---------------------------------------------------------------------------

func TestUpdateProfile_RepoInternalError(t *testing.T) {
	ctx := context.Background()
	mockRepo := new(MockProfileRepository)
	svc := NewService(mockRepo)

	userID := uuid.New().String()
	newOccupation := "Developer"
	req := dto.UpdateProfileRequest{Occupation: &newOccupation}

	mockRepo.On("UpdateColumns", ctx, userID, mock.Anything).Return(errors.New("db failure"))

	result, err := svc.UpdateProfile(ctx, userID, req)

	assert.Error(t, err)
	assert.Nil(t, result)
	mockRepo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetProfile — invalid UUID
// ---------------------------------------------------------------------------

func TestGetProfile_InvalidUUID(t *testing.T) {
	ctx := context.Background()
	mockRepo := new(MockProfileRepository)
	svc := NewService(mockRepo)

	result, err := svc.GetProfile(ctx, "not-a-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	mockRepo.AssertExpectations(t)
}
