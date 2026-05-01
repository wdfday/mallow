package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/middleware"
	authDomain "mallow/identity/internal/module/auth/domain"
	profileDomain "mallow/identity/internal/module/profile/domain"
	profileService "mallow/identity/internal/module/profile/service"
	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type telegramMockUserService struct{ mock.Mock }

func (m *telegramMockUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*userDomain.User, error) {
	args := m.Called(ctx, provider, providerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userDomain.User), args.Error(1)
}
func (m *telegramMockUserService) LinkAccount(ctx context.Context, userID string, account userDomain.LinkedAccount) error {
	args := m.Called(ctx, userID, account)
	return args.Error(0)
}
func (m *telegramMockUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	args := m.Called(ctx, userID, provider, providerID)
	return args.Error(0)
}

// Stubs for unused IUserService methods
func (m *telegramMockUserService) Create(ctx context.Context, u *userDomain.User) (*userDomain.User, error) {
	return nil, nil
}
func (m *telegramMockUserService) GetByID(ctx context.Context, id string) (*userDomain.User, error) {
	return nil, nil
}
func (m *telegramMockUserService) GetByEmail(ctx context.Context, email string) (*userDomain.User, error) {
	return nil, nil
}
func (m *telegramMockUserService) List(ctx context.Context, filter userDomain.ListUsersFilter, p shared.Pagination) (shared.Page[userDomain.User], error) {
	return shared.Page[userDomain.User]{}, nil
}
func (m *telegramMockUserService) Update(ctx context.Context, u *userDomain.User) error { return nil }
func (m *telegramMockUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	return nil
}
func (m *telegramMockUserService) UpdatePassword(ctx context.Context, id, hash string) error {
	return nil
}
func (m *telegramMockUserService) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	return nil
}
func (m *telegramMockUserService) SoftDelete(ctx context.Context, id string) error { return nil }
func (m *telegramMockUserService) HardDelete(ctx context.Context, id string) error { return nil }
func (m *telegramMockUserService) Restore(ctx context.Context, id string) error    { return nil }
func (m *telegramMockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	return false, nil
}
func (m *telegramMockUserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	return nil
}
func (m *telegramMockUserService) IncLoginAttempts(ctx context.Context, id string) error { return nil }
func (m *telegramMockUserService) ResetLoginAttempts(ctx context.Context, id string) error {
	return nil
}
func (m *telegramMockUserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return nil
}

var _ userService.IUserService = (*telegramMockUserService)(nil)

type telegramMockProfileService struct{ mock.Mock }

func (m *telegramMockProfileService) GetProfile(ctx context.Context, userID string) (*profileDomain.UserProfile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*profileDomain.UserProfile), args.Error(1)
}

var _ profileService.Service = (*telegramMockProfileService)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func init() { gin.SetMode(gin.TestMode) }

// setCurrentUser injects an AuthUser into gin context (simulating AuthMiddleware).
func setCurrentUser(userID uuid.UUID) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(middleware.UserKey, authDomain.AuthUser{
			ID:       userID,
			Username: "testuser@example.com",
			Role:     userDomain.UserRoleUser,
		})
		c.Next()
	}
}

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

// ---------------------------------------------------------------------------
// confirmLink tests
// ---------------------------------------------------------------------------

