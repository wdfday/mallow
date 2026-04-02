package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/repository"
	notificationservice "mallow/identity/internal/module/notification/service"
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ===========================================================================
// Shared helpers & mock for E2E flow tests
// ===========================================================================

// e2eUserService is a mock that tracks state changes for multi-step flows.
type e2eUserService struct {
	mock.Mock
	// In-memory user store keyed by ID, simulating real persistence.
	users map[string]*userdomain.User
}

func newE2EUserService() *e2eUserService {
	return &e2eUserService{users: make(map[string]*userdomain.User)}
}

func (m *e2eUserService) addUser(u *userdomain.User) {
	m.users[u.ID.String()] = u
}

func (m *e2eUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	for _, u := range m.users {
		if u.Email == email {
			return true, nil
		}
	}
	return false, nil
}

func (m *e2eUserService) Create(ctx context.Context, user *userdomain.User) (*userdomain.User, error) {
	user.ID = uuid.New()
	user.CreatedAt = time.Now()
	m.users[user.ID.String()] = user
	return user, nil
}

func (m *e2eUserService) GetByEmail(ctx context.Context, email string) (*userdomain.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, shared.ErrUserNotFound
}

func (m *e2eUserService) GetByID(ctx context.Context, id string) (*userdomain.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, shared.ErrUserNotFound
	}
	return u, nil
}

func (m *e2eUserService) IncLoginAttempts(ctx context.Context, userID string) error {
	if u, ok := m.users[userID]; ok {
		u.LoginAttempts++
	}
	return nil
}

func (m *e2eUserService) ResetLoginAttempts(ctx context.Context, userID string) error {
	if u, ok := m.users[userID]; ok {
		u.LoginAttempts = 0
		u.LockedUntil = nil
	}
	return nil
}

func (m *e2eUserService) SetLockedUntil(ctx context.Context, userID string, until *time.Time) error {
	if u, ok := m.users[userID]; ok {
		u.LockedUntil = until
	}
	return nil
}

func (m *e2eUserService) UpdateLastLogin(ctx context.Context, userID string, at time.Time, ip *string) error {
	if u, ok := m.users[userID]; ok {
		u.LastLoginAt = &at
		u.LastLoginIP = ip
	}
	return nil
}

func (m *e2eUserService) MarkEmailVerified(ctx context.Context, userID string, at time.Time) error {
	if u, ok := m.users[userID]; ok {
		u.EmailVerified = true
		u.EmailVerifiedAt = &at
		u.Status = userdomain.UserStatusActive
	}
	return nil
}

func (m *e2eUserService) UpdatePassword(ctx context.Context, id string, hash string) error {
	if u, ok := m.users[id]; ok {
		u.Password = hash
	}
	return nil
}

// Unused stubs to satisfy IUserService
func (m *e2eUserService) List(ctx context.Context, f userdomain.ListUsersFilter, p shared.Pagination) (shared.Page[userdomain.User], error) {
	return shared.Page[userdomain.User]{}, nil
}
func (m *e2eUserService) Update(ctx context.Context, u *userdomain.User) error { return nil }
func (m *e2eUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	return nil
}
func (m *e2eUserService) SoftDelete(ctx context.Context, id string) error { return nil }
func (m *e2eUserService) HardDelete(ctx context.Context, id string) error { return nil }
func (m *e2eUserService) Restore(ctx context.Context, id string) error    { return nil }
func (m *e2eUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*userdomain.User, error) {
	for _, u := range m.users {
		for _, la := range u.LinkedAccounts {
			if la.Provider == provider && la.ProviderID == providerID {
				return u, nil
			}
		}
	}
	return nil, shared.ErrUserNotFound
}
func (m *e2eUserService) LinkAccount(ctx context.Context, userID string, account userdomain.LinkedAccount) error {
	if u, ok := m.users[userID]; ok {
		u.LinkedAccounts = append(u.LinkedAccounts, account)
	}
	return nil
}
func (m *e2eUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	if u, ok := m.users[userID]; ok {
		filtered := make([]userdomain.LinkedAccount, 0, len(u.LinkedAccounts))
		for _, la := range u.LinkedAccounts {
			if !(la.Provider == provider && la.ProviderID == providerID) {
				filtered = append(filtered, la)
			}
		}
		u.LinkedAccounts = filtered
	}
	return nil
}

