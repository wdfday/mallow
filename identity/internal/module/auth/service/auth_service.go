package service

import (
	"context"
	"fmt"
	"log/slog"
	"mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/repository"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/module/user/service"
	"time"

	"mallow/identity/internal/config"
	"mallow/identity/internal/shared"

	"github.com/google/uuid"
)

// Service handles authentication operations
type Service struct {
	userService        service.IUserService
	jwtService         IJWTService
	passwordService    IPasswordService
	googleOAuthService *GoogleOAuthService
	tokenBlacklistRepo repository.ITokenBlacklistRepository
	config             *config.Config
	logger             *slog.Logger
}

// NewService creates a new auth service
func NewService(
	userService service.IUserService,
	jwtService IJWTService,
	passwordService IPasswordService,
	googleOAuthService *GoogleOAuthService,
	tokenBlacklistRepo repository.ITokenBlacklistRepository,
	cfg *config.Config,
	logger *slog.Logger,
) *Service {
	return &Service{
		userService:        userService,
		jwtService:         jwtService,
		passwordService:    passwordService,
		googleOAuthService: googleOAuthService,
		tokenBlacklistRepo: tokenBlacklistRepo,
		config:             cfg,
		logger:             logger,
	}
}

// Register registers a new user
func (s *Service) Register(ctx context.Context, req dto.RegisterRequest) (*dto.AuthResult, error) {
	// Check if user already exists
	exists, err := s.userService.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}
	if exists {
		return nil, shared.ErrConflict.WithDetails("field", "email")
	}

	// Hash password
	hashedPassword, err := s.passwordService.HashPassword(req.Password)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	// Create user
	user := &domain.User{
		Email:    req.Email,
		Password: hashedPassword,
		FullName: req.FullName,
		Role:     domain.UserRoleUser,
		Status:   domain.UserStatusPendingVerification,
	}

	if req.Phone != "" {
		user.PhoneNumber = &req.Phone
	}

	createdUser, err := s.userService.Create(ctx, user)
	if err != nil {
		return nil, err
	}

	// Generate tokens
	sid := uuid.New().String()
	accessToken, expiresAt, err := s.jwtService.GenerateAccessToken(
		createdUser.ID.String(),
		createdUser.Email,
		sid,
		createdUser.Role,
	)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	refreshToken, _, err := s.jwtService.GenerateRefreshToken(createdUser.ID.String(), sid)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	return &dto.AuthResult{
		User:         createdUser,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		SessionID:    sid,
	}, nil
}

// Login authenticates a user with email and password
func (s *Service) Login(ctx context.Context, req dto.LoginRequest) (*dto.AuthResult, error) {
	// Get user by email
	user, err := s.userService.GetByEmail(ctx, req.Email)
	if err != nil {
		return nil, shared.ErrUnauthorized.WithDetails("message", "invalid credentials")
	}

	// Check if account is locked
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		return nil, shared.ErrUnauthorized.WithDetails("message", "account is locked")
	}

	// Verify password
	if err := s.passwordService.VerifyPassword(user.Password, req.Password); err != nil {
		// Increment login attempts
		_ = s.userService.IncLoginAttempts(ctx, user.ID.String())

		// Lock account after 5 failed attempts
		if user.LoginAttempts >= 4 {
			lockUntil := time.Now().Add(15 * time.Minute)
			_ = s.userService.SetLockedUntil(ctx, user.ID.String(), &lockUntil)
		}

		return nil, shared.ErrUnauthorized.WithDetails("message", "invalid credentials")
	}

	// Check account status
	if user.Status == domain.UserStatusSuspended {
		return nil, shared.ErrUnauthorized.WithDetails("message", "account is suspended")
	}

	// Reset login attempts
	_ = s.userService.ResetLoginAttempts(ctx, user.ID.String())

	// Update last login
	ip := req.IP
	_ = s.userService.UpdateLastLogin(ctx, user.ID.String(), time.Now(), &ip)

	// Generate tokens
	sid := uuid.New().String()
	accessToken, expiresAt, err := s.jwtService.GenerateAccessToken(
		user.ID.String(),
		user.Email,
		sid,
		user.Role,
	)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	refreshToken, _, err := s.jwtService.GenerateRefreshToken(user.ID.String(), sid)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	return &dto.AuthResult{
		User:         user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		SessionID:    sid,
	}, nil
}

