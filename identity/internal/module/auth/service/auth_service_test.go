package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"log/slog"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/auth/dto"
	profiledomain "mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	profileservice "mallow/identity/internal/module/profile/service"
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ==================== Mocks ====================

type MockUserService struct {
	mock.Mock
}

func (m *MockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	args := m.Called(ctx, email)
	return args.Bool(0), args.Error(1)
}

func (m *MockUserService) Create(ctx context.Context, user *userdomain.User) (*userdomain.User, error) {
	args := m.Called(ctx, user)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userdomain.User), args.Error(1)
}

func (m *MockUserService) GetByEmail(ctx context.Context, email string) (*userdomain.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userdomain.User), args.Error(1)
}

func (m *MockUserService) GetByID(ctx context.Context, id string) (*userdomain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userdomain.User), args.Error(1)
}

func (m *MockUserService) IncLoginAttempts(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockUserService) ResetLoginAttempts(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockUserService) SetLockedUntil(ctx context.Context, userID string, lockedUntil *time.Time) error {
	args := m.Called(ctx, userID, lockedUntil)
	return args.Error(0)
}

func (m *MockUserService) UpdateLastLogin(ctx context.Context, userID string, lastLoginAt time.Time, ipAddress *string) error {
	args := m.Called(ctx, userID, lastLoginAt, ipAddress)
	return args.Error(0)
}

func (m *MockUserService) MarkEmailVerified(ctx context.Context, userID string, verifiedAt time.Time) error {
	args := m.Called(ctx, userID, verifiedAt)
	return args.Error(0)
}

// Placeholder methods (not used in auth service but part of interface)
func (m *MockUserService) List(ctx context.Context, filter userdomain.ListUsersFilter, pagination shared.Pagination) (shared.Page[userdomain.User], error) {
	return shared.Page[userdomain.User]{}, nil
}
func (m *MockUserService) Update(ctx context.Context, user *userdomain.User) error { return nil }
func (m *MockUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	return nil
}
func (m *MockUserService) UpdatePassword(ctx context.Context, id string, passwordHash string) error {
	args := m.Called(ctx, id, passwordHash)
	return args.Error(0)
}

func (m *MockUserService) Delete(ctx context.Context, id string) error { return nil }
func (m *MockUserService) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	return false, nil
}
func (m *MockUserService) SoftDelete(ctx context.Context, id string) error {
	return nil
}
func (m *MockUserService) HardDelete(ctx context.Context, id string) error {
	return nil
}
func (m *MockUserService) Restore(ctx context.Context, id string) error {
	return nil
}
func (m *MockUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*userdomain.User, error) {
	return nil, nil
}
func (m *MockUserService) LinkAccount(ctx context.Context, userID string, account userdomain.LinkedAccount) error {
	return nil
}
func (m *MockUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	return nil
}

// MockProfileService implements profileservice.Service for testing.
type MockProfileService struct{ mock.Mock }

func (m *MockProfileService) CreateProfile(ctx context.Context, userID string, req profiledto.CreateProfileRequest) (*profiledomain.UserProfile, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*profiledomain.UserProfile), args.Error(1)
}
func (m *MockProfileService) CreateDefaultProfile(ctx context.Context, userID string) (*profiledomain.UserProfile, error) {
	return nil, nil
}
func (m *MockProfileService) GetProfile(ctx context.Context, userID string) (*profiledomain.UserProfile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*profiledomain.UserProfile), args.Error(1)
}
func (m *MockProfileService) UpdateProfile(ctx context.Context, userID string, req profiledto.UpdateProfileRequest) (*profiledomain.UserProfile, error) {
	return nil, nil
}

var _ profileservice.Service = (*MockProfileService)(nil)

type MockJWTService struct {
	mock.Mock
}

func (m *MockJWTService) GenerateAccessToken(userID, email, sessionID string, role userdomain.UserRole) (string, int64, error) {
	args := m.Called(userID, email, sessionID, role)
	return args.String(0), args.Get(1).(int64), args.Error(2)
}