// capturingEmailService captures the last sent token for each type.
type capturingEmailService struct {
	LastVerificationToken string
	LastResetToken        string
}

func (c *capturingEmailService) SendVerificationEmail(_, _ string, token string) error {
	c.LastVerificationToken = token
	return nil
}
func (c *capturingEmailService) SendPasswordResetEmail(_, _ string, token string) error {
	c.LastResetToken = token
	return nil
}

var _ notificationservice.EmailService = (*capturingEmailService)(nil)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

func newTestJWTService(t *testing.T) *JWTService {
	t.Helper()
	svc, err := NewJWTService(
		"e2e-test-secret-key-minimum-length",
		"", "", "", "identity-e2e",
		1*time.Hour,    // access TTL
		7*24*time.Hour, // refresh TTL
	)
	require.NoError(t, err)
	return svc
}

// ===========================================================================
// E2E Flow: Forgot Password → Reset Password
// ===========================================================================

func TestE2E_ForgotPassword_ResetPassword(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)
	logger := slog.Default()

	// Real services
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()

	// Seed a user with known password
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, logger)
	originalHash, err := passwordSvc.HashPassword("OldP@ssw0rd!")
	require.NoError(t, err)

	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID:            userID,
		Email:         "user@example.com",
		FullName:      "Test User",
		Password:      originalHash,
		Role:          userdomain.UserRoleUser,
		Status:        userdomain.UserStatusActive,
		EmailVerified: true,
	})

	// Step 1: initiate forgot-password
	err = passwordSvc.ForgotPassword(ctx, "user@example.com", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	require.NotEmpty(t, emailSvc.LastResetToken, "email service should have captured reset token")
	resetToken := emailSvc.LastResetToken

	// Step 2: verify token exists in Redis
	stored, err := tokenRepo.GetByToken(ctx, resetToken)
	require.NoError(t, err)
	assert.Equal(t, userID.String(), stored.UserID)
	assert.Equal(t, "password_reset", stored.Type)
	assert.False(t, stored.IsExpired())

	// Step 3: reset password using the token
	err = passwordSvc.ResetPassword(ctx, resetToken, "NewStr0ng!Pass")
	require.NoError(t, err)

	// Step 4: verify old password no longer works
	user, _ := userSvc.GetByID(ctx, userID.String())
	assert.Error(t, passwordSvc.VerifyPassword(user.Password, "OldP@ssw0rd!"))

	// Step 5: verify new password works
	assert.NoError(t, passwordSvc.VerifyPassword(user.Password, "NewStr0ng!Pass"))

	// Step 6: verify token was invalidated (one-time use)
	_, err = tokenRepo.GetByToken(ctx, resetToken)
	assert.ErrorIs(t, err, shared.ErrTokenNotFound)
}

func TestE2E_ForgotPassword_NonexistentEmail_NoLeak(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, slog.Default())

	// ForgotPassword for non-existent email should succeed silently (no leak)
	err := passwordSvc.ForgotPassword(ctx, "nobody@example.com", "127.0.0.1", "agent")
	require.NoError(t, err)
	assert.Empty(t, emailSvc.LastResetToken, "no email should be sent for non-existent user")
}

