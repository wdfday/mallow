package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	authDomain "mallow/identity/internal/module/auth/domain"
	authService "mallow/identity/internal/module/auth/service"
	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockJWTService struct{ mock.Mock }

func (m *mockJWTService) GenerateAccessToken(userID, email string, role userDomain.UserRole) (string, int64, error) {
	args := m.Called(userID, email, role)
	return args.String(0), args.Get(1).(int64), args.Error(2)
}
func (m *mockJWTService) GenerateRefreshToken(userID string) (string, int64, error) {
	args := m.Called(userID)
	return args.String(0), args.Get(1).(int64), args.Error(2)
}
func (m *mockJWTService) ValidateToken(tokenString string) (*authService.Claims, error) {
	args := m.Called(tokenString)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*authService.Claims), args.Error(1)
}
func (m *mockJWTService) ValidateRefreshToken(tokenString string) (string, error) {
	args := m.Called(tokenString)
	return args.String(0), args.Error(1)
}

type mockUserService struct{ mock.Mock }

func (m *mockUserService) GetByID(ctx context.Context, id string) (*userDomain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userDomain.User), args.Error(1)
}

// Remaining interface methods — stubs only.
func (m *mockUserService) Create(ctx context.Context, u *userDomain.User) (*userDomain.User, error) {
	return nil, nil
}
func (m *mockUserService) GetByEmail(ctx context.Context, email string) (*userDomain.User, error) {
	return nil, nil
}
func (m *mockUserService) List(ctx context.Context, filter userDomain.ListUsersFilter, p shared.Pagination) (shared.Page[userDomain.User], error) {
	return shared.Page[userDomain.User]{}, nil
}
func (m *mockUserService) Update(ctx context.Context, u *userDomain.User) error { return nil }
func (m *mockUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	return nil
}
func (m *mockUserService) UpdatePassword(ctx context.Context, id, hash string) error { return nil }
func (m *mockUserService) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	return nil
}
func (m *mockUserService) SoftDelete(ctx context.Context, id string) error { return nil }
func (m *mockUserService) HardDelete(ctx context.Context, id string) error { return nil }
func (m *mockUserService) Restore(ctx context.Context, id string) error    { return nil }
func (m *mockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	return false, nil
}
func (m *mockUserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	return nil
}
func (m *mockUserService) IncLoginAttempts(ctx context.Context, id string) error   { return nil }
func (m *mockUserService) ResetLoginAttempts(ctx context.Context, id string) error { return nil }
func (m *mockUserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return nil
}
func (m *mockUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*userDomain.User, error) {
	return nil, nil
}
func (m *mockUserService) LinkAccount(ctx context.Context, userID string, account userDomain.LinkedAccount) error {
	return nil
}
func (m *mockUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	return nil
}

// Ensure mockUserService satisfies the interface.
var _ userService.IUserService = (*mockUserService)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func init() {
	gin.SetMode(gin.TestMode)
}

// validClaims returns claims for a regular, active user.
func validClaims(userID uuid.UUID) *authService.Claims {
	return &authService.Claims{
		UserID: userID.String(),
		Email:  "user@example.com",
		Role:   userDomain.UserRoleUser,
	}
}

// adminClaims returns claims for an admin user.
func adminClaims(userID uuid.UUID) *authService.Claims {
	return &authService.Claims{
		UserID: userID.String(),
		Email:  "admin@example.com",
		Role:   userDomain.UserRoleAdmin,
	}
}

