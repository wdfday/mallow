package service

import (
	"context"
	"log/slog"

	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/helper"
	"mallow/identity/internal/module/auth/repository"
	notificationservice "mallow/identity/internal/module/notification/service"
	profileservice "mallow/identity/internal/module/profile/service"
	userservice "mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"

	"golang.org/x/crypto/bcrypt"
)

// PasswordService handles password hashing, validation, and management operations
type PasswordService struct {
	cost           int // bcrypt cost factor
	userService    userservice.IUserService
	profileService profileservice.Service
	tokenRepo      repository.TokenRepository
	tokenGenerator ITokenService
	emailService   notificationservice.EmailService
	logger         *slog.Logger
}

// NewPasswordService creates a new password service
func NewPasswordService(
	userService userservice.IUserService,
	profileService profileservice.Service,
	tokenRepo repository.TokenRepository,
	tokenGenerator ITokenService,
	emailService notificationservice.EmailService,
	logger *slog.Logger,
) *PasswordService {
	return &PasswordService{
		cost:           bcrypt.DefaultCost,
		userService:    userService,
		profileService: profileService,
		tokenRepo:      tokenRepo,
		tokenGenerator: tokenGenerator,
		emailService:   emailService,
		logger:         logger,
	}
}

func (s *PasswordService) HashPassword(password string) (string, error) {
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}

func (s *PasswordService) VerifyPassword(hashedPassword, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
}

func (s *PasswordService) IsValidPassword(password string) bool {
	return helper.IsPasswordStrong(password)
}

func (s *PasswordService) ValidatePasswordStrength(password string) []string {
	return helper.PasswordValidationErrors(password)
}

// ChangePassword allows authenticated users to update their password
func (s *PasswordService) ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordRequest) error {
	user, err := s.userService.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := s.VerifyPassword(user.Password, req.CurrentPassword); err != nil {
		return shared.ErrUnauthorized.WithDetails("message", "current password is incorrect")
	}

	if !s.IsValidPassword(req.NewPassword) {
		return shared.ErrBadRequest.WithDetails("message", "password does not meet security requirements")
	}

	hashedPassword, err := s.HashPassword(req.NewPassword)
	if err != nil {
		return shared.ErrInternal.WithError(err)
	}

	return s.userService.UpdatePassword(ctx, userID, hashedPassword)
}

// ForgotPassword initiates the password reset flow by issuing a reset token
func (s *PasswordService) ForgotPassword(ctx context.Context, email, ipAddress, userAgent string) error {
	user, err := s.userService.GetByEmail(ctx, email)
	if err != nil {
		// Do not leak whether the user exists
		return nil
	}

	if err := s.tokenRepo.DeleteByUserIDAndType(ctx, user.ID.String(), string(domain.TokenTypePasswordReset)); err != nil {
		return shared.ErrInternal.WithError(err)
	}

	tokenStr, err := s.tokenGenerator.GenerateTokenWithPrefix("pwd_reset")
	if err != nil {
		return shared.ErrInternal.WithError(err)
	}

	token := &domain.VerificationToken{
		UserID:    user.ID.String(),
		Token:     tokenStr,
		Type:      string(domain.TokenTypePasswordReset),
		ExpiresAt: s.tokenGenerator.GetPasswordResetExpiry(),
		IPAddress: helper.StringPtr(ipAddress),
		UserAgent: helper.StringPtr(userAgent),
	}

	if err := s.tokenRepo.Create(ctx, token); err != nil {
		return shared.ErrInternal.WithError(err)
	}

	if s.emailService != nil {
		fullName := ""
		if s.profileService != nil {
			if p, _ := s.profileService.GetProfile(ctx, user.ID.String()); p != nil {
				fullName = p.FullName
			}
		}
		if err := s.emailService.SendPasswordResetEmail(user.Email, fullName, tokenStr); err != nil {
			s.logger.Error("failed to send password reset email", "email", user.Email, "err", err)
		}
	}

	return nil
}

// ResetPassword validates a reset token and updates the user's password.
// In Redis, a missing token means never issued, already used, or expired.
func (s *PasswordService) ResetPassword(ctx context.Context, tokenStr, newPassword string) error {
	token, err := s.tokenRepo.GetByToken(ctx, tokenStr)
	if err != nil {
		if err == shared.ErrTokenNotFound {
			return shared.ErrTokenNotFound.WithDetails("type", "password reset")
		}
		return shared.ErrInternal.WithError(err)
	}

	if token.Type != string(domain.TokenTypePasswordReset) {
		return shared.ErrTokenInvalid.WithDetails("message", "wrong token type")
	}

	if token.IsExpired() {
		return shared.ErrTokenExpired
	}

	if !s.IsValidPassword(newPassword) {
		return shared.ErrBadRequest.WithDetails("message", "password does not meet security requirements")
	}

	hashedPassword, err := s.HashPassword(newPassword)
	if err != nil {
		return shared.ErrInternal.WithError(err)
	}

	if err := s.userService.UpdatePassword(ctx, token.UserID, hashedPassword); err != nil {
		return err
	}

	// Invalidate the token so it cannot be reused.
	if err := s.tokenRepo.MarkAsUsed(ctx, tokenStr); err != nil {
		return shared.ErrInternal.WithError(err)
	}

	_ = s.userService.ResetLoginAttempts(ctx, token.UserID)

	return nil
}