func (m *MockJWTService) GenerateRefreshToken(userID, sessionID string) (string, int64, error) {
	args := m.Called(userID, sessionID)
	return args.String(0), args.Get(1).(int64), args.Error(2)
}

func (m *MockJWTService) ValidateToken(tokenString string) (*Claims, error) {
	args := m.Called(tokenString)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Claims), args.Error(1)
}

func (m *MockJWTService) ValidateRefreshToken(tokenString string) (*RefreshClaims, error) {
	args := m.Called(tokenString)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*RefreshClaims), args.Error(1)
}

func (m *MockJWTService) ExtractSessionID(tokenString string) string {
	args := m.Called(tokenString)
	return args.String(0)
}

type MockPasswordService struct {
	mock.Mock
}

func (m *MockPasswordService) HashPassword(password string) (string, error) {
	args := m.Called(password)
	return args.String(0), args.Error(1)
}

func (m *MockPasswordService) VerifyPassword(hashedPassword, password string) error {
	args := m.Called(hashedPassword, password)
	return args.Error(0)
}

func (m *MockPasswordService) IsValidPassword(password string) bool {
	return true
}

func (m *MockPasswordService) ValidatePasswordStrength(password string) []string {
	return nil
}

func (m *MockPasswordService) ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordRequest) error {
	return nil
}

func (m *MockPasswordService) ForgotPassword(ctx context.Context, email, ipAddress, userAgent string) error {
	return nil
}

func (m *MockPasswordService) ResetPassword(ctx context.Context, tokenStr, newPassword string) error {
	return nil
}

type MockTokenBlacklistRepo struct {
	mock.Mock
}

func (m *MockTokenBlacklistRepo) Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error {
	args := m.Called(ctx, token, userID, reason, expiresAt)
	return args.Error(0)
}

func (m *MockTokenBlacklistRepo) IsBlacklisted(ctx context.Context, token string) (bool, error) {
	args := m.Called(ctx, token)
	return args.Bool(0), args.Error(1)
}

func (m *MockTokenBlacklistRepo) CleanupExpired(ctx context.Context) error {
	return nil
}

func (m *MockTokenBlacklistRepo) BlacklistAllUserTokens(ctx context.Context, userID uuid.UUID, reason string) error {
	return nil
}

func (m *MockTokenBlacklistRepo) RevokeSession(ctx context.Context, sid string, expiresAt time.Time) error {
	return nil
}

func (m *MockTokenBlacklistRepo) IsRevokedByFields(ctx context.Context, sid, userID string) (bool, error) {
	return false, nil
}

type MockGoogleOAuthService struct {
	mock.Mock
}

// ==================== Helpers ====================

func makeRFClaims(userID uuid.UUID, sid string) *RefreshClaims {
	return &RefreshClaims{
		SessionID: sid,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		},
	}
}

// ==================== Tests ====================

