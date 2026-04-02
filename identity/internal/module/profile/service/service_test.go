package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/profile/dto"
	"mallow/identity/internal/shared"
)

// stringPtr returns a pointer to the string value
func stringPtr(s string) *string {
	return &s
}

// ==================== Mocks ====================

type MockProfileRepository struct {
	mock.Mock
}

func (m *MockProfileRepository) Create(ctx context.Context, profile *domain.UserProfile) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockProfileRepository) GetByUserID(ctx context.Context, userID string) (*domain.UserProfile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.UserProfile), args.Error(1)
}

func (m *MockProfileRepository) Update(ctx context.Context, profile *domain.UserProfile) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockProfileRepository) Delete(ctx context.Context, userID uuid.UUID) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockProfileRepository) ExistsByUserID(ctx context.Context, userID uuid.UUID) (bool, error) {
	args := m.Called(ctx, userID)
	return args.Bool(0), args.Error(1)
}

func (m *MockProfileRepository) UpdateColumns(ctx context.Context, userID string, columns map[string]any) error {
	args := m.Called(ctx, userID, columns)
	return args.Error(0)
}

// ==================== Tests ====================

// TestCreateProfile tests profile creation
func TestCreateProfile(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Create profile with full data", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		occupation := "Software Engineer"
		industry := "Technology"
		monthlyIncome := 50000000.0
		creditScore := 750

		req := dto.CreateProfileRequest{
			Occupation:       &occupation,
			Industry:         &industry,
			MonthlyIncomeAvg: &monthlyIncome,
			CreditScore:      &creditScore,
			RiskTolerance:    stringPtr(string(domain.RiskToleranceModerate)),
		}

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound).Once()
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(&domain.UserProfile{
			UserID:           userID,
			Occupation:       &occupation,
			Industry:         &industry,
			MonthlyIncomeAvg: &monthlyIncome,
			CreditScore:      &creditScore,
		}, nil).Once()

		mockRepo.On("Create", ctx, mock.MatchedBy(func(p *domain.UserProfile) bool {
			return p.UserID == userID &&
				*p.Occupation == occupation &&
				*p.Industry == industry &&
				*p.MonthlyIncomeAvg == monthlyIncome &&
				*p.CreditScore == creditScore
		})).Return(nil)

		result, err := service.CreateProfile(ctx, userID.String(), req)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, userID, result.UserID)
		assert.Equal(t, occupation, *result.Occupation)
		assert.Equal(t, industry, *result.Industry)
		assert.Equal(t, monthlyIncome, *result.MonthlyIncomeAvg)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Success - Create default profile", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound).Once()

		mockRepo.On("Create", ctx, mock.MatchedBy(func(p *domain.UserProfile) bool {
			return p.UserID == userID &&
				p.RiskTolerance == domain.RiskToleranceModerate &&
				p.InvestmentHorizon == domain.InvestmentHorizonMedium &&
				p.InvestmentExperience == domain.InvestmentExperienceBeginner
		})).Return(nil)

		// Mock retrieval after creation
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(&domain.UserProfile{
			UserID:               userID,
			RiskTolerance:        domain.RiskToleranceModerate,
			InvestmentHorizon:    domain.InvestmentHorizonMedium,
			InvestmentExperience: domain.InvestmentExperienceBeginner,
			BudgetMethod:         domain.BudgetMethodCustom,
			CurrencyPrimary:      "VND",
			CurrencySecondary:    "USD",
			OnboardingCompleted:  false,
		}, nil).Once()

		result, err := service.CreateDefaultProfile(ctx, userID.String())

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, userID, result.UserID)
		assert.Equal(t, domain.RiskToleranceModerate, result.RiskTolerance)
		assert.False(t, result.OnboardingCompleted)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - Invalid user ID", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		req := dto.CreateProfileRequest{}

		result, err := service.CreateProfile(ctx, "invalid-uuid", req)

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("Error - Repository error", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		req := dto.CreateProfileRequest{}

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound)

		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.UserProfile")).
			Return(errors.New("database error"))

		result, err := service.CreateProfile(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockRepo.AssertExpectations(t)
	})
}

// TestGetProfile tests profile retrieval
func TestGetProfile(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Get existing profile", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		occupation := "Software Engineer"
		monthlyIncome := 50000000.0

		profile := &domain.UserProfile{
			ID:               uuid.New(),
			UserID:           userID,
			Occupation:       &occupation,
			MonthlyIncomeAvg: &monthlyIncome,
			RiskTolerance:    domain.RiskToleranceModerate,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		}

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(profile, nil)

		result, err := service.GetProfile(ctx, userID.String())

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, userID, result.UserID)
		assert.Equal(t, occupation, *result.Occupation)
		assert.Equal(t, monthlyIncome, *result.MonthlyIncomeAvg)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - Profile not found", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound)

		result, err := service.GetProfile(ctx, userID.String())

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrProfileNotFound, err)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - Invalid user ID", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		result, err := service.GetProfile(ctx, "invalid-uuid")

		assert.Error(t, err)
		assert.Nil(t, result)
	})
}

