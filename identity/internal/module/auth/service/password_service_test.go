package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	authdomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/dto"
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ==================== Mocks ====================

type MockTokenRepository struct {
	mock.Mock
}

func (m *MockTokenRepository) Create(ctx context.Context, token *authdomain.VerificationToken) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockTokenRepository) GetByToken(ctx context.Context, tokenStr string) (*authdomain.VerificationToken, error) {
	args := m.Called(ctx, tokenStr)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*authdomain.VerificationToken), args.Error(1)
}

func (m *MockTokenRepository) DeleteByUserIDAndType(ctx context.Context, userID, tokenType string) error {
	args := m.Called(ctx, userID, tokenType)
	return args.Error(0)
}

func (m *MockTokenRepository) DeleteExpired(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockTokenRepository) MarkAsUsed(ctx context.Context, tokenID string) error {
	args := m.Called(ctx, tokenID)
	return args.Error(0)
}

type MockTokenService struct {
	mock.Mock
}

func (m *MockTokenService) GenerateToken() (string, error) {
	args := m.Called()
	return args.String(0), args.Error(1)
}

func (m *MockTokenService) GenerateTokenWithPrefix(prefix string) (string, error) {
	args := m.Called(prefix)
	return args.String(0), args.Error(1)
}

func (m *MockTokenService) GetEmailVerificationExpiry() time.Time {
	return time.Now().Add(24 * time.Hour)
}

func (m *MockTokenService) GetPasswordResetExpiry() time.Time {
	return time.Now().Add(1 * time.Hour)
}

func (m *MockTokenService) ValidateUUID(id string) bool {
	return true
}

type MockEmailService struct {
	mock.Mock
}

func (m *MockEmailService) SendPasswordResetEmail(to, name, token string) error {
	args := m.Called(to, name, token)
	return args.Error(0)
}

func (m *MockEmailService) SendVerificationEmail(to, name, verificationURL string) error {
	return nil
}

func (m *MockEmailService) SendWelcomeEmail(to, name, email string) error {
	return nil
}
func (m *MockEmailService) SendBudgetAlert(to, name, category string, budgetAmount, spentAmount, threshold float64) error {
	return nil
}

func (m *MockEmailService) SendCustomEmail(to, subject, templateFileName string, data map[string]interface{}) error {
	return nil
}

func (m *MockEmailService) SendEmailFromTemplate(to, subject, templateName string, data map[string]interface{}) error {
	return nil
}

func (m *MockEmailService) SendGoalAchievedEmail(to, name, goalName string, targetAmount, currentAmount float64, deadline, achievedAt time.Time) error {
	return nil
}

func (m *MockEmailService) SendMonthlySummary(to, name, month string, year int, summary map[string]interface{}) error {
	return nil
}

// ==================== Tests ====================

// TestHashAndVerifyPassword tests password hashing and verification
func TestHashAndVerifyPassword(t *testing.T) {
	service := &PasswordService{
		cost:   10, // bcrypt.DefaultCost
		logger: slog.Default(),
	}

	t.Run("Success - Hash and verify valid password", func(t *testing.T) {
		password := "Password123!"

		hashedPassword, err := service.HashPassword(password)

		assert.NoError(t, err)
		assert.NotEmpty(t, hashedPassword)
		assert.NotEqual(t, password, hashedPassword)

		// Verify correct password
		err = service.VerifyPassword(hashedPassword, password)
		assert.NoError(t, err)
	})

	t.Run("Error - Verify incorrect password", func(t *testing.T) {
		password := "Password123!"
		wrongPassword := "WrongPassword!"

		hashedPassword, _ := service.HashPassword(password)

		err := service.VerifyPassword(hashedPassword, wrongPassword)
		assert.Error(t, err)
	})

	t.Run("Consistent hashing", func(t *testing.T) {
		password := "Password123!"

		hash1, _ := service.HashPassword(password)
		hash2, _ := service.HashPassword(password)

		// Bcrypt generates different hashes each time (salt is random)
		assert.NotEqual(t, hash1, hash2)

		// But both should verify correctly
		assert.NoError(t, service.VerifyPassword(hash1, password))
		assert.NoError(t, service.VerifyPassword(hash2, password))
	})
}

// TestPasswordStrengthValidation tests password strength validation
func TestPasswordStrengthValidation(t *testing.T) {
	service := &PasswordService{
		logger: slog.Default(),
	}

	t.Run("Valid strong password", func(t *testing.T) {
		validPasswords := []string{
			"Password123!",
			"SecureP@ss1",
			"MyP@ssw0rd",
		}

		for _, password := range validPasswords {
			assert.True(t, service.IsValidPassword(password),
				"Password %s should be valid", password)

			errors := service.ValidatePasswordStrength(password)
			assert.Empty(t, errors,
				"Password %s should have no validation errors", password)
		}
	})

	t.Run("Invalid weak passwords", func(t *testing.T) {
		weakPasswords := []string{
			"short",       // Too short
			"password",    // No uppercase, no numbers, no special chars
			"PASSWORD123", // No lowercase, no special chars
			"Password",    // No numbers, no special chars
			"Password123", // No special chars
		}

		for _, password := range weakPasswords {
			errors := service.ValidatePasswordStrength(password)
			assert.NotEmpty(t, errors,
				"Password %s should have validation errors", password)
		}
	})
}

