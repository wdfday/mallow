package repository

import (
	"context"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)

	// Create table manually with SQLite-compatible schema.
	// linked_accounts is stored as a JSON text blob in SQLite (JSONB in Postgres).
	sqlStmt := `
	CREATE TABLE users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL,
		password TEXT,
		phone_number TEXT,
		google_id TEXT,
		role TEXT DEFAULT 'user',
		status TEXT DEFAULT 'pending_verification',
		email_verified BOOLEAN DEFAULT 0,
		email_verified_at DATETIME,
		mfa_enabled BOOLEAN DEFAULT 0,
		mfa_secret TEXT,
		last_login_at DATETIME,
		last_login_ip TEXT,
		login_attempts INTEGER DEFAULT 0,
		locked_until DATETIME,
		password_changed_at DATETIME,
		terms_accepted BOOLEAN DEFAULT 0,
		terms_accepted_at DATETIME,
		data_sharing_consent BOOLEAN DEFAULT 0,
		analytics_consent BOOLEAN DEFAULT 1,
		marketing_consent BOOLEAN DEFAULT 0,
		linked_accounts TEXT,
		last_active_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	);
	CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE deleted_at IS NULL;
	CREATE UNIQUE INDEX idx_users_phone ON users(phone_number) WHERE deleted_at IS NULL;
	CREATE UNIQUE INDEX idx_users_google_id ON users(google_id) WHERE deleted_at IS NULL AND google_id IS NOT NULL;
	CREATE INDEX idx_users_deleted_at ON users(deleted_at);
	CREATE INDEX idx_users_last_active_at ON users(last_active_at);

	CREATE TABLE user_profiles (
		id TEXT PRIMARY KEY,
		user_id TEXT,
		full_name TEXT,
		display_name TEXT,
		deleted_at DATETIME
	);
	`
	err = db.Exec(sqlStmt).Error
	require.NoError(t, err)

	return db
}

func createTestUser(email string) *domain.User {
	now := time.Now()
	return &domain.User{
		ID:               uuid.New(),
		Email:            email,
		Password:         "hashedpassword",
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusPendingVerification,
		EmailVerified:    false,
		MFAEnabled:       false,
		LoginAttempts:    0,
		TermsAccepted:    true,
		AnalyticsConsent: true,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func TestGormRepo_Create(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully create user", func(t *testing.T) {
		user := createTestUser("test@example.com")

		err := repo.Create(ctx, user)
		require.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, user.ID)
	})

	t.Run("fail on duplicate email", func(t *testing.T) {
		user1 := createTestUser("duplicate@example.com")
		err := repo.Create(ctx, user1)
		require.NoError(t, err)

		user2 := createTestUser("duplicate@example.com")
		err = repo.Create(ctx, user2)
		assert.Error(t, err)
	})
}

func TestGormRepo_GetByID(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully get user by ID", func(t *testing.T) {
		user := createTestUser("getbyid@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, user.Email, found.Email)
	})

	t.Run("return error for non-existent user", func(t *testing.T) {
		_, err := repo.GetByID(ctx, uuid.New().String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})

	t.Run("not return soft-deleted user", func(t *testing.T) {
		user := createTestUser("softdeleted@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.SoftDelete(ctx, user.ID.String())
		require.NoError(t, err)

		_, err = repo.GetByID(ctx, user.ID.String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_GetByEmail(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully get user by email", func(t *testing.T) {
		user := createTestUser("getbyemail@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		found, err := repo.GetByEmail(ctx, user.Email)
		require.NoError(t, err)
		assert.Equal(t, user.Email, found.Email)
	})

	t.Run("email lookup is case insensitive", func(t *testing.T) {
		user := createTestUser("caseinsensitive@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		found, err := repo.GetByEmail(ctx, "CASEINSENSITIVE@EXAMPLE.COM")
		require.NoError(t, err)
		assert.Equal(t, user.Email, found.Email)
	})

	t.Run("return error for non-existent email", func(t *testing.T) {
		_, err := repo.GetByEmail(ctx, "nonexistent@example.com")
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_List(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	// Create test users
	users := []*domain.User{
		createTestUser("user1@example.com"),
		createTestUser("user2@example.com"),
		createTestUser("admin@example.com"),
	}
	users[2].Role = domain.UserRoleAdmin
	users[2].Status = domain.UserStatusActive
	users[2].EmailVerified = true

	for _, u := range users {
		err := repo.Create(ctx, u)
		require.NoError(t, err)
	}

	t.Run("list all users with pagination", func(t *testing.T) {
		filter := domain.ListUsersFilter{}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(3), page.TotalItems)
		assert.Len(t, page.Data, 3)
	})

	t.Run("filter by role", func(t *testing.T) {
		adminRole := domain.UserRoleAdmin
		filter := domain.ListUsersFilter{Role: &adminRole}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(1), page.TotalItems)
		assert.Equal(t, domain.UserRoleAdmin, page.Data[0].Role)
	})

	t.Run("filter by status", func(t *testing.T) {
		activeStatus := domain.UserStatusActive
		filter := domain.ListUsersFilter{Status: &activeStatus}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(1), page.TotalItems)
		assert.Equal(t, domain.UserStatusActive, page.Data[0].Status)
	})

	t.Run("filter by email verified", func(t *testing.T) {
		verified := true
		filter := domain.ListUsersFilter{EmailVerified: &verified}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(1), page.TotalItems)
		assert.True(t, page.Data[0].EmailVerified)
	})

	t.Run("search by query", func(t *testing.T) {
		filter := domain.ListUsersFilter{Query: "admin"}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(1), page.TotalItems)
	})

	t.Run("pagination works correctly", func(t *testing.T) {
		filter := domain.ListUsersFilter{}
		pagination := shared.Pagination{Page: 1, PerPage: 2}

		page, err := repo.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(3), page.TotalItems)
		assert.Len(t, page.Data, 2)
		assert.Equal(t, 2, page.TotalPages)
	})
}

func TestGormRepo_Update(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully update user", func(t *testing.T) {
		user := createTestUser("update@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		// Update the user's last_active_at as a simple field update
		user.LoginAttempts = 5
		err = repo.Update(ctx, user)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, 5, found.LoginAttempts)
	})
}