// TestRegister tests user registration
func TestRegister(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Register new user", func(t *testing.T) {
		// Setup mocks
		mockUserService := new(MockUserService)
		mockJWTService := new(MockJWTService)
		mockPasswordService := new(MockPasswordService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)
		mockProfileService := new(MockProfileService)

		service := &Service{
			userService:        mockUserService,
			profileService:     mockProfileService,
			jwtService:         mockJWTService,
			passwordService:    mockPasswordService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		// Test data
		req := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
			Phone:    "+84123456789",
		}

		userID := uuid.New()
		createdUser := &userdomain.User{
			ID:            userID,
			Email:         req.Email,
			Role:          userdomain.UserRoleUser,
			Status:        userdomain.UserStatusPendingVerification,
			Password:      "hashed_password",
			EmailVerified: false,
			CreatedAt:     time.Now(),
		}

		// Mock expectations
		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(false, nil)
		mockPasswordService.On("HashPassword", req.Password).Return("hashed_password", nil)
		mockUserService.On("Create", ctx, mock.AnythingOfType("*domain.User")).Return(createdUser, nil)
		mockProfileService.On("CreateProfile", ctx, createdUser.ID.String(), mock.Anything).
			Return(&profiledomain.UserProfile{UserID: createdUser.ID, FullName: req.FullName}, nil)
		mockJWTService.On("GenerateAccessToken", createdUser.ID.String(), createdUser.Email, mock.AnythingOfType("string"), createdUser.Role).
			Return("access_token", int64(3600), nil)
		mockJWTService.On("GenerateRefreshToken", createdUser.ID.String(), mock.AnythingOfType("string")).
			Return("refresh_token", int64(604800), nil)

		// Execute
		result, err := service.Register(ctx, req)

		// Assert
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, createdUser, result.User)
		assert.Equal(t, "access_token", result.AccessToken)
		assert.Equal(t, "refresh_token", result.RefreshToken)
		assert.Equal(t, int64(3600), result.ExpiresAt)

		mockUserService.AssertExpectations(t)
		mockJWTService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("Error - Email already exists", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockPasswordService := new(MockPasswordService)

		service := &Service{
			userService:     mockUserService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.RegisterRequest{
			Email:    "existing@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(true, nil)

		result, err := service.Register(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "conflict")

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Password hashing fails", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockPasswordService := new(MockPasswordService)

		service := &Service{
			userService:     mockUserService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(false, nil)
		mockPasswordService.On("HashPassword", req.Password).Return("", errors.New("hashing failed"))

		result, err := service.Register(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockUserService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
	})
}

// TestLogin tests user login
func TestLogin(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Login with valid credentials", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockJWTService := new(MockJWTService)
		mockPasswordService := new(MockPasswordService)
		mockProfileService := new(MockProfileService)

		service := &Service{
			userService:     mockUserService,
			profileService:  mockProfileService,
			jwtService:      mockJWTService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			IP:       "192.168.1.1",
		}

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         req.Email,
			Password:      "hashed_password",
			Role:          userdomain.UserRoleUser,
			Status:        userdomain.UserStatusActive,
			LoginAttempts: 0,
			LockedUntil:   nil,
			EmailVerified: true,
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(user, nil)
		mockPasswordService.On("VerifyPassword", user.Password, req.Password).Return(nil)
		mockUserService.On("ResetLoginAttempts", ctx, user.ID.String()).Return(nil)
		mockUserService.On("UpdateLastLogin", ctx, user.ID.String(), mock.AnythingOfType("time.Time"), &req.IP).Return(nil)
		mockJWTService.On("GenerateAccessToken", user.ID.String(), user.Email, mock.AnythingOfType("string"), user.Role).
			Return("access_token", int64(3600), nil)
		mockJWTService.On("GenerateRefreshToken", user.ID.String(), mock.AnythingOfType("string")).
			Return("refresh_token", int64(604800), nil)
		mockProfileService.On("GetProfile", ctx, user.ID.String()).
			Return(&profiledomain.UserProfile{UserID: userID, FullName: "Test User"}, nil)

		result, err := service.Login(ctx, req)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, user, result.User)
		assert.Equal(t, "access_token", result.AccessToken)
		assert.Equal(t, "refresh_token", result.RefreshToken)

		mockUserService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
		mockJWTService.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("Error - User not found", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := &Service{
			userService: mockUserService,
			config:      &config.Config{},
			logger:      slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "notfound@example.com",
			Password: "Password123!",
			IP:       "192.168.1.1",
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(nil, shared.ErrUserNotFound)

		result, err := service.Login(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Invalid password", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockPasswordService := new(MockPasswordService)

		service := &Service{
			userService:     mockUserService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "test@example.com",
			Password: "WrongPassword",
			IP:       "192.168.1.1",
		}

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         req.Email,
			Password:      "hashed_password",
			LoginAttempts: 0,
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(user, nil)
		mockPasswordService.On("VerifyPassword", user.Password, req.Password).Return(errors.New("invalid password"))
		mockUserService.On("IncLoginAttempts", ctx, user.ID.String()).Return(nil)

		result, err := service.Login(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockUserService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
	})

	t.Run("Error - Account locked", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := &Service{
			userService: mockUserService,
			config:      &config.Config{},
			logger:      slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "locked@example.com",
			Password: "Password123!",
		}

		lockUntil := time.Now().Add(15 * time.Minute)
		user := &userdomain.User{
			ID:          uuid.New(),
			Email:       req.Email,
			Password:    "hashed_password",
			LockedUntil: &lockUntil,
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(user, nil)

		result, err := service.Login(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Account suspended", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockPasswordService := new(MockPasswordService)

		service := &Service{
			userService:     mockUserService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "suspended@example.com",
			Password: "Password123!",
		}

		user := &userdomain.User{
			ID:       uuid.New(),
			Email:    req.Email,
			Password: "hashed_password",
			Status:   userdomain.UserStatusSuspended,
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(user, nil)
		mockPasswordService.On("VerifyPassword", user.Password, req.Password).Return(nil)

		result, err := service.Login(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockUserService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
	})
}

// TestLogout tests user logout
func TestLogout(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Logout with valid token", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "valid_refresh_token"
		ipAddress := "192.168.1.1"

		rfClaims := &RefreshClaims{RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		}}
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(rfClaims, nil)
		mockTokenBlacklistRepo.On("Add", ctx, refreshToken, userID, "logout", mock.AnythingOfType("time.Time")).Return(nil)

		err := service.Logout(ctx, userID.String(), refreshToken, ipAddress)

		assert.NoError(t, err)

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
	})

	t.Run("Error - Invalid token", func(t *testing.T) {
		mockJWTService := new(MockJWTService)

		service := &Service{
			jwtService: mockJWTService,
			config:     &config.Config{},
			logger:     slog.Default(),
		}

		refreshToken := "invalid_token"
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(nil, errors.New("invalid token"))

		err := service.Logout(ctx, "user-id", refreshToken, "192.168.1.1")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockJWTService.AssertExpectations(t)
	})

	t.Run("Error - Token doesn't belong to user", func(t *testing.T) {
		mockJWTService := new(MockJWTService)

		service := &Service{
			jwtService: mockJWTService,
			config:     &config.Config{},
			logger:     slog.Default(),
		}

		userID := uuid.New()
		differentUserID := uuid.New()
		refreshToken := "token_for_different_user"

		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(&RefreshClaims{RegisteredClaims: jwt.RegisteredClaims{
			Subject:   differentUserID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		}}, nil)

		err := service.Logout(ctx, userID.String(), refreshToken, "192.168.1.1")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockJWTService.AssertExpectations(t)
	})
}

// TestRefreshToken tests token refresh
// Flow: ValidateRefreshToken → IsBlacklisted → GetByID → GenerateAccessToken
func TestRefreshToken(t *testing.T) {
	ctx := context.Background()

	makeRFClaims := func(userID uuid.UUID, sid string) *RefreshClaims {
		return &RefreshClaims{
			SessionID: sid,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   userID.String(),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			},
		}
	}

	t.Run("Success - Refresh with valid token", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)
		mockUserService := new(MockUserService)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			userService:        mockUserService,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		sid := uuid.New().String()
		refreshToken := "valid_refresh_token"
		user := &userdomain.User{
			ID:    userID,
			Email: "test@example.com",
			Role:  userdomain.UserRoleUser,
		}

		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, sid), nil)
		mockTokenBlacklistRepo.On("IsBlacklisted", ctx, refreshToken).Return(false, nil)
		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)
		mockJWTService.On("GenerateAccessToken", user.ID.String(), user.Email, sid, user.Role).
			Return("new_access_token", int64(3600), nil)

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "new_access_token", result.AccessToken)
		assert.Equal(t, int64(3600), result.ExpiresIn)

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Invalid refresh token", func(t *testing.T) {
		mockJWTService := new(MockJWTService)

		service := &Service{
			jwtService: mockJWTService,
			config:     &config.Config{},
			logger:     slog.Default(),
		}

		refreshToken := "invalid_token"
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(nil, errors.New("invalid token"))

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockJWTService.AssertExpectations(t)
	})

	t.Run("Error - Token is blacklisted", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "blacklisted_token"
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, uuid.New().String()), nil)
		mockTokenBlacklistRepo.On("IsBlacklisted", ctx, refreshToken).Return(true, nil)

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
	})

	t.Run("Error - Blacklist check fails", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "some_token"
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, uuid.New().String()), nil)
		mockTokenBlacklistRepo.On("IsBlacklisted", ctx, refreshToken).Return(false, errors.New("db error"))

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
	})

	t.Run("Error - User not found", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)
		mockUserService := new(MockUserService)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			userService:        mockUserService,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "valid_refresh_token"
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, uuid.New().String()), nil)
		mockTokenBlacklistRepo.On("IsBlacklisted", ctx, refreshToken).Return(false, nil)
		mockUserService.On("GetByID", ctx, userID.String()).Return(nil, shared.ErrUserNotFound)

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Token generation fails", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)
		mockUserService := new(MockUserService)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			userService:        mockUserService,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		sid := uuid.New().String()
		refreshToken := "valid_refresh_token"
		user := &userdomain.User{
			ID:    userID,
			Email: "test@example.com",
			Role:  userdomain.UserRoleUser,
		}

		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, sid), nil)
		mockTokenBlacklistRepo.On("IsBlacklisted", ctx, refreshToken).Return(false, nil)
		mockUserService.On("GetByID", ctx, userID.String()).Return(user, nil)
		mockJWTService.On("GenerateAccessToken", user.ID.String(), user.Email, sid, user.Role).
			Return("", int64(0), errors.New("token generation failed"))

		result, err := service.RefreshToken(ctx, refreshToken)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockJWTService.AssertExpectations(t)
	})
}