// TestUpdateProfile tests profile updates
func TestUpdateProfile(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Update profile with partial data", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		oldOccupation := "Software Engineer"
		newOccupation := "Senior Software Engineer"
		oldIncome := 50000000.0
		newIncome := 70000000.0

		existingProfile := &domain.UserProfile{
			ID:               uuid.New(),
			UserID:           userID,
			Occupation:       &oldOccupation,
			MonthlyIncomeAvg: &oldIncome,
			RiskTolerance:    domain.RiskToleranceModerate,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		}

		req := dto.UpdateProfileRequest{
			Occupation:       &newOccupation,
			MonthlyIncomeAvg: &newIncome,
		}

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["occupation"] == newOccupation &&
				cols["monthly_income_avg"] == newIncome
		})).Return(nil)

		updatedProfile := &domain.UserProfile{
			ID:               existingProfile.ID,
			UserID:           existingProfile.UserID,
			Occupation:       &newOccupation,
			MonthlyIncomeAvg: &newIncome,
			RiskTolerance:    existingProfile.RiskTolerance,
			CreatedAt:        existingProfile.CreatedAt,
			UpdatedAt:        existingProfile.UpdatedAt,
		}
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(updatedProfile, nil).Once()

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, newOccupation, *result.Occupation)
		assert.Equal(t, newIncome, *result.MonthlyIncomeAvg)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Success - Update risk tolerance", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()

		existingProfile := &domain.UserProfile{
			ID:            uuid.New(),
			UserID:        userID,
			RiskTolerance: domain.RiskToleranceModerate,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}

		newRiskTolerance := domain.RiskToleranceAggressive
		newRiskToleranceStr := string(newRiskTolerance)
		req := dto.UpdateProfileRequest{
			RiskTolerance: &newRiskToleranceStr,
		}

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["risk_tolerance"] == newRiskToleranceStr
		})).Return(nil)

		updatedProfile := &domain.UserProfile{
			ID:            existingProfile.ID,
			UserID:        existingProfile.UserID,
			RiskTolerance: newRiskTolerance,
			CreatedAt:     existingProfile.CreatedAt,
			UpdatedAt:     existingProfile.UpdatedAt,
		}
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(updatedProfile, nil).Once()

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, newRiskTolerance, result.RiskTolerance)
		assert.True(t, result.IsAggressiveInvestor())

		mockRepo.AssertExpectations(t)
	})

	t.Run("Success - Mark onboarding completed", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()

		existingProfile := &domain.UserProfile{
			ID:                  uuid.New(),
			UserID:              userID,
			OnboardingCompleted: false,
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
		}

		onboardingCompleted := true
		req := dto.UpdateProfileRequest{
			OnboardingCompleted: &onboardingCompleted,
		}

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["onboarding_completed"] == true &&
				cols["onboarding_completed_at"] != nil
		})).Return(nil)

		now := time.Now()
		updatedProfile := &domain.UserProfile{
			ID:                    existingProfile.ID,
			UserID:                existingProfile.UserID,
			OnboardingCompleted:   true,
			OnboardingCompletedAt: &now,
			CreatedAt:             existingProfile.CreatedAt,
			UpdatedAt:             existingProfile.UpdatedAt,
		}
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(updatedProfile, nil).Once()

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.OnboardingCompleted)
		assert.NotNil(t, result.OnboardingCompletedAt)
		assert.True(t, result.IsOnboardingComplete())

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - Profile not found", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		req := dto.UpdateProfileRequest{}

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound)

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - Invalid user ID", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		req := dto.UpdateProfileRequest{}

		result, err := service.UpdateProfile(ctx, "invalid-uuid", req)

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("Error - Repository UpdateColumns error", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		newOccupation := "Engineer"
		req := dto.UpdateProfileRequest{
			Occupation: &newOccupation,
		}

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.Anything).Return(errors.New("db error"))

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - UpdateColumns returns NotFound", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		newOccupation := "Engineer"
		req := dto.UpdateProfileRequest{
			Occupation: &newOccupation,
		}

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.Anything).Return(shared.ErrNotFound)

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrNotFound, err)

		mockRepo.AssertExpectations(t)
	})

	t.Run("Success - Empty update request", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)

		userID := uuid.New()
		req := dto.UpdateProfileRequest{} // No fields to update

		existingProfile := &domain.UserProfile{
			ID:            uuid.New(),
			UserID:        userID,
			RiskTolerance: domain.RiskToleranceModerate,
		}

		// With no updates, UpdateColumns shouldn't be called
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(existingProfile, nil).Once()

		result, err := service.UpdateProfile(ctx, userID.String(), req)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		mockRepo.AssertExpectations(t)
	})
}