func TestGormRepo_UpdateColumns(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully update specific columns", func(t *testing.T) {
		user := createTestUser("updatecols@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		updates := map[string]any{
			"login_attempts": 3,
		}
		err = repo.UpdateColumns(ctx, user.ID.String(), updates)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, 3, found.LoginAttempts)
	})

	t.Run("return error for non-existent user", func(t *testing.T) {
		updates := map[string]any{"login_attempts": 1}
		err := repo.UpdateColumns(ctx, uuid.New().String(), updates)
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_SoftDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully soft delete user", func(t *testing.T) {
		user := createTestUser("softdelete@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.SoftDelete(ctx, user.ID.String())
		require.NoError(t, err)

		_, err = repo.GetByID(ctx, user.ID.String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})

	t.Run("return error when soft deleting non-existent user", func(t *testing.T) {
		err := repo.SoftDelete(ctx, uuid.New().String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_Restore(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully restore soft-deleted user", func(t *testing.T) {
		user := createTestUser("restore@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		// Soft delete the user
		err = repo.SoftDelete(ctx, user.ID.String())
		require.NoError(t, err)

		// Verify user is soft deleted
		_, err = repo.GetByID(ctx, user.ID.String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)

		// Restore the user - need to use unscoped query in repository implementation
		err = repo.Restore(ctx, user.ID.String())
		require.NoError(t, err)

		// Verify user is restored and accessible again
		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, user.Email, found.Email)
	})

	t.Run("return error when restoring non-deleted user", func(t *testing.T) {
		user := createTestUser("notdeleted@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.Restore(ctx, user.ID.String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_HardDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully hard delete user", func(t *testing.T) {
		user := createTestUser("harddelete@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.HardDelete(ctx, user.ID.String())
		require.NoError(t, err)

		// Verify user is completely gone by trying to get it with unscoped
		var foundUser domain.User
		err = db.Unscoped().Where("id = ?", user.ID.String()).First(&foundUser).Error
		assert.Error(t, err)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("return error when hard deleting non-existent user", func(t *testing.T) {
		err := repo.HardDelete(ctx, uuid.New().String())
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
	})
}

func TestGormRepo_MarkEmailVerified(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully mark email as verified", func(t *testing.T) {
		user := createTestUser("verify@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		verifiedAt := time.Now()
		err = repo.MarkEmailVerified(ctx, user.ID.String(), verifiedAt)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.True(t, found.EmailVerified)
		assert.NotNil(t, found.EmailVerifiedAt)
	})
}

func TestGormRepo_LoginAttempts(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("increment login attempts", func(t *testing.T) {
		user := createTestUser("loginattempts@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.IncLoginAttempts(ctx, user.ID.String())
		require.NoError(t, err)

		err = repo.IncLoginAttempts(ctx, user.ID.String())
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, 2, found.LoginAttempts)
	})

	t.Run("reset login attempts", func(t *testing.T) {
		user := createTestUser("resetattempts@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		err = repo.IncLoginAttempts(ctx, user.ID.String())
		require.NoError(t, err)

		err = repo.ResetLoginAttempts(ctx, user.ID.String())
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, 0, found.LoginAttempts)
	})
}

func TestGormRepo_SetLockedUntil(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully set locked until", func(t *testing.T) {
		user := createTestUser("lock@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		lockTime := time.Now().Add(1 * time.Hour)
		err = repo.SetLockedUntil(ctx, user.ID.String(), &lockTime)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.NotNil(t, found.LockedUntil)
		assert.True(t, found.IsLocked())
	})

	t.Run("successfully unlock user", func(t *testing.T) {
		user := createTestUser("unlock@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		lockTime := time.Now().Add(1 * time.Hour)
		err = repo.SetLockedUntil(ctx, user.ID.String(), &lockTime)
		require.NoError(t, err)

		err = repo.SetLockedUntil(ctx, user.ID.String(), nil)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Nil(t, found.LockedUntil)
		assert.False(t, found.IsLocked())
	})
}

func TestGormRepo_UpdateLastLogin(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully update last login", func(t *testing.T) {
		user := createTestUser("lastlogin@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)

		loginTime := time.Now()
		loginIP := "192.168.1.1"
		err = repo.UpdateLastLogin(ctx, user.ID.String(), loginTime, &loginIP)
		require.NoError(t, err)

		found, err := repo.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.NotNil(t, found.LastLoginAt)
		assert.NotNil(t, found.LastLoginIP)
		assert.Equal(t, loginIP, *found.LastLoginIP)
	})
}

func TestGormRepo_Count(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	// Create test users
	for i := 0; i < 5; i++ {
		user := createTestUser("count" + string(rune(i)) + "@example.com")
		err := repo.Create(ctx, user)
		require.NoError(t, err)
	}

	t.Run("count all users", func(t *testing.T) {
		filter := domain.ListUsersFilter{}
		count, err := repo.Count(ctx, filter)
		require.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})
}