// setupRouter builds a minimal Gin engine with the auth middleware applied to
// a single GET /test route, using the provided options.
func setupRouter(
	jwtSvc authService.IJWTService,
	usrSvc userService.IUserService,
	opts ...func(*AuthOptions),
) *gin.Engine {
	router := gin.New()
	m := NewMiddleware(jwtSvc, usrSvc)
	router.GET("/test", m.AuthMiddleware(opts...), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func performRequest(router http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAuthMiddleware_MissingAuthorizationHeader(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	router := setupRouter(jwtSvc, usrSvc)
	w := performRequest(router, "")

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthMiddleware_EmptyToken(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	router := setupRouter(jwtSvc, usrSvc)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	jwtSvc.On("ValidateToken", "bad-token").Return(nil, errors.New("invalid token"))

	router := setupRouter(jwtSvc, usrSvc)
	w := performRequest(router, "bad-token")

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	jwtSvc.AssertExpectations(t)
}

func TestAuthMiddleware_ValidToken_SetsContext(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)
	jwtSvc.On("ValidateToken", "good-token").Return(claims, nil)

	var capturedUser authDomain.AuthUser
	router := gin.New()
	m := NewMiddleware(jwtSvc, usrSvc)
	router.GET("/test", m.AuthMiddleware(), func(c *gin.Context) {
		user, ok := GetCurrentUser(c)
		assert.True(t, ok)
		capturedUser = user
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := performRequest(router, "good-token")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, userID, capturedUser.ID)
	jwtSvc.AssertExpectations(t)
}

func TestAuthMiddleware_AdminOnly_NonAdmin_Forbidden(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID) // role = user

	activeUser := &userDomain.User{
		ID:     userID,
		Email:  claims.Email,
		Status: userDomain.UserStatusActive,
	}

	jwtSvc.On("ValidateToken", "user-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(activeUser, nil)

	router := setupRouter(jwtSvc, usrSvc, WithAdminOnly())
	w := performRequest(router, "user-token")

	assert.Equal(t, http.StatusForbidden, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_AdminOnly_Admin_Allowed(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := adminClaims(userID)

	adminUser := &userDomain.User{
		ID:     userID,
		Email:  claims.Email,
		Role:   userDomain.UserRoleAdmin,
		Status: userDomain.UserStatusActive,
	}

	jwtSvc.On("ValidateToken", "admin-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(adminUser, nil)

	router := setupRouter(jwtSvc, usrSvc, WithAdminOnly())
	w := performRequest(router, "admin-token")

	assert.Equal(t, http.StatusOK, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_SuspendedUser_Forbidden(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)

	suspendedUser := &userDomain.User{
		ID:     userID,
		Email:  claims.Email,
		Status: userDomain.UserStatusSuspended,
	}

	jwtSvc.On("ValidateToken", "suspended-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(suspendedUser, nil)

	router := setupRouter(jwtSvc, usrSvc, WithIsNotSuspended())
	w := performRequest(router, "suspended-token")

	assert.Equal(t, http.StatusForbidden, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_EmailNotVerified_Forbidden(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)

	unverifiedUser := &userDomain.User{
		ID:            userID,
		Email:         claims.Email,
		Status:        userDomain.UserStatusActive,
		EmailVerified: false,
	}

	jwtSvc.On("ValidateToken", "unverified-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(unverifiedUser, nil)

	router := setupRouter(jwtSvc, usrSvc, WithEmailVerified())
	w := performRequest(router, "unverified-token")

	assert.Equal(t, http.StatusForbidden, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_EmailVerified_Allowed(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)

	verifiedUser := &userDomain.User{
		ID:            userID,
		Email:         claims.Email,
		Status:        userDomain.UserStatusActive,
		EmailVerified: true,
	}

	jwtSvc.On("ValidateToken", "verified-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(verifiedUser, nil)

	router := setupRouter(jwtSvc, usrSvc, WithEmailVerified())
	w := performRequest(router, "verified-token")

	assert.Equal(t, http.StatusOK, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_UserNotFound_Unauthorized(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)

	jwtSvc.On("ValidateToken", "orphan-token").Return(claims, nil)
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(nil, shared.ErrUserNotFound)

	router := setupRouter(jwtSvc, usrSvc, WithAdminOnly())
	w := performRequest(router, "orphan-token")

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	jwtSvc.AssertExpectations(t)
	usrSvc.AssertExpectations(t)
}

func TestAuthMiddleware_BearerCaseInsensitive(t *testing.T) {
	jwtSvc := &mockJWTService{}
	usrSvc := &mockUserService{}

	userID := uuid.New()
	claims := validClaims(userID)
	jwtSvc.On("ValidateToken", "my-token").Return(claims, nil)

	router := setupRouter(jwtSvc, usrSvc)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "bearer my-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	jwtSvc.AssertExpectations(t)
}

func TestGetCurrentUser_NotSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	_, ok := GetCurrentUser(c)
	assert.False(t, ok)
}

func TestGetCurrentUser_Set(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	userID := uuid.New()
	c.Set(UserKey, authDomain.AuthUser{ID: userID, Username: "user@example.com"})

	user, ok := GetCurrentUser(c)
	assert.True(t, ok)
	assert.Equal(t, userID, user.ID)
}