func TestE2E_ForgotPassword_TokenReuse_Fails(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, slog.Default())

	userID := uuid.New()
	hash, _ := passwordSvc.HashPassword("MyP@ssw0rd!")
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "reuse@example.com", FullName: "Reuse User",
		Password: hash, Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})

	// Initiate, reset once
	_ = passwordSvc.ForgotPassword(ctx, "reuse@example.com", "1.2.3.4", "agent")
	token := emailSvc.LastResetToken
	require.NotEmpty(t, token)

	err := passwordSvc.ResetPassword(ctx, token, "First!Res3t")
	require.NoError(t, err)

	// Second use of the same token should fail
	err = passwordSvc.ResetPassword(ctx, token, "Second!Res3t")
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrTokenNotFound)
}

func TestE2E_ForgotPassword_NewTokenInvalidatesOld(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, slog.Default())

	userID := uuid.New()
	hash, _ := passwordSvc.HashPassword("MyP@ssw0rd!")
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "reissue@example.com", FullName: "Reissue User",
		Password: hash, Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})

	// First request
	_ = passwordSvc.ForgotPassword(ctx, "reissue@example.com", "1.2.3.4", "agent")
	firstToken := emailSvc.LastResetToken
	require.NotEmpty(t, firstToken)

	// Second request — should invalidate the first token
	_ = passwordSvc.ForgotPassword(ctx, "reissue@example.com", "1.2.3.4", "agent")
	secondToken := emailSvc.LastResetToken
	require.NotEmpty(t, secondToken)
	assert.NotEqual(t, firstToken, secondToken)

	// First token should no longer exist
	_, err := tokenRepo.GetByToken(ctx, firstToken)
	assert.ErrorIs(t, err, shared.ErrTokenNotFound)

	// Second token should still work
	err = passwordSvc.ResetPassword(ctx, secondToken, "Renewed!Pa55")
	assert.NoError(t, err)
}

// ===========================================================================
// E2E Flow: Email Verification (Send → Verify)
// ===========================================================================

func TestE2E_EmailVerification_SendAndVerify(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()

	verifSvc := NewVerificationService(tokenRepo, userSvc, tokenGen, emailSvc, slog.Default())

	// Seed unverified user
	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "newuser@example.com", FullName: "New User",
		Role: userdomain.UserRoleUser, Status: userdomain.UserStatusPendingVerification,
		EmailVerified: false,
	})

	// Step 1: Send verification email
	err := verifSvc.SendVerificationEmail(ctx, userID.String(), "127.0.0.1", "test-agent")
	require.NoError(t, err)
	require.NotEmpty(t, emailSvc.LastVerificationToken)
	token := emailSvc.LastVerificationToken

	// Step 2: Verify the token is in Redis
	stored, err := tokenRepo.GetByToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, userID.String(), stored.UserID)
	assert.Equal(t, "email_verification", stored.Type)

	// Step 3: Verify email using the token
	err = verifSvc.VerifyEmail(ctx, token)
	require.NoError(t, err)

	// Step 4: User should now be verified
	user, _ := userSvc.GetByID(ctx, userID.String())
	assert.True(t, user.EmailVerified)
	assert.NotNil(t, user.EmailVerifiedAt)
	assert.Equal(t, userdomain.UserStatusActive, user.Status)

	// Step 5: Token should be invalidated
	_, err = tokenRepo.GetByToken(ctx, token)
	assert.ErrorIs(t, err, shared.ErrTokenNotFound)
}

func TestE2E_EmailVerification_AlreadyVerified_Rejected(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()

	verifSvc := NewVerificationService(tokenRepo, userSvc, tokenGen, emailSvc, slog.Default())

	now := time.Now()
	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "verified@example.com", FullName: "Verified User",
		Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true, EmailVerifiedAt: &now,
	})

	err := verifSvc.SendVerificationEmail(ctx, userID.String(), "127.0.0.1", "agent")
	require.Error(t, err)
	assert.Empty(t, emailSvc.LastVerificationToken, "no email should be sent for already verified user")
}

