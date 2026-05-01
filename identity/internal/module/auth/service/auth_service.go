package service

import (
	"context"
	"fmt"
	"log/slog"
	authdomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/repository"
	profiledomain "mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	profileservice "mallow/identity/internal/module/profile/service"
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
	profileService     profileservice.Service
	jwtService         IJWTService
	passwordService    IPasswordService
	googleOAuthService *GoogleOAuthService
	tokenBlacklistRepo repository.ITokenBlacklistRepository
	config             *config.Config
	logger             *slog.Logger
}

type googleIDReader interface {
	GetByGoogleID(ctx context.Context, googleID string) (*domain.User, error)
}

// NewService creates a new auth service
func NewService(
	userService service.IUserService,
	profileService profileservice.Service,
	jwtService IJWTService,
	passwordService IPasswordService,
	googleOAuthService *GoogleOAuthService,
	tokenBlacklistRepo repository.ITokenBlacklistRepository,
	cfg *config.Config,
	logger *slog.Logger,
) *Service {
	return &Service{
		userService:        userService,
		profileService:     profileService,
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

	// Create profile with FullName (nil-safe: profileService may be absent in tests)
	var profile *profiledomain.UserProfile
	if s.profileService != nil {
		profile, _ = s.profileService.CreateProfile(ctx, createdUser.ID.String(), profiledto.CreateProfileRequest{
			FullName: &req.FullName,
		})
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
		Profile:      profile,
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

	// Fetch profile (nil-safe: legacy users may not have one; profileService may be absent in tests)
	var profile *profiledomain.UserProfile
	if s.profileService != nil {
		profile, _ = s.profileService.GetProfile(ctx, user.ID.String())
	}

	return &dto.AuthResult{
		User:         user,
		Profile:      profile,
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

	if s.config == nil || s.config.Google.ClientID == "" {
		return nil, shared.ErrInternal.WithDetails("message", "Google auth is not configured")
	}
	if googleUser.Audience != s.config.Google.ClientID {
		return nil, shared.ErrUnauthorized.WithDetails("message", "Google token audience mismatch")
	}
	if !googleUser.VerifiedEmail {
		return nil, shared.ErrUnauthorized.WithDetails(
			"message",
			"Google email is not verified; please log in with another method and verify your email before using Google login",
		)
	}

	googleReader, ok := s.userService.(googleIDReader)
	if !ok {
		return nil, shared.ErrInternal.WithDetails("message", "Google ID lookup is not configured")
	}

	user, err := googleReader.GetByGoogleID(ctx, googleUser.ID)
	if err != nil {
		if err == shared.ErrUserNotFound {
			user, err = s.findOrCreateGoogleUser(ctx, googleUser)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	if user.Status == domain.UserStatusSuspended {
		return nil, shared.ErrUnauthorized.WithDetails("message", "account is suspended")
	}
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		return nil, shared.ErrUnauthorized.WithDetails("message", "account is locked")
	}

	// Upsert profile with name + avatar from Google (nil-safe: profileService may be absent in tests)
	var avatarURL *string
	if googleUser.Picture != "" {
		avatarURL = &googleUser.Picture
	}
	var profile *profiledomain.UserProfile
	if s.profileService != nil {
		profile, _ = s.profileService.GetProfile(ctx, user.ID.String())
		if profile == nil {
			profile, _ = s.profileService.CreateProfile(ctx, user.ID.String(), profiledto.CreateProfileRequest{
				FullName:  &googleUser.Name,
				AvatarURL: avatarURL,
			})
		} else if avatarURL != nil {
			profile, _ = s.profileService.UpdateProfile(ctx, user.ID.String(), profiledto.UpdateProfileRequest{
				AvatarURL: avatarURL,
			})
		}
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
		Profile:      profile,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		SessionID:    sid,
	}, nil
}

func (s *Service) findOrCreateGoogleUser(ctx context.Context, googleUser *authdomain.GoogleUserInfo) (*domain.User, error) {
	user, err := s.userService.GetByEmail(ctx, googleUser.Email)
	if err == nil {
		if user.GoogleID != nil && *user.GoogleID != googleUser.ID {
			return nil, shared.ErrConflict.WithDetails("field", "google_id")
		}
		if user.GoogleID == nil || *user.GoogleID == "" {
			if err := s.userService.UpdateColumns(ctx, user.ID.String(), map[string]any{"google_id": googleUser.ID}); err != nil {
				return nil, err
			}
			user.GoogleID = &googleUser.ID
		}
		if !user.EmailVerified {
			if err := s.userService.MarkEmailVerified(ctx, user.ID.String(), time.Now()); err != nil {
				s.logger.Warn(
					"Failed to mark email as verified",
					"user_id", user.ID.String(),
					"email", user.Email,
					"error", err,
				)
			}
			user.EmailVerified = true
		}
		return user, nil
	}
	if err != shared.ErrUserNotFound {
		return nil, err
	}

	newUser := &domain.User{
		Email:         googleUser.Email,
		GoogleID:      &googleUser.ID,
		Role:          domain.UserRoleUser,
		Status:        domain.UserStatusActive,
		EmailVerified: true,
	}

	randomPassword, err := s.passwordService.HashPassword(fmt.Sprintf("google_oauth_%s", googleUser.ID))
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}
	newUser.Password = randomPassword

	return s.userService.Create(ctx, newUser)
}