// TestLoginAccountLocking tests account locking after failed attempts
func TestLoginAccountLocking(t *testing.T) {
	ctx := context.Background()

	t.Run("Account locked after 5th failed attempt", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockPasswordService := new(MockPasswordService)

		service := &Service{
			userService:     mockUserService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.LoginRequest{
			Email:    "test@example.com",
			Password: "WrongPassword",
			IP:       "192.168.1.1",
		}

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         req.Email,
			Password:      "hashed_password",
			LoginAttempts: 4, // Already 4 failed attempts
		}

		mockUserService.On("GetByEmail", ctx, req.Email).Return(user, nil)
		mockPasswordService.On("VerifyPassword", user.Password, req.Password).Return(errors.New("invalid password"))
		mockUserService.On("IncLoginAttempts", ctx, user.ID.String()).Return(nil)
		mockUserService.On("SetLockedUntil", ctx, user.ID.String(), mock.AnythingOfType("*time.Time")).Return(nil)

		result, err := service.Login(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "Unauthorized")

		mockUserService.AssertExpectations(t)
		mockPasswordService.AssertExpectations(t)
	})
}

// TestLogoutEdgeCases tests additional logout edge cases
func TestLogoutEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Logout even if user not found for logging", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)
		mockUserService := new(MockUserService)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			userService:        mockUserService,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "valid_refresh_token"
		ipAddress := "192.168.1.1"

		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, uuid.New().String()), nil)
		mockTokenBlacklistRepo.On("Add", ctx, refreshToken, userID, "logout", mock.AnythingOfType("time.Time")).Return(nil)
		mockUserService.On("GetByID", ctx, userID.String()).Return(nil, shared.ErrUserNotFound)

		err := service.Logout(ctx, userID.String(), refreshToken, ipAddress)

		assert.NoError(t, err) // Should succeed even if user not found for logging

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
	})

	t.Run("Error - Invalid user ID format", func(t *testing.T) {
		mockJWTService := new(MockJWTService)

		service := &Service{
			jwtService: mockJWTService,
			config:     &config.Config{},
			logger:     slog.Default(),
		}

		invalidUserID := "not-a-uuid"
		refreshToken := "valid_refresh_token"

		// ValidateRefreshToken succeeds but Subject is not a valid UUID
		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(&RefreshClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   invalidUserID,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			},
		}, nil)

		err := service.Logout(ctx, invalidUserID, refreshToken, "192.168.1.1")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Bad request")

		mockJWTService.AssertExpectations(t)
	})

	t.Run("Error - Blacklist add fails", func(t *testing.T) {
		mockJWTService := new(MockJWTService)
		mockTokenBlacklistRepo := new(MockTokenBlacklistRepo)

		service := &Service{
			jwtService:         mockJWTService,
			tokenBlacklistRepo: mockTokenBlacklistRepo,
			config:             &config.Config{},
			logger:             slog.Default(),
		}

		userID := uuid.New()
		refreshToken := "valid_refresh_token"

		mockJWTService.On("ValidateRefreshToken", refreshToken).Return(makeRFClaims(userID, uuid.New().String()), nil)
		mockTokenBlacklistRepo.On("Add", ctx, refreshToken, userID, "logout", mock.AnythingOfType("time.Time")).Return(errors.New("db error"))

		err := service.Logout(ctx, userID.String(), refreshToken, "192.168.1.1")

		assert.Error(t, err)

		mockJWTService.AssertExpectations(t)
		mockTokenBlacklistRepo.AssertExpectations(t)
	})
}