func TestE2E_EmailVerification_ResendInvalidatesOldToken(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()

	verifSvc := NewVerificationService(tokenRepo, userSvc, tokenGen, emailSvc, slog.Default())

	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "resend@example.com", FullName: "Resend User",
		Role: userdomain.UserRoleUser, Status: userdomain.UserStatusPendingVerification,
		EmailVerified: false,
	})

	// First send
	err := verifSvc.SendVerificationEmail(ctx, userID.String(), "127.0.0.1", "agent")
	require.NoError(t, err)
	firstToken := emailSvc.LastVerificationToken

	// Resend
	err = verifSvc.ResendVerificationEmail(ctx, "resend@example.com", "127.0.0.1", "agent")
	require.NoError(t, err)
	secondToken := emailSvc.LastVerificationToken
	assert.NotEqual(t, firstToken, secondToken)

	// First token should be invalidated
	_, err = tokenRepo.GetByToken(ctx, firstToken)
	assert.ErrorIs(t, err, shared.ErrTokenNotFound)

	// Second token verifies successfully
	err = verifSvc.VerifyEmail(ctx, secondToken)
	assert.NoError(t, err)

	user, _ := userSvc.GetByID(ctx, userID.String())
	assert.True(t, user.EmailVerified)
}

func TestE2E_EmailVerification_ResendNonexistentEmail_NoLeak(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	userSvc := newE2EUserService()

	verifSvc := NewVerificationService(tokenRepo, userSvc, tokenGen, emailSvc, slog.Default())

	err := verifSvc.ResendVerificationEmail(ctx, "ghost@example.com", "127.0.0.1", "agent")
	require.NoError(t, err) // no leakage
	assert.Empty(t, emailSvc.LastVerificationToken)
}

// ===========================================================================
// E2E Flow: Register → Login → Refresh → Logout
// ===========================================================================

func TestE2E_AuthLifecycle_Register_Login_Refresh_Logout(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()

	// Use real password service (with noop email)
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())

	// --- Step 1: Register ---
	regResult, err := authSvc.Register(ctx, dto.RegisterRequest{
		Email:    "lifecycle@example.com",
		Password: "MyS3cure!Pass",
		FullName: "Lifecycle User",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, regResult.AccessToken)
	assert.NotEmpty(t, regResult.RefreshToken)
	assert.Equal(t, "lifecycle@example.com", regResult.User.Email)

	userID := regResult.User.ID.String()

	// --- Step 2: Login ---
	loginResult, err := authSvc.Login(ctx, dto.LoginRequest{
		Email:    "lifecycle@example.com",
		Password: "MyS3cure!Pass",
		IP:       "10.0.0.1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, loginResult.AccessToken)
	assert.NotEmpty(t, loginResult.RefreshToken)
	loginRefreshToken := loginResult.RefreshToken

	// Verify last login was recorded
	user, _ := userSvc.GetByID(ctx, userID)
	assert.NotNil(t, user.LastLoginAt)

	// --- Step 3: Refresh token ---
	tokenResp, err := authSvc.RefreshToken(ctx, loginRefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, tokenResp.AccessToken)
	assert.Equal(t, "Bearer", tokenResp.TokenType)

	// --- Step 4: Logout (blacklist refresh token) ---
	err = authSvc.Logout(ctx, userID, loginRefreshToken, "10.0.0.1")
	require.NoError(t, err)

	// --- Step 5: Refresh with blacklisted token should fail ---
	_, err = authSvc.RefreshToken(ctx, loginRefreshToken)
	require.Error(t, err)
}

func TestE2E_Register_DuplicateEmail_Rejected(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())

	req := dto.RegisterRequest{
		Email: "dup@example.com", Password: "MyS3cure!Pass", FullName: "First",
	}
	_, err := authSvc.Register(ctx, req)
	require.NoError(t, err)

	// Second register with same email should fail
	_, err = authSvc.Register(ctx, req)
	require.Error(t, err)
}

// ===========================================================================
// E2E Flow: Account Lockout after failed logins
// ===========================================================================

func TestE2E_AccountLockout_AfterFailedLogins(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())

	// Register user first
	_, err := authSvc.Register(ctx, dto.RegisterRequest{
		Email: "lockout@example.com", Password: "MyS3cure!Pass", FullName: "Lockout User",
	})
	require.NoError(t, err)

	// 5 failed login attempts (wrong password)
	for i := 0; i < 5; i++ {
		_, err := authSvc.Login(ctx, dto.LoginRequest{
			Email: "lockout@example.com", Password: "WrongP@ss1!", IP: "1.1.1.1",
		})
		assert.Error(t, err)
	}

	// Account should now be locked — even correct password fails
	_, err = authSvc.Login(ctx, dto.LoginRequest{
		Email: "lockout@example.com", Password: "MyS3cure!Pass", IP: "1.1.1.1",
	})
	require.Error(t, err)

	// Verify user is locked
	user, _ := userSvc.GetByEmail(ctx, "lockout@example.com")
	assert.NotNil(t, user.LockedUntil)
	assert.True(t, time.Now().Before(*user.LockedUntil))
}

