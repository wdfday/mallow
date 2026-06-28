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
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ==================== Tests ====================

// TestSendVerificationEmail tests the SendVerificationEmail method
func TestSendVerificationEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Send verification email", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)
		mockTokenService := new(MockTokenService)
		mockEmailService := new(MockEmailService)

		service := NewVerificationService(
			mockTokenRepo,
			mockUserService,
			nil,
			mockTokenService,
			mockEmailService,
			slog.Default(),
		)

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         "test@example.com",
			EmailVerified: false,
		}

		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)
		mockTokenRepo.On("DeleteByUserIDAndType", ctx, userID.String(), string(authdomain.TokenTypeEmailVerification)).Return(nil)
		mockTokenService.On("GenerateTokenWithPrefix", "email_verify").Return("email_verify_token123", nil)
		mockTokenRepo.On("Create", ctx, mock.AnythingOfType("*domain.VerificationToken")).Return(nil)
		mockEmailService.On("SendVerificationEmail", user.Email, "", "email_verify_token123").Return(nil)

		err := service.SendVerificationEmail(ctx, userID.String(), "192.168.1.1", "test-agent")

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
		mockTokenRepo.AssertExpectations(t)
		mockTokenService.AssertExpectations(t)
	})

	t.Run("Error - User not found", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := NewVerificationService(
			nil,
			mockUserService,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		mockUserService.On("GetByID", ctx, userID.String()).Return(nil, shared.ErrUserNotFound)

		err := service.SendVerificationEmail(ctx, userID.String(), "192.168.1.1", "test-agent")

		assert.Error(t, err)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Email already verified", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := NewVerificationService(
			nil,
			mockUserService,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         "test@example.com",
			EmailVerified: true,
		}

		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)

		err := service.SendVerificationEmail(ctx, userID.String(), "192.168.1.1", "test-agent")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Bad request")
		mockUserService.AssertExpectations(t)
	})
}

// TestVerifyEmail tests the VerifyEmail method
func TestVerifyEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Verify email with valid token", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			mockUserService,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		tokenStr := "email_verify_token123"

		token := &authdomain.VerificationToken{
			UserID:    userID.String(),
			Token:     tokenStr,
			Type:      string(authdomain.TokenTypeEmailVerification),
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(token, nil)
		mockUserService.On("MarkEmailVerified", ctx, userID.String(), mock.AnythingOfType("time.Time")).Return(nil)
		mockTokenRepo.On("MarkAsUsed", ctx, tokenStr).Return(nil)

		err := service.VerifyEmail(ctx, tokenStr)

		assert.NoError(t, err)
		mockTokenRepo.AssertExpectations(t)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Token not found", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			nil,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		tokenStr := "invalid_token"
		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(nil, shared.ErrTokenNotFound)

		err := service.VerifyEmail(ctx, tokenStr)

		assert.Error(t, err)
		mockTokenRepo.AssertExpectations(t)
	})

	t.Run("Error - Wrong token type", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			nil,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		tokenStr := "password_reset_token"

		token := &authdomain.VerificationToken{
			UserID:    userID.String(),
			Token:     tokenStr,
			Type:      string(authdomain.TokenTypePasswordReset), // Wrong type
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(token, nil)

		err := service.VerifyEmail(ctx, tokenStr)

		assert.Error(t, err)
		mockTokenRepo.AssertExpectations(t)
	})

	// "Token already used" is not a separate case in Redis: MarkAsUsed deletes the key,
	// so a reuse attempt returns ErrTokenNotFound (same as "Error - Token not found").

	t.Run("Error - Token expired", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			nil,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		tokenStr := "expired_token"

		token := &authdomain.VerificationToken{
			UserID:    userID.String(),
			Token:     tokenStr,
			Type:      string(authdomain.TokenTypeEmailVerification),
			ExpiresAt: time.Now().Add(-1 * time.Hour), // Expired
		}

		mockTokenRepo.On("GetByToken", ctx, tokenStr).Return(token, nil)

		err := service.VerifyEmail(ctx, tokenStr)

		assert.Error(t, err)
		mockTokenRepo.AssertExpectations(t)
	})
}

// TestResendVerificationEmail tests the ResendVerificationEmail method
func TestResendVerificationEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Resend verification email", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockTokenRepo := new(MockTokenRepository)
		mockTokenService := new(MockTokenService)
		mockEmailService := new(MockEmailService)

		service := NewVerificationService(
			mockTokenRepo,
			mockUserService,
			nil,
			mockTokenService,
			mockEmailService,
			slog.Default(),
		)

		userID := uuid.New()
		email := "test@example.com"
		user := &userdomain.User{
			ID:            userID,
			Email:         email,
			EmailVerified: false,
		}

		mockUserService.On("GetByEmail", ctx, email).Return(user, nil)
		mockTokenRepo.On("DeleteByUserIDAndType", ctx, userID.String(), string(authdomain.TokenTypeEmailVerification)).Return(nil)
		mockTokenService.On("GenerateTokenWithPrefix", "email_verify").Return("new_verify_token", nil)
		mockTokenRepo.On("Create", ctx, mock.AnythingOfType("*domain.VerificationToken")).Return(nil)
		mockEmailService.On("SendVerificationEmail", email, "", "new_verify_token").Return(nil)

		err := service.ResendVerificationEmail(ctx, email, "192.168.1.1", "test-agent")

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
		mockTokenRepo.AssertExpectations(t)
		mockTokenService.AssertExpectations(t)
	})

	t.Run("Success - User not found (no error leaked)", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := NewVerificationService(
			nil,
			mockUserService,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		email := "notfound@example.com"
		mockUserService.On("GetByEmail", ctx, email).Return(nil, shared.ErrUserNotFound)

		// Should not return error to avoid leaking user existence
		err := service.ResendVerificationEmail(ctx, email, "192.168.1.1", "test-agent")

		assert.NoError(t, err)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Email already verified", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := NewVerificationService(
			nil,
			mockUserService,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		userID := uuid.New()
		email := "verified@example.com"
		user := &userdomain.User{
			ID:            userID,
			Email:         email,
			EmailVerified: true,
		}

		mockUserService.On("GetByEmail", ctx, email).Return(user, nil)

		err := service.ResendVerificationEmail(ctx, email, "192.168.1.1", "test-agent")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Bad request")
		mockUserService.AssertExpectations(t)
	})
}

// TestCleanupExpiredTokens tests the CleanupExpiredTokens method
func TestCleanupExpiredTokens(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Cleanup expired tokens", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			nil,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		mockTokenRepo.On("DeleteExpired", ctx).Return(nil)

		err := service.CleanupExpiredTokens(ctx)

		assert.NoError(t, err)
		mockTokenRepo.AssertExpectations(t)
	})

	t.Run("Error - Repository error", func(t *testing.T) {
		mockTokenRepo := new(MockTokenRepository)

		service := NewVerificationService(
			mockTokenRepo,
			nil,
			nil,
			nil,
			nil,
			slog.Default(),
		)

		mockTokenRepo.On("DeleteExpired", ctx).Return(errors.New("database error"))

		err := service.CleanupExpiredTokens(ctx)

		assert.Error(t, err)
		mockTokenRepo.AssertExpectations(t)
	})
}
