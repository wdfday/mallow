package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// Mock – reuse the mockUserService from auth_middleware_test.go is in the same
// package, so we define a local version here with a different name.
// ---------------------------------------------------------------------------

type emailVerifMockUserService struct{ mock.Mock }

func (m *emailVerifMockUserService) GetByID(ctx context.Context, id string) (*userDomain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userDomain.User), args.Error(1)
}

// Stubs for unused IUserService methods
func (m *emailVerifMockUserService) Create(ctx context.Context, u *userDomain.User) (*userDomain.User, error) {
	return nil, nil
}
func (m *emailVerifMockUserService) GetByEmail(ctx context.Context, email string) (*userDomain.User, error) {
	return nil, nil
}
func (m *emailVerifMockUserService) List(ctx context.Context, filter userDomain.ListUsersFilter, p shared.Pagination) (shared.Page[userDomain.User], error) {
	return shared.Page[userDomain.User]{}, nil
}
func (m *emailVerifMockUserService) Update(ctx context.Context, u *userDomain.User) error { return nil }
func (m *emailVerifMockUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	return nil
}
func (m *emailVerifMockUserService) UpdatePassword(ctx context.Context, id, hash string) error {
	return nil
}
func (m *emailVerifMockUserService) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	return nil
}
func (m *emailVerifMockUserService) SoftDelete(ctx context.Context, id string) error { return nil }
func (m *emailVerifMockUserService) HardDelete(ctx context.Context, id string) error { return nil }
func (m *emailVerifMockUserService) Restore(ctx context.Context, id string) error    { return nil }
func (m *emailVerifMockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	return false, nil
}
func (m *emailVerifMockUserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	return nil
}
func (m *emailVerifMockUserService) IncLoginAttempts(ctx context.Context, id string) error {
	return nil
}
func (m *emailVerifMockUserService) ResetLoginAttempts(ctx context.Context, id string) error {
	return nil
}
func (m *emailVerifMockUserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return nil
}
func (m *emailVerifMockUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*userDomain.User, error) {
	return nil, nil
}
func (m *emailVerifMockUserService) LinkAccount(ctx context.Context, userID string, account userDomain.LinkedAccount) error {
	return nil
}
func (m *emailVerifMockUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	return nil
}

var _ userService.IUserService = (*emailVerifMockUserService)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupEmailVerifRoute(usrSvc userService.IUserService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	mw := NewEmailVerificationMiddleware(usrSvc)
	router := gin.New()
	router.Use(LoggerMiddleware(slog.Default()))
	router.GET("/protected", mw.RequireVerifiedEmail(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func setupEmailVerifRouteWithUserID(usrSvc userService.IUserService, userID interface{}) *gin.Engine {
	gin.SetMode(gin.TestMode)
	mw := NewEmailVerificationMiddleware(usrSvc)
	router := gin.New()
	router.Use(LoggerMiddleware(slog.Default()))
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Next()
	})
	router.GET("/protected", mw.RequireVerifiedEmail(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEmailVerification_NoUserInContext_Unauthorized(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	router := setupEmailVerifRoute(usrSvc)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEmailVerification_InvalidUserIDType_InternalError(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	// Set user_id to an int (not string or UUID)
	router := setupEmailVerifRouteWithUserID(usrSvc, 12345)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestEmailVerification_UserIDAsString_UserNotFound(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	userID := uuid.New().String()
	usrSvc.On("GetByID", mock.Anything, userID).Return(nil, errors.New("user not found"))

	router := setupEmailVerifRouteWithUserID(usrSvc, userID)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestEmailVerification_UserIDAsUUID_VerifiedUser_Passes(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	userID := uuid.New()
	verifiedUser := &userDomain.User{
		ID:            userID,
		Email:         "verified@example.com",
		EmailVerified: true,
	}
	usrSvc.On("GetByID", mock.Anything, userID.String()).Return(verifiedUser, nil)

	// UUID implements String() interface, so set as UUID type
	router := setupEmailVerifRouteWithUserID(usrSvc, userID)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestEmailVerification_UnverifiedUser_Forbidden(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	userID := uuid.New().String()
	unverifiedUser := &userDomain.User{
		ID:            uuid.MustParse(userID),
		Email:         "unverified@example.com",
		EmailVerified: false,
	}
	usrSvc.On("GetByID", mock.Anything, userID).Return(unverifiedUser, nil)

	router := setupEmailVerifRouteWithUserID(usrSvc, userID)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestEmailVerification_VerifiedUser_Passes(t *testing.T) {
	usrSvc := &emailVerifMockUserService{}
	userID := uuid.New().String()
	verifiedUser := &userDomain.User{
		ID:            uuid.MustParse(userID),
		Email:         "verified@example.com",
		EmailVerified: true,
	}
	usrSvc.On("GetByID", mock.Anything, userID).Return(verifiedUser, nil)

	router := setupEmailVerifRouteWithUserID(usrSvc, userID)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	usrSvc.AssertExpectations(t)
}