// ===========================================================================
// E2E Flow: Password Reset unlocks a locked account
// ===========================================================================

func TestE2E_PasswordReset_UnlocksAccount(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())

	// Register then lock via failed logins
	_, err := authSvc.Register(ctx, dto.RegisterRequest{
		Email: "unlock@example.com", Password: "MyS3cure!Pass", FullName: "Unlock User",
	})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, _ = authSvc.Login(ctx, dto.LoginRequest{
			Email: "unlock@example.com", Password: "Bad!Pass1234", IP: "1.1.1.1",
		})
	}

	user, _ := userSvc.GetByEmail(ctx, "unlock@example.com")
	require.NotNil(t, user.LockedUntil, "account should be locked")

	// Forgot password → reset
	err = passwordSvc.ForgotPassword(ctx, "unlock@example.com", "127.0.0.1", "agent")
	require.NoError(t, err)
	token := emailSvc.LastResetToken

	err = passwordSvc.ResetPassword(ctx, token, "Unlocked!P@ss1")
	require.NoError(t, err)

	// After reset, login attempts should be 0 and account unlocked
	user, _ = userSvc.GetByEmail(ctx, "unlock@example.com")
	assert.Equal(t, 0, user.LoginAttempts)
	assert.Nil(t, user.LockedUntil)

	// Login with new password should succeed
	_, err = authSvc.Login(ctx, dto.LoginRequest{
		Email: "unlock@example.com", Password: "Unlocked!P@ss1", IP: "10.0.0.1",
	})
	assert.NoError(t, err)
}

// ===========================================================================
// E2E Flow: Register → Verify Email → Login (full onboarding)
// ===========================================================================

