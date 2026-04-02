package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func createTestUser() *User {
	return &User{
		ID:            uuid.New(),
		Email:         "test@example.com",
		Password:      "hashed_password",
		FullName:      "Test User",
		Role:          UserRoleUser,
		Status:        UserStatusActive,
		EmailVerified: true,
		MFAEnabled:    false,
	}
}

func TestUser_TableName(t *testing.T) {
	user := User{}
	assert.Equal(t, "users", user.TableName())
}

func TestUser_IsAdmin(t *testing.T) {
	t.Run("user is admin", func(t *testing.T) {
		user := &User{Role: UserRoleAdmin}
		assert.True(t, user.IsAdmin())
	})

	t.Run("user is not admin", func(t *testing.T) {
		user := &User{Role: UserRoleUser}
		assert.False(t, user.IsAdmin())
	})
}

func TestUser_IsActive(t *testing.T) {
	t.Run("user is active", func(t *testing.T) {
		user := &User{Status: UserStatusActive}
		assert.True(t, user.IsActive())
	})

	t.Run("user is not active - pending verification", func(t *testing.T) {
		user := &User{Status: UserStatusPendingVerification}
		assert.False(t, user.IsActive())
	})

	t.Run("user is not active - suspended", func(t *testing.T) {
		user := &User{Status: UserStatusSuspended}
		assert.False(t, user.IsActive())
	})
}

func TestUser_IsPendingVerification(t *testing.T) {
	t.Run("user is pending verification", func(t *testing.T) {
		user := &User{Status: UserStatusPendingVerification}
		assert.True(t, user.IsPendingVerification())
	})

	t.Run("user is not pending verification", func(t *testing.T) {
		user := &User{Status: UserStatusActive}
		assert.False(t, user.IsPendingVerification())
	})
}

func TestUser_IsSuspended(t *testing.T) {
	t.Run("user is suspended", func(t *testing.T) {
		user := &User{Status: UserStatusSuspended}
		assert.True(t, user.IsSuspended())
	})

	t.Run("user is not suspended", func(t *testing.T) {
		user := &User{Status: UserStatusActive}
		assert.False(t, user.IsSuspended())
	})
}

func TestUser_IsEmailVerified(t *testing.T) {
	t.Run("email is verified", func(t *testing.T) {
		user := &User{EmailVerified: true}
		assert.True(t, user.IsEmailVerified())
	})

	t.Run("email is not verified", func(t *testing.T) {
		user := &User{EmailVerified: false}
		assert.False(t, user.IsEmailVerified())
	})
}

func TestUser_IsMFAEnabled(t *testing.T) {
	t.Run("MFA is enabled", func(t *testing.T) {
		user := &User{MFAEnabled: true}
		assert.True(t, user.IsMFAEnabled())
	})

	t.Run("MFA is not enabled", func(t *testing.T) {
		user := &User{MFAEnabled: false}
		assert.False(t, user.IsMFAEnabled())
	})
}

func TestUser_IsLocked(t *testing.T) {
	t.Run("user is not locked - LockedUntil is nil", func(t *testing.T) {
		user := &User{LockedUntil: nil}
		assert.False(t, user.IsLocked())
	})

	t.Run("user is locked - LockedUntil is in the future", func(t *testing.T) {
		future := time.Now().Add(1 * time.Hour)
		user := &User{LockedUntil: &future}
		assert.True(t, user.IsLocked())
	})

	t.Run("user is not locked - LockedUntil is in the past", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour)
		user := &User{LockedUntil: &past}
		assert.False(t, user.IsLocked())
	})
}

func TestUser_CanLogin(t *testing.T) {
	t.Run("user can login - active, verified, not locked", func(t *testing.T) {
		user := &User{
			Status:        UserStatusActive,
			EmailVerified: true,
			LockedUntil:   nil,
		}
		assert.True(t, user.CanLogin())
	})

	t.Run("user cannot login - locked", func(t *testing.T) {
		future := time.Now().Add(1 * time.Hour)
		user := &User{
			Status:        UserStatusActive,
			EmailVerified: true,
			LockedUntil:   &future,
		}
		assert.False(t, user.CanLogin())
	})

	t.Run("user cannot login - suspended", func(t *testing.T) {
		user := &User{
			Status:        UserStatusSuspended,
			EmailVerified: true,
			LockedUntil:   nil,
		}
		assert.False(t, user.CanLogin())
	})

	t.Run("user cannot login - email not verified", func(t *testing.T) {
		user := &User{
			Status:        UserStatusActive,
			EmailVerified: false,
			LockedUntil:   nil,
		}
		assert.False(t, user.CanLogin())
	})

	t.Run("user cannot login - multiple conditions", func(t *testing.T) {
		future := time.Now().Add(1 * time.Hour)
		user := &User{
			Status:        UserStatusSuspended,
			EmailVerified: false,
			LockedUntil:   &future,
		}
		assert.False(t, user.CanLogin())
	})
}

func TestUserRole_Constants(t *testing.T) {
	assert.Equal(t, UserRole("user"), UserRoleUser)
	assert.Equal(t, UserRole("admin"), UserRoleAdmin)
}

func TestUserStatus_Constants(t *testing.T) {
	assert.Equal(t, UserStatus("active"), UserStatusActive)
	assert.Equal(t, UserStatus("pending_verification"), UserStatusPendingVerification)
	assert.Equal(t, UserStatus("suspended"), UserStatusSuspended)
}

func TestListUsersFilter(t *testing.T) {
	role := UserRoleAdmin
	status := UserStatusActive
	verified := true

	filter := ListUsersFilter{
		Query:         "test",
		Role:          &role,
		Status:        &status,
		ActiveOnly:    true,
		EmailVerified: &verified,
	}

	assert.Equal(t, "test", filter.Query)
	assert.Equal(t, UserRoleAdmin, *filter.Role)
	assert.Equal(t, UserStatusActive, *filter.Status)
	assert.True(t, filter.ActiveOnly)
	assert.True(t, *filter.EmailVerified)
}