func TestConfirmLink_InvalidJSON_Returns400(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(uuid.New()), h.confirmLink)

	body := bytes.NewBufferString(`{invalid json}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConfirmLink_NoUserInContext_Returns401(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	router := gin.New()
	// No setCurrentUser middleware
	router.POST("/confirm-link", h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"123456"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestConfirmLink_InvalidOTP_Returns400(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(uuid.New()), h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"nonexistent"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConfirmLink_InvalidOTPPayload_Returns400(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	// Store invalid JSON in Redis
	mr.Set("telegram:link:my-otp", "not-json")

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(uuid.New()), h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"my-otp"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConfirmLink_TelegramAlreadyLinkedToOtherUser_Returns409(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	currentUserID := uuid.New()
	otherUserID := uuid.New()

	payload, _ := json.Marshal(telegramLinkPayload{ID: "12345", Username: "tguser"})
	mr.Set("telegram:link:valid-otp", string(payload))

	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "12345").
		Return(&userDomain.User{ID: otherUserID}, nil)

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(currentUserID), h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"valid-otp"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestConfirmLink_Success_LinksAccount(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	currentUserID := uuid.New()

	payload, _ := json.Marshal(telegramLinkPayload{ID: "12345", Username: "tguser"})
	mr.Set("telegram:link:valid-otp", string(payload))

	// telegram not linked to anyone
	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "12345").
		Return(nil, shared.ErrUserNotFound)
	usrSvc.On("LinkAccount", mock.Anything, currentUserID.String(), mock.MatchedBy(func(acc userDomain.LinkedAccount) bool {
		return acc.Provider == "telegram" && acc.ProviderID == "12345" && acc.Username == "@tguser"
	})).Return(nil)

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(currentUserID), h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"valid-otp"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	usrSvc.AssertExpectations(t)

	// Verify OTP was deleted (GetDel)
	assert.False(t, mr.Exists("telegram:link:valid-otp"))
}

func TestConfirmLink_SameUserRelink_Success(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	currentUserID := uuid.New()

	payload, _ := json.Marshal(telegramLinkPayload{ID: "12345", Username: ""})
	mr.Set("telegram:link:the-otp", string(payload))

	// Already linked to same user — should still succeed
	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "12345").
		Return(&userDomain.User{ID: currentUserID}, nil)
	usrSvc.On("LinkAccount", mock.Anything, currentUserID.String(), mock.Anything).Return(nil)

	router := gin.New()
	router.POST("/confirm-link", setCurrentUser(currentUserID), h.confirmLink)

	body := bytes.NewBufferString(`{"otp":"the-otp"}`)
	req := httptest.NewRequest(http.MethodPost, "/confirm-link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	usrSvc.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// unlink tests
// ---------------------------------------------------------------------------

func TestUnlink_InvalidJSON_Returns400(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	router := gin.New()
	router.DELETE("/link", setCurrentUser(uuid.New()), h.unlink)

	body := bytes.NewBufferString(`{bad}`)
	req := httptest.NewRequest(http.MethodDelete, "/link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUnlink_NoUserInContext_Returns401(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	router := gin.New()
	router.DELETE("/link", h.unlink)

	body := bytes.NewBufferString(`{"provider_id":"12345"}`)
	req := httptest.NewRequest(http.MethodDelete, "/link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestUnlink_Success(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	currentUserID := uuid.New()
	usrSvc.On("UnlinkAccount", mock.Anything, currentUserID.String(), "telegram", "12345").Return(nil)

	router := gin.New()
	router.DELETE("/link", setCurrentUser(currentUserID), h.unlink)

	body := bytes.NewBufferString(`{"provider_id":"12345"}`)
	req := httptest.NewRequest(http.MethodDelete, "/link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestUnlink_ServiceError_Returns500(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	currentUserID := uuid.New()
	usrSvc.On("UnlinkAccount", mock.Anything, currentUserID.String(), "telegram", "99").
		Return(shared.ErrInternal)

	router := gin.New()
	router.DELETE("/link", setCurrentUser(currentUserID), h.unlink)

	body := bytes.NewBufferString(`{"provider_id":"99"}`)
	req := httptest.NewRequest(http.MethodDelete, "/link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	usrSvc.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// getUserByTelegram tests
// ---------------------------------------------------------------------------

func TestGetUserByTelegram_UserNotFound_Returns404(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "999").
		Return(nil, shared.ErrUserNotFound)

	router := gin.New()
	router.GET("/user/:telegram_id", h.getUserByTelegram)

	req := httptest.NewRequest(http.MethodGet, "/user/999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestGetUserByTelegram_InternalError_Returns500(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	h := NewTelegramHandler(usrSvc, nil, rdb, nil, "")

	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "999").
		Return(nil, shared.ErrInternal)

	router := gin.New()
	router.GET("/user/:telegram_id", h.getUserByTelegram)

	req := httptest.NewRequest(http.MethodGet, "/user/999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	usrSvc.AssertExpectations(t)
}

func TestGetUserByTelegram_Success_ReturnsUserWithTelegramInfo(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	profSvc := &telegramMockProfileService{}
	h := NewTelegramHandler(usrSvc, profSvc, rdb, nil, "")

	userID := uuid.New()
	linkedAt := time.Now()
	user := &userDomain.User{
		ID:    userID,
		Email: "user@example.com",
		LinkedAccounts: []userDomain.LinkedAccount{
			{
				Provider:   "telegram",
				ProviderID: "12345",
				Username:   "@tguser",
				LinkedAt:   linkedAt,
			},
		},
	}
	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "12345").
		Return(user, nil)
	profSvc.On("GetProfile", mock.Anything, userID.String()).
		Return(&profileDomain.UserProfile{FullName: "Test User"}, nil)

	router := gin.New()
	router.GET("/user/:telegram_id", h.getUserByTelegram)

	req := httptest.NewRequest(http.MethodGet, "/user/12345", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, userID.String(), data["id"])
	assert.Equal(t, "user@example.com", data["email"])
	assert.Equal(t, "Test User", data["full_name"])

	tg, ok := data["telegram"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "telegram", tg["provider"])
	assert.Equal(t, "12345", tg["provider_id"])

	usrSvc.AssertExpectations(t)
	profSvc.AssertExpectations(t)
}

func TestGetUserByTelegram_NoLinkedTelegramAccount_ReturnsNilTelegram(t *testing.T) {
	_, rdb := newMiniRedis(t)
	usrSvc := &telegramMockUserService{}
	profSvc := &telegramMockProfileService{}
	h := NewTelegramHandler(usrSvc, profSvc, rdb, nil, "")

	userID := uuid.New()
	user := &userDomain.User{
		ID:             userID,
		Email:          "user@example.com",
		LinkedAccounts: nil, // no linked accounts
	}
	usrSvc.On("GetByLinkedAccount", mock.Anything, "telegram", "12345").
		Return(user, nil)
	profSvc.On("GetProfile", mock.Anything, userID.String()).
		Return(nil, shared.ErrNotFound)

	router := gin.New()
	router.GET("/user/:telegram_id", h.getUserByTelegram)

	req := httptest.NewRequest(http.MethodGet, "/user/12345", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	data := resp["data"].(map[string]interface{})
	assert.Nil(t, data["telegram"])

	usrSvc.AssertExpectations(t)
	profSvc.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// publishTelegramEvent tests
// ---------------------------------------------------------------------------

func TestPublishTelegramEvent_NilJetStream_NoOp(t *testing.T) {
	_, rdb := newMiniRedis(t)
	h := NewTelegramHandler(nil, nil, rdb, nil, "")

	// Should not panic when js is nil
	assert.NotPanics(t, func() {
		h.publishTelegramEvent("user.telegram.linked", "linked", uuid.NewString(), "12345")
	})
}