func TestE2E_FullOnboarding_Register_VerifyEmail_Login(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	emailSvc := &capturingEmailService{}
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, emailSvc, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())
	verifSvc := NewVerificationService(tokenRepo, userSvc, tokenGen, emailSvc, slog.Default())

	// --- Step 1: Register ---
	regResult, err := authSvc.Register(ctx, dto.RegisterRequest{
		Email: "onboard@example.com", Password: "Onb0ard!Pass", FullName: "Onboard User",
	})
	require.NoError(t, err)
	userID := regResult.User.ID.String()

	// User is pending verification
	user, _ := userSvc.GetByID(ctx, userID)
	assert.Equal(t, userdomain.UserStatusPendingVerification, user.Status)
	assert.False(t, user.EmailVerified)

	// --- Step 2: Send verification email ---
	err = verifSvc.SendVerificationEmail(ctx, userID, "127.0.0.1", "agent")
	require.NoError(t, err)
	verifToken := emailSvc.LastVerificationToken
	require.NotEmpty(t, verifToken)

	// --- Step 3: Verify email ---
	err = verifSvc.VerifyEmail(ctx, verifToken)
	require.NoError(t, err)

	user, _ = userSvc.GetByID(ctx, userID)
	assert.True(t, user.EmailVerified)
	assert.Equal(t, userdomain.UserStatusActive, user.Status)

	// --- Step 4: Login successfully ---
	loginResult, err := authSvc.Login(ctx, dto.LoginRequest{
		Email: "onboard@example.com", Password: "Onb0ard!Pass", IP: "10.0.0.1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, loginResult.AccessToken)
}

// ===========================================================================
// E2E Flow: Change Password (authenticated)
// ===========================================================================

func TestE2E_ChangePassword(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	userSvc := newE2EUserService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	// Seed user
	userID := uuid.New()
	hash, _ := passwordSvc.HashPassword("Original!P@ss1")
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "changepw@example.com", FullName: "Chang PW",
		Password: hash, Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})

	// Change password with correct current password
	err := passwordSvc.ChangePassword(ctx, userID.String(), dto.ChangePasswordRequest{
		CurrentPassword: "Original!P@ss1",
		NewPassword:     "Updated!P@ss2",
	})
	require.NoError(t, err)

	// Old password should not work
	user, _ := userSvc.GetByID(ctx, userID.String())
	assert.Error(t, passwordSvc.VerifyPassword(user.Password, "Original!P@ss1"))

	// New password works
	assert.NoError(t, passwordSvc.VerifyPassword(user.Password, "Updated!P@ss2"))
}

func TestE2E_ChangePassword_WrongCurrent_Fails(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	userSvc := newE2EUserService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	userID := uuid.New()
	hash, _ := passwordSvc.HashPassword("Correct!P@ss1")
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "wrongcur@example.com", FullName: "Wrong Cur",
		Password: hash, Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})

	err := passwordSvc.ChangePassword(ctx, userID.String(), dto.ChangePasswordRequest{
		CurrentPassword: "WrongCurrent!1",
		NewPassword:     "NewPassword!1",
	})
	require.Error(t, err)

	// Password should remain unchanged
	user, _ := userSvc.GetByID(ctx, userID.String())
	assert.NoError(t, passwordSvc.VerifyPassword(user.Password, "Correct!P@ss1"))
}

// ===========================================================================
// E2E Flow: Logout → cannot refresh
// ===========================================================================

func TestE2E_Logout_BlacklistsRefreshToken(t *testing.T) {
	ctx := context.Background()
	_, rdb := newTestRedis(t)

	jwtSvc := newTestJWTService(t)
	blacklistRepo := repository.NewTokenBlacklistRepository(nil, rdb)
	userSvc := newE2EUserService()
	tokenRepo := repository.NewTokenRepository(rdb)
	tokenGen := NewTokenService()
	passwordSvc := NewPasswordService(userSvc, tokenRepo, tokenGen, nil, slog.Default())

	authSvc := NewService(userSvc, jwtSvc, passwordSvc, nil, blacklistRepo, &config.Config{}, slog.Default())

	// Register
	regResult, err := authSvc.Register(ctx, dto.RegisterRequest{
		Email: "logout@example.com", Password: "MyS3cure!Pass", FullName: "Logout User",
	})
	require.NoError(t, err)

	refreshToken := regResult.RefreshToken
	userID := regResult.User.ID.String()

	// Refresh works before logout
	_, err = authSvc.RefreshToken(ctx, refreshToken)
	require.NoError(t, err)

	// Logout
	err = authSvc.Logout(ctx, userID, refreshToken, "10.0.0.1")
	require.NoError(t, err)

	// Refresh should fail after logout
	_, err = authSvc.RefreshToken(ctx, refreshToken)
	require.Error(t, err)

	// Verify token is in blacklist
	isBlacklisted, err := blacklistRepo.IsBlacklisted(ctx, refreshToken)
	require.NoError(t, err)
	assert.True(t, isBlacklisted)
}