// TestChangePassword tests password change functionality
func TestChangePassword(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Change password", func(t *testing.T) {
		mockUserService := new(MockUserService)
		service := &PasswordService{
			cost:        10,
			userService: mockUserService,
			logger:      slog.Default(),
		}

		userID := uuid.New()
		currentPassword := "OldPassword123!"
		newPassword := "NewPassword123!"

		// Hash current password
		hashedCurrentPassword, _ := service.HashPassword(currentPassword)

		user := &userdomain.User{
			ID:       userID,
			Email:    "test@example.com",
			Password: hashedCurrentPassword,
		}

		req := dto.ChangePasswordRequest{
			CurrentPassword: currentPassword,
			NewPassword:     newPassword,
		}

		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)
		mockUserService.On("UpdatePassword", ctx, userID.String(), mock.MatchedBy(func(hashedPwd string) bool {
			// Verify the new password is hashed and different from old
			return hashedPwd != hashedCurrentPassword &&
				service.VerifyPassword(hashedPwd, newPassword) == nil
		})).Return(nil)

		err := service.ChangePassword(ctx, userID.String(), req)

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Current password incorrect", func(t *testing.T) {
		mockUserService := new(MockUserService)
		service := &PasswordService{
			cost:        10,
			userService: mockUserService,
			logger:      slog.Default(),
		}

		userID := uuid.New()
		currentPassword := "CurrentPassword123!"
		wrongPassword := "WrongPassword123!"
		newPassword := "NewPassword123!"

		hashedCurrentPassword, _ := service.HashPassword(currentPassword)

		user := &userdomain.User{
			ID:       userID,
			Email:    "test@example.com",
			Password: hashedCurrentPassword,
		}

		req := dto.ChangePasswordRequest{
			CurrentPassword: wrongPassword,
			NewPassword:     newPassword,
		}

		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)

		err := service.ChangePassword(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - New password too weak", func(t *testing.T) {
		mockUserService := new(MockUserService)
		service := &PasswordService{
			cost:        10,
			userService: mockUserService,
			logger:      slog.Default(),
		}

		userID := uuid.New()
		currentPassword := "CurrentPassword123!"
		weakNewPassword := "weak"

		hashedCurrentPassword, _ := service.HashPassword(currentPassword)

		user := &userdomain.User{
			ID:       userID,
			Email:    "test@example.com",
			Password: hashedCurrentPassword,
		}

		req := dto.ChangePasswordRequest{
			CurrentPassword: currentPassword,
			NewPassword:     weakNewPassword,
		}

		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)

		err := service.ChangePassword(ctx, userID.String(), req)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Bad request")

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - User not found", func(t *testing.T) {
		mockUserService := new(MockUserService)
		service := &PasswordService{
			cost:        10,
			userService: mockUserService,
			logger:      slog.Default(),
		}

		userID := uuid.New()
		req := dto.ChangePasswordRequest{
			CurrentPassword: "CurrentPassword123!",
			NewPassword:     "NewPassword123!",
		}

		mockUserService.On("GetByID", ctx, userID.String()).
			Return(nil, shared.ErrUserNotFound)

		err := service.ChangePassword(ctx, userID.String(), req)

		assert.Error(t, err)

		mockUserService.AssertExpectations(t)
	})
}