// TestProfileBusinessLogic tests domain business logic
func TestProfileBusinessLogic(t *testing.T) {
	t.Run("Risk level calculation", func(t *testing.T) {
		profile := &domain.UserProfile{
			RiskTolerance: domain.RiskToleranceConservative,
		}
		assert.Equal(t, 1, profile.GetRiskLevel())
		assert.True(t, profile.IsConservativeInvestor())
		assert.False(t, profile.IsAggressiveInvestor())

		profile.RiskTolerance = domain.RiskToleranceModerate
		assert.Equal(t, 2, profile.GetRiskLevel())

		profile.RiskTolerance = domain.RiskToleranceAggressive
		assert.Equal(t, 3, profile.GetRiskLevel())
		assert.True(t, profile.IsAggressiveInvestor())
	})

	t.Run("Emergency fund check", func(t *testing.T) {
		profile := &domain.UserProfile{}
		assert.False(t, profile.HasEmergencyFund())

		months := 3.0
		profile.EmergencyFundMonths = &months
		assert.True(t, profile.HasEmergencyFund())

		zeroMonths := 0.0
		profile.EmergencyFundMonths = &zeroMonths
		assert.False(t, profile.HasEmergencyFund())
	})

	t.Run("Onboarding status", func(t *testing.T) {
		profile := &domain.UserProfile{
			OnboardingCompleted: false,
		}
		assert.False(t, profile.IsOnboardingComplete())

		profile.OnboardingCompleted = true
		assert.True(t, profile.IsOnboardingComplete())
	})
}

// TestProfileDefaults tests default values
func TestProfileDefaults(t *testing.T) {
	t.Run("Default profile has correct defaults", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)
		ctx := context.Background()

		userID := uuid.New()

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound).Once()

		mockRepo.On("Create", ctx, mock.MatchedBy(func(p *domain.UserProfile) bool {
			return p.RiskTolerance == domain.RiskToleranceModerate &&
				p.InvestmentHorizon == domain.InvestmentHorizonMedium &&
				p.InvestmentExperience == domain.InvestmentExperienceBeginner &&
				p.BudgetMethod == domain.BudgetMethodCustom &&
				p.CurrencyPrimary == "VND" &&
				p.CurrencySecondary == "USD" &&
				p.OnboardingCompleted == false
		})).Return(nil)

		// Mock retrieval after creation
		mockRepo.On("GetByUserID", ctx, userID.String()).Return(&domain.UserProfile{
			UserID:               userID,
			RiskTolerance:        domain.RiskToleranceModerate,
			InvestmentHorizon:    domain.InvestmentHorizonMedium,
			InvestmentExperience: domain.InvestmentExperienceBeginner,
			BudgetMethod:         domain.BudgetMethodCustom,
			CurrencyPrimary:      "VND",
			CurrencySecondary:    "USD",
			OnboardingCompleted:  false,
		}, nil).Once()

		result, err := service.CreateDefaultProfile(ctx, userID.String())

		assert.NoError(t, err)
		assert.Equal(t, domain.RiskToleranceModerate, result.RiskTolerance)
		assert.Equal(t, domain.InvestmentHorizonMedium, result.InvestmentHorizon)
		assert.Equal(t, domain.InvestmentExperienceBeginner, result.InvestmentExperience)
		assert.Equal(t, "VND", result.CurrencyPrimary)
		assert.Equal(t, "USD", result.CurrencySecondary)
		assert.False(t, result.OnboardingCompleted)

		mockRepo.AssertExpectations(t)
	})
}

func TestProfileService_AppErrorPassThrough(t *testing.T) {
	ctx := context.Background()

	t.Run("CreateProfile returns repository app error directly", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)
		userID := uuid.New()
		req := dto.CreateProfileRequest{}
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).WithDetails("field", "user_id")

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, shared.ErrProfileNotFound).Once()
		mockRepo.On("Create", ctx, mock.AnythingOfType("*domain.UserProfile")).Return(appErr).Once()

		result, err := service.CreateProfile(ctx, userID.String(), req)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})

	t.Run("UpdateProfile returns repository app error directly", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)
		userID := uuid.New()
		occupation := "Engineer"
		req := dto.UpdateProfileRequest{Occupation: &occupation}
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).WithDetails("field", "occupation")

		mockRepo.On("UpdateColumns", ctx, userID.String(), mock.Anything).Return(appErr).Once()

		result, err := service.UpdateProfile(ctx, userID.String(), req)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})

	t.Run("GetProfile returns repository app error directly", func(t *testing.T) {
		mockRepo := new(MockProfileRepository)
		service := NewService(mockRepo)
		userID := uuid.New()
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).WithDetails("field", "user_id")

		mockRepo.On("GetByUserID", ctx, userID.String()).Return(nil, appErr).Once()

		result, err := service.GetProfile(ctx, userID.String())
		assert.Nil(t, result)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})
}
