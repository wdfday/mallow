package service

import (
	"context"
	"log/slog"
	"time"

	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/helper"
	"mallow/identity/internal/module/auth/repository"
	notificationservice "mallow/identity/internal/module/notification/service"
	"mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"
)

// VerificationService handles the full email verification lifecycle
type VerificationService struct {
	tokenRepo    repository.TokenRepository
	userService  service.IUserService
	tokenGen     ITokenService
	emailService notificationservice.EmailService
	logger       *slog.Logger
}

// NewVerificationService creates a new verification service
func NewVerificationService(
	tokenRepo repository.TokenRepository,
	userService service.IUserService,
	tokenGen ITokenService,
	emailService notificationservice.EmailService,
	logger *slog.Logger,
) *VerificationService {
	return &VerificationService{
		tokenRepo:    tokenRepo,
		userService:  userService,
		tokenGen:     tokenGen,
		emailService: emailService,
		logger:       logger,
	}
}

// SendVerificationEmail generates a verification token and emails it to the user
func (s *VerificationService) SendVerificationEmail(ctx context.Context, userID, ipAddress, userAgent string) error {
	user, err := s.userService.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.EmailVerified {
		return shared.ErrBadRequest.WithDetails("message", "email already verified")
	}

	return s.dispatchVerificationEmail(ctx, user.ID.String(), user.Email, user.FullName, ipAddress, userAgent)
}

// VerifyEmail verifies a user's email using the token.
// In Redis, a missing token means it was never issued, already used, or expired —
// all three are treated as ErrTokenNotFound.
func (s *VerificationService) VerifyEmail(ctx context.Context, tokenStr string) error {
	token, err := s.tokenRepo.GetByToken(ctx, tokenStr)
	if err != nil {
		if err == shared.ErrTokenNotFound {
			return shared.ErrTokenNotFound.WithDetails("type", "email verification")
		}
		return shared.ErrInternal.WithError(err)
	}

	if token.Type != string(domain.TokenTypeEmailVerification) {
		return shared.ErrTokenInvalid.WithDetails("message", "wrong token type")
	}

	if token.IsExpired() {
		return shared.ErrTokenExpired
	}

	now := time.Now()
	if err := s.userService.MarkEmailVerified(ctx, token.UserID, now); err != nil {
		return err
	}

	// Invalidate the token so it cannot be reused.
	if err := s.tokenRepo.MarkAsUsed(ctx, tokenStr); err != nil {
		return shared.ErrInternal.WithError(err)
	}

	return nil
}

// ResendVerificationEmail resends a verification token to the given email
func (s *VerificationService) ResendVerificationEmail(ctx context.Context, email, ipAddress, userAgent string) error {
	user, err := s.userService.GetByEmail(ctx, email)
	if err != nil {
		// Avoid leaking existence of the email address
		return nil
	}

	if user.EmailVerified {
		return shared.ErrBadRequest.WithDetails("message", "email already verified")
	}

	return s.dispatchVerificationEmail(ctx, user.ID.String(), user.Email, user.FullName, ipAddress, userAgent)
}

// CleanupExpiredTokens is a no-op: Redis TTL handles expiry automatically.
func (s *VerificationService) CleanupExpiredTokens(ctx context.Context) error {
	return s.tokenRepo.DeleteExpired(ctx)
}

func (s *VerificationService) dispatchVerificationEmail(ctx context.Context, userID, email, fullName, ipAddress, userAgent string) error {
	token, err := s.issueEmailVerificationToken(ctx, userID, helper.StringPtr(ipAddress), helper.StringPtr(userAgent))
	if err != nil {
		return err
	}

	s.sendVerificationEmail(email, fullName, token)
	return nil
}

func (s *VerificationService) issueEmailVerificationToken(ctx context.Context, userID string, ipAddress, userAgent *string) (string, error) {
	if err := s.tokenRepo.DeleteByUserIDAndType(ctx, userID, string(domain.TokenTypeEmailVerification)); err != nil {
		return "", shared.ErrInternal.WithError(err)
	}

	tokenStr, err := s.tokenGen.GenerateTokenWithPrefix("email_verify")
	if err != nil {
		return "", shared.ErrInternal.WithError(err)
	}

	token := &domain.VerificationToken{
		UserID:    userID,
		Token:     tokenStr,
		Type:      string(domain.TokenTypeEmailVerification),
		ExpiresAt: s.tokenGen.GetEmailVerificationExpiry(),
		IPAddress: ipAddress,
		UserAgent: userAgent,
	}

	if err := s.tokenRepo.Create(ctx, token); err != nil {
		return "", shared.ErrInternal.WithError(err)
	}

	return tokenStr, nil
}

func (s *VerificationService) sendVerificationEmail(email, fullName, token string) {
	if s.emailService == nil {
		return
	}

	if err := s.emailService.SendVerificationEmail(email, fullName, token); err != nil {
		s.logger.Error("failed to send verification email", "email", email, "err", err)
	}
}
