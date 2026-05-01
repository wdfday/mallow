package seed

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/config"
	authdto "mallow/identity/internal/module/auth/dto"
	authService "mallow/identity/internal/module/auth/service"
	profiledomain "mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	profileService "mallow/identity/internal/module/profile/service"
	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockUserService struct{ mock.Mock }

func (m *mockUserService) Create(ctx context.Context, u *userDomain.User) (*userDomain.User, error) {
	args := m.Called(ctx, u)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*userDomain.User), args.Error(1)
}
func (m *mockUserService) GetByID(ctx context.Context, id string) (*userDomain.User, error) {
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
func (m *mockUserService) SoftDelete(ctx context.Context, id string) error         { return nil }
func (m *mockUserService) HardDelete(ctx context.Context, id string) error         { return nil }
func (m *mockUserService) Restore(ctx context.Context, id string) error            { return nil }
func (m *mockUserService) IncLoginAttempts(ctx context.Context, id string) error   { return nil }
func (m *mockUserService) ResetLoginAttempts(ctx context.Context, id string) error { return nil }
func (m *mockUserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return nil
}
func (m *mockUserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
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
func (m *mockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	args := m.Called(ctx, email)
	return args.Bool(0), args.Error(1)
}

var _ userService.IUserService = (*mockUserService)(nil)

type mockPasswordService struct{ mock.Mock }

func (m *mockPasswordService) HashPassword(password string) (string, error) {
	args := m.Called(password)
	return args.String(0), args.Error(1)
}
func (m *mockPasswordService) VerifyPassword(hash, password string) error { return nil }
func (m *mockPasswordService) IsValidPassword(password string) bool       { return true }
func (m *mockPasswordService) ValidatePasswordStrength(password string) []string {
	return nil
}
func (m *mockPasswordService) ChangePassword(ctx context.Context, userID string, req authdto.ChangePasswordRequest) error {
	return nil
}
func (m *mockPasswordService) ForgotPassword(ctx context.Context, email, ip, ua string) error {
	return nil
}
func (m *mockPasswordService) ResetPassword(ctx context.Context, token, newPass string) error {
	return nil
}

var _ authService.IPasswordService = (*mockPasswordService)(nil)

type mockProfileService struct{ mock.Mock }

func (m *mockProfileService) CreateProfile(ctx context.Context, userID string, req profiledto.CreateProfileRequest) (*profiledomain.UserProfile, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*profiledomain.UserProfile), args.Error(1)
}
func (m *mockProfileService) CreateDefaultProfile(ctx context.Context, userID string) (*profiledomain.UserProfile, error) {
	return nil, nil
}
func (m *mockProfileService) GetProfile(ctx context.Context, userID string) (*profiledomain.UserProfile, error) {
	return nil, nil
}
func (m *mockProfileService) UpdateProfile(ctx context.Context, userID string, req profiledto.UpdateProfileRequest) (*profiledomain.UserProfile, error) {
	return nil, nil
}

var _ profileService.Service = (*mockProfileService)(nil)

// suppress unused import warnings
var _ = assert.Equal

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSeedAdmin_EmptyEmail_SkipsSeeding(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "", Password: "secret"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertNotCalled(t, "ExistsByEmail")
	pwdSvc.AssertNotCalled(t, "HashPassword")
}

func TestSeedAdmin_EmptyPassword_SkipsSeeding(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "admin@test.com", Password: ""},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertNotCalled(t, "ExistsByEmail")
}

func TestSeedAdmin_AdminAlreadyExists_Skips(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(true, nil)

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "admin@test.com", Password: "SuperPass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertExpectations(t)
	pwdSvc.AssertNotCalled(t, "HashPassword")
	usrSvc.AssertNotCalled(t, "Create")
}

func TestSeedAdmin_ExistsByEmailError_Skips(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(false, errors.New("db error"))

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "admin@test.com", Password: "SuperPass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertExpectations(t)
	pwdSvc.AssertNotCalled(t, "HashPassword")
}

func TestSeedAdmin_HashPasswordError_Skips(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(false, nil)
	pwdSvc.On("HashPassword", "SuperPass1!").Return("", errors.New("hash error"))

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "admin@test.com", Password: "SuperPass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertExpectations(t)
	pwdSvc.AssertExpectations(t)
	usrSvc.AssertNotCalled(t, "Create")
}

func TestSeedAdmin_Success_CreatesAdminUser(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	createdUser := &userDomain.User{Email: "admin@test.com", Role: userDomain.UserRoleAdmin}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(false, nil)
	pwdSvc.On("HashPassword", "SuperPass1!").Return("$2a$hashed", nil)
	usrSvc.On("Create", mock.Anything, mock.MatchedBy(func(u *userDomain.User) bool {
		return u.Email == "admin@test.com" &&
			u.Password == "$2a$hashed" &&
			u.Role == userDomain.UserRoleAdmin &&
			u.Status == userDomain.UserStatusActive &&
			u.EmailVerified
	})).Return(createdUser, nil)
	profSvc.On("CreateProfile", mock.Anything, mock.Anything, mock.Anything).Return(&profiledomain.UserProfile{}, nil)

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "  Admin@Test.Com  ", Password: "SuperPass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertExpectations(t)
	pwdSvc.AssertExpectations(t)
	profSvc.AssertExpectations(t)
}

func TestSeedAdmin_CreateError_LogsAndReturns(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(false, nil)
	pwdSvc.On("HashPassword", "Pass1!").Return("$2a$hashed", nil)
	usrSvc.On("Create", mock.Anything, mock.Anything).Return(nil, errors.New("create failed"))

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "admin@test.com", Password: "Pass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	// Should not panic, just log the error
	usrSvc.AssertExpectations(t)
	pwdSvc.AssertExpectations(t)
}

func TestSeedAdmin_EmailNormalization(t *testing.T) {
	usrSvc := &mockUserService{}
	pwdSvc := &mockPasswordService{}
	profSvc := &mockProfileService{}

	usrSvc.On("ExistsByEmail", mock.Anything, "admin@test.com").Return(true, nil)

	SeedAdmin(Params{
		Config: &config.Config{
			AdminSeed: config.AdminSeedConfig{Email: "  ADMIN@Test.Com  ", Password: "Pass1!"},
		},
		UserService:     usrSvc,
		ProfileService:  profSvc,
		PasswordService: pwdSvc,
		Logger:          slog.Default(),
	})

	usrSvc.AssertCalled(t, "ExistsByEmail", mock.Anything, "admin@test.com")
}