// Logout logs out a user by blacklisting their refresh token
func (s *Service) Logout(ctx context.Context, userID, refreshToken, ipAddress string) error {
	claims, err := s.jwtService.ValidateRefreshToken(refreshToken)
	if err != nil {
		return shared.ErrUnauthorized.WithDetails("message", "invalid refresh token")
	}

	if claims.Subject != userID {
		return shared.ErrUnauthorized.WithDetails("message", "token does not belong to user")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return shared.ErrBadRequest.WithDetails("message", "invalid user ID")
	}

	if err := s.tokenBlacklistRepo.Add(ctx, refreshToken, userUUID, "logout", claims.ExpiresAt.Time); err != nil {
		return err
	}

	return nil
}

// RefreshToken generates a new access token from a refresh token
func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*dto.TokenResponse, error) {
	claims, err := s.jwtService.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, shared.ErrUnauthorized.WithDetails("message", "invalid refresh token")
	}

	userUUID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, shared.ErrUnauthorized.WithDetails("message", "invalid user ID in token")
	}

	isRevoked, err := s.tokenBlacklistRepo.IsBlacklisted(ctx, refreshToken)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}
	if isRevoked {
		return nil, shared.ErrUnauthorized.WithDetails("message", "token has been revoked")
	}

	user, err := s.userService.GetByID(ctx, userUUID.String())
	if err != nil {
		return nil, shared.ErrUnauthorized.WithDetails("message", "user not found")
	}

	sid := claims.SessionID
	if sid == "" {
		sid = uuid.New().String()
	}

	accessToken, expiresAt, err := s.jwtService.GenerateAccessToken(
		user.ID.String(),
		user.Email,
		sid,
		user.Role,
	)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	return dto.NewTokenResponse(accessToken, expiresAt), nil
}

// AuthenticateGoogle authenticates a user with Google OAuth
func (s *Service) AuthenticateGoogle(ctx context.Context, req dto.GoogleAuthRequest) (*dto.AuthResult, error) {
	// Verify Google token and get user info
	googleUser, err := s.googleOAuthService.VerifyGoogleToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}

	// Try to find existing user by email
	user, err := s.userService.GetByEmail(ctx, googleUser.Email)
	if err != nil {
		// User doesn't exist, create new one
		if err == shared.ErrUserNotFound {
			newUser := &domain.User{
				Email:         googleUser.Email,
				FullName:      googleUser.Name,
				Role:          domain.UserRoleUser,
				Status:        domain.UserStatusActive,
				EmailVerified: googleUser.VerifiedEmail,
			}

			if googleUser.Picture != "" {
				newUser.AvatarURL = &googleUser.Picture
			}

			// Set a random password (won't be used for Google OAuth users)
			randomPassword, err := s.passwordService.HashPassword(fmt.Sprintf("google_oauth_%s", googleUser.ID))
			if err != nil {
				return nil, shared.ErrInternal.WithError(err)
			}
			newUser.Password = randomPassword

			// Create user
			user, err = s.userService.Create(ctx, newUser)
			if err != nil {
				return nil, err
			}

		} else {
			return nil, err
		}
	}

	// If user exists but email not verified, mark as verified (Google confirmed it)
	if !user.EmailVerified && googleUser.VerifiedEmail {
		if err := s.userService.MarkEmailVerified(ctx, user.ID.String(), time.Now()); err != nil {
			// Log error but don't fail authentication
			s.logger.Warn(
				"Failed to mark email as verified",
				"user_id", user.ID.String(),
				"email", user.Email,
				"error", err,
			)
		}
		user.EmailVerified = true
	}

	// Generate tokens
	sid := uuid.New().String()
	accessToken, expiresAt, err := s.jwtService.GenerateAccessToken(
		user.ID.String(),
		user.Email,
		sid,
		user.Role,
	)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	refreshToken, _, err := s.jwtService.GenerateRefreshToken(user.ID.String(), sid)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	// Update last login
	ip := ""
	_ = s.userService.UpdateLastLogin(ctx, user.ID.String(), time.Now(), &ip)

	return &dto.AuthResult{
		User:         user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		SessionID:    sid,
	}, nil
}