// TestForgotPassword tests forgot password functionality
func TestForgotPassword(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Generate password reset token", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)
		mockTokenService := new(MockTokenService)
		mockEmailService := new(MockEmailService)

		service := &PasswordService{
			userService:    mockUserService,
			tokenRepo:      mockTokenRepo,
			tokenGenerator: mockTokenService,
			emailService:   mockEmailService,
			logger:         slog.Default(),
		}

		email := "test@example.com"
		userID := uuid.New()

		user := &userdomain.User{
			ID:    userID,
			Email: email,
		}

		mockUserService.On("GetByEmail", ctx, email).Return(user, nil)
		mockTokenRepo.On("DeleteByUserIDAndType", ctx, userID.String(), string(authdomain.TokenTypePasswordReset)).Return(nil)
		mockTokenService.On("GenerateTokenWithPrefix", "pwd_reset").Return("reset_token_123", nil)
		mockTokenRepo.On("Create", ctx, mock.MatchedBy(func(token *authdomain.VerificationToken) bool {
			return token.UserID == userID.String() &&
				token.Type == string(authdomain.TokenTypePasswordReset) &&
				token.Token == "reset_token_123"
		})).Return(nil)
		mockEmailService.On("SendPasswordResetEmail", email, mock.Anything, "reset_token_123").Return(nil)

		err := service.ForgotPassword(ctx, email, "192.168.1.1", "test-agent")

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
		mockTokenRepo.AssertExpectations(t)
		mockTokenService.AssertExpectations(t)
		mockEmailService.AssertExpectations(t)
	})

	t.Run("Success - User not found (no error leaked)", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := &PasswordService{
			userService: mockUserService,
			logger:      slog.Default(),
		}

		email := "notfound@example.com"

		mockUserService.On("GetByEmail", ctx, email).Return(nil, shared.ErrUserNotFound)

		// Should not return error to avoid leaking user existence
		err := service.ForgotPassword(ctx, email, "192.168.1.1", "test-agent")

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Token generation fails", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)
		mockTokenService := new(MockTokenService)

		service := &PasswordService{
			userService:    mockUserService,
			tokenRepo:      mockTokenRepo,
			tokenGenerator: mockTokenService,
			logger:         slog.Default(),
		}

		email := "test@example.com"
		userID := uuid.New()

		user := &userdomain.User{
			ID:    userID,
			Email: email,
		}

		mockUserService.On("GetByEmail", ctx, email).Return(user, nil)
		mockTokenRepo.On("DeleteByUserIDAndType", ctx, userID.String(), string(authdomain.TokenTypePasswordReset)).Return(nil)
		mockTokenService.On("GenerateTokenWithPrefix", "pwd_reset").Return("", errors.New("token generation failed"))

		err := service.ForgotPassword(ctx, email, "192.168.1.1", "test-agent")

		assert.Error(t, err)
		mockUserService.AssertExpectations(t)
		mockTokenRepo.AssertExpectations(t)
		mockTokenService.AssertExpectations(t)
	})
}

// TestResetPassword tests password reset functionality
func TestResetPassword(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Reset password with valid token", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)

		service := &PasswordService{
			cost:        10,
			userService: mockUserService,
			tokenRepo:   mockTokenRepo,
			logger:      slog.Default(),
		}

		userID := uuid.New()
		tokenStr := "reset_token_123"
		newPassword := "NewSecurePassword123!"

		token := &authdomain.VerificationToken{
			Token:     tokenStr,
			UserID:    userID.String(),
			Type:      string(authdomain.TokenTypePasswordReset),
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		mockTokenRepo.On("GetByToken", mock.Anything, tokenStr).Return(token, nil)
		mockUserService.On("UpdatePassword", mock.Anything, userID.String(), mock.Anything).Return(nil)
		mockTokenRepo.On("MarkAsUsed", mock.Anything, tokenStr).Return(nil)
		mockUserService.On("ResetLoginAttempts", mock.Anything, userID.String()).Return(nil)

		err := service.ResetPassword(ctx, tokenStr, newPassword)

		assert.NoError(t, err)
		mockTokenRepo.AssertExpectations(t)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Token not found", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := &PasswordService{
			cost:      10,
			tokenRepo: mockTokenRepo,
			logger:    slog.Default(),
		}

		tokenStr := "invalid_token"
		newPassword := "NewSecurePassword123!"

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(nil, shared.ErrTokenNotFound)

		err := service.ResetPassword(ctx, tokenStr, newPassword)

		assert.Error(t, err)
		mockTokenRepo.AssertExpectations(t)
	})

	t.Run("Error - Token expired", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := &PasswordService{
			cost:      10,
			tokenRepo: mockTokenRepo,
			logger:    slog.Default(),
		}

		userID := uuid.New()
		tokenStr := "expired_token"
		newPassword := "NewSecurePassword123!"

		// Token expired 1 hour ago
		token := &authdomain.VerificationToken{
			Token:     tokenStr,
			UserID:    userID.String(),
			Type:      string(authdomain.TokenTypePasswordReset),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
		}

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(token, nil)

		err := service.ResetPassword(ctx, tokenStr, newPassword)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "expired")

		mockTokenRepo.AssertExpectations(t)
	})

	// "Token already used" is not a separate case in Redis: MarkAsUsed deletes the key,
	// so a reuse attempt returns ErrTokenNotFound (same as "Error - Token not found").

	t.Run("Error - New password too weak", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := &PasswordService{
			cost:      10,
			tokenRepo: mockTokenRepo,
			logger:    slog.Default(),
		}

		userID := uuid.New()
		tokenStr := "reset_token_123"
		weakPassword := "weak"

		token := &authdomain.VerificationToken{
			Token:     tokenStr,
			UserID:    userID.String(),
			Type:      string(authdomain.TokenTypePasswordReset),
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(token, nil)

		err := service.ResetPassword(ctx, tokenStr, weakPassword)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Bad request")

		mockTokenRepo.AssertExpectations(t)
	})
}