// ===========================================================================
// E2E Flow: Telegram Link → Confirm → Unlink
// ===========================================================================

func TestE2E_TelegramLink_Confirm_Unlink(t *testing.T) {
	_, rdb := newTestRedis(t)
	userSvc := newE2EUserService()

	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "tg@example.com", FullName: "TG User",
		Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})

	// Step 1: Simulate OTP storage in Redis (as strategist/bot would)
	ctx := context.Background()
	otp := "123456"
	otpKey := "telegram:link_otp:" + otp
	payload := `{"telegram_id":"999888","telegram_username":"@tguser"}`
	err := rdb.Set(ctx, otpKey, payload, 5*time.Minute).Err()
	require.NoError(t, err)

	// Step 2: Verify OTP exists in Redis
	val, err := rdb.Get(ctx, otpKey).Result()
	require.NoError(t, err)
	assert.Equal(t, payload, val)

	// Step 3: Link the account (simulating what confirm-link handler does)
	err = userSvc.LinkAccount(ctx, userID.String(), userdomain.LinkedAccount{
		Provider:   "telegram",
		ProviderID: "999888",
		Username:   "@tguser",
		LinkedAt:   time.Now(),
	})
	require.NoError(t, err)

	// Delete OTP after use
	rdb.Del(ctx, otpKey)

	// Step 4: Verify user has the linked account
	user, _ := userSvc.GetByID(ctx, userID.String())
	require.Len(t, user.LinkedAccounts, 1)
	assert.Equal(t, "telegram", user.LinkedAccounts[0].Provider)
	assert.Equal(t, "999888", user.LinkedAccounts[0].ProviderID)

	// Step 5: Can look up user by linked account
	found, err := userSvc.GetByLinkedAccount(ctx, "telegram", "999888")
	require.NoError(t, err)
	assert.Equal(t, userID, found.ID)

	// Step 6: Unlink
	err = userSvc.UnlinkAccount(ctx, userID.String(), "telegram", "999888")
	require.NoError(t, err)

	user, _ = userSvc.GetByID(ctx, userID.String())
	assert.Empty(t, user.LinkedAccounts)

	// Step 7: Lookup after unlink should fail
	_, err = userSvc.GetByLinkedAccount(ctx, "telegram", "999888")
	assert.ErrorIs(t, err, shared.ErrUserNotFound)
}

// ===========================================================================
// E2E Flow: Telegram re-link (same user)
// ===========================================================================

func TestE2E_TelegramRelink_SameUser(t *testing.T) {
	_, rdb := newTestRedis(t)
	_ = rdb // Redis is available but we only use in-memory user service here
	userSvc := newE2EUserService()

	userID := uuid.New()
	userSvc.addUser(&userdomain.User{
		ID: userID, Email: "relink@example.com", FullName: "Relink User",
		Role: userdomain.UserRoleUser, Status: userdomain.UserStatusActive,
		EmailVerified: true,
	})
	ctx := context.Background()

	// Link first time
	err := userSvc.LinkAccount(ctx, userID.String(), userdomain.LinkedAccount{
		Provider: "telegram", ProviderID: "111222", Username: "@first", LinkedAt: time.Now(),
	})
	require.NoError(t, err)

	// Unlink
	err = userSvc.UnlinkAccount(ctx, userID.String(), "telegram", "111222")
	require.NoError(t, err)

	// Link with different telegram account
	err = userSvc.LinkAccount(ctx, userID.String(), userdomain.LinkedAccount{
		Provider: "telegram", ProviderID: "333444", Username: "@second", LinkedAt: time.Now(),
	})
	require.NoError(t, err)

	user, _ := userSvc.GetByID(ctx, userID.String())
	require.Len(t, user.LinkedAccounts, 1)
	assert.Equal(t, "333444", user.LinkedAccounts[0].ProviderID)
}