// TestRegisterEdgeCases tests additional register edge cases
func TestRegisterEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("Error - ExistsByEmail check fails", func(t *testing.T) {
		mockUserService := new(MockUserService)

		service := &Service{
			userService: mockUserService,
			config:      &config.Config{},
			logger:      slog.Default(),
		}

		req := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(false, errors.New("db error"))

		result, err := service.Register(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockUserService.AssertExpectations(t)
	})

	t.Run("Error - Access token generation fails", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockJWTService := new(MockJWTService)
		mockPasswordService := new(MockPasswordService)
		mockProfileService := new(MockProfileService)

		service := &Service{
			userService:     mockUserService,
			profileService:  mockProfileService,
			jwtService:      mockJWTService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		userID := uuid.New()
		createdUser := &userdomain.User{
			ID:    userID,
			Email: req.Email,
			Role:  userdomain.UserRoleUser,
		}

		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(false, nil)
		mockPasswordService.On("HashPassword", req.Password).Return("hashed_password", nil)
		mockUserService.On("Create", ctx, mock.AnythingOfType("*domain.User")).Return(createdUser, nil)
		mockProfileService.On("CreateProfile", ctx, createdUser.ID.String(), mock.Anything).
			Return(&profiledomain.UserProfile{}, nil)
		mockJWTService.On("GenerateAccessToken", createdUser.ID.String(), createdUser.Email, mock.AnythingOfType("string"), createdUser.Role).
			Return("", int64(0), errors.New("token generation failed"))

		result, err := service.Register(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockUserService.AssertExpectations(t)
		mockJWTService.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("Error - Refresh token generation fails", func(t *testing.T) {
		mockUserService := new(MockUserService)
		mockJWTService := new(MockJWTService)
		mockPasswordService := new(MockPasswordService)
		mockProfileService := new(MockProfileService)

		service := &Service{
			userService:     mockUserService,
			profileService:  mockProfileService,
			jwtService:      mockJWTService,
			passwordService: mockPasswordService,
			config:          &config.Config{},
			logger:          slog.Default(),
		}

		req := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		userID := uuid.New()
		createdUser := &userdomain.User{
			ID:    userID,
			Email: req.Email,
			Role:  userdomain.UserRoleUser,
		}

		mockUserService.On("ExistsByEmail", ctx, req.Email).Return(false, nil)
		mockPasswordService.On("HashPassword", req.Password).Return("hashed_password", nil)
		mockUserService.On("Create", ctx, mock.AnythingOfType("*domain.User")).Return(createdUser, nil)
		mockProfileService.On("CreateProfile", ctx, createdUser.ID.String(), mock.Anything).
			Return(&profiledomain.UserProfile{}, nil)
		mockJWTService.On("GenerateAccessToken", createdUser.ID.String(), createdUser.Email, mock.AnythingOfType("string"), createdUser.Role).
			Return("access_token", int64(3600), nil)
		mockJWTService.On("GenerateRefreshToken", createdUser.ID.String(), mock.AnythingOfType("string")).
			Return("", int64(0), errors.New("refresh token generation failed"))

		result, err := service.Register(ctx, req)

		assert.Error(t, err)
		assert.Nil(t, result)

		mockUserService.AssertExpectations(t)
		mockJWTService.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
	})
}
