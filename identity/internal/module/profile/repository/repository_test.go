package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"
)

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Create tables manually for SQLite (avoiding PostgreSQL-specific UUID syntax)
	err = db.Exec(`
		CREATE TABLE IF NOT EXISTS user_profiles (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL UNIQUE,
			occupation TEXT,
			industry TEXT,
			employer TEXT,
			dependents_count INTEGER,
			marital_status TEXT,
			monthly_income_avg REAL,
			emergency_fund_months REAL,
			debt_to_income_ratio REAL,
			credit_score INTEGER,
			income_stability TEXT,
			risk_tolerance TEXT DEFAULT 'moderate',
			investment_horizon TEXT DEFAULT 'medium',
			investment_experience TEXT DEFAULT 'beginner',
			budget_method TEXT DEFAULT 'custom',
			alert_threshold_budget REAL,
			report_frequency TEXT,
			currency_primary TEXT DEFAULT 'VND',
			currency_secondary TEXT DEFAULT 'USD',
			settings TEXT,
			preferred_report_day_of_month INTEGER,
			onboarding_completed INTEGER DEFAULT 0,
			onboarding_completed_at DATETIME,
			primary_goal TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)
	`).Error
	require.NoError(t, err)

	return db
}

func createTestProfile(userID uuid.UUID) *domain.UserProfile {
	occupation := "Software Engineer"
	industry := "Technology"
	monthlyIncome := 5000.0
	return &domain.UserProfile{
		ID:               uuid.New(),
		UserID:           userID,
		Occupation:       &occupation,
		Industry:         &industry,
		MonthlyIncomeAvg: &monthlyIncome,
		CurrencyPrimary:  "VND",
	}
}

// ==================== Repository Tests ====================

func TestProfileRepository_Create(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully create profile", func(t *testing.T) {
		userID := uuid.New()
		profile := createTestProfile(userID)

		err := repo.Create(ctx, profile)
		assert.NoError(t, err)

		// Verify profile was created
		var result domain.UserProfile
		err = db.First(&result, "user_id = ?", userID).Error
		assert.NoError(t, err)
		assert.Equal(t, userID, result.UserID)
	})

	t.Run("create duplicate profile fails", func(t *testing.T) {
		userID := uuid.New()
		profile1 := createTestProfile(userID)

		err := repo.Create(ctx, profile1)
		require.NoError(t, err)

		// Try to create another profile with same user ID
		profile2 := createTestProfile(userID)
		err = repo.Create(ctx, profile2)
		assert.Error(t, err) // Should fail due to unique constraint
	})
}

func TestProfileRepository_GetByUserID(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully get profile", func(t *testing.T) {
		userID := uuid.New()
		profile := createTestProfile(userID)
		require.NoError(t, db.Create(profile).Error)

		result, err := repo.GetByUserID(ctx, userID.String())
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, userID, result.UserID)
	})

	t.Run("profile not found", func(t *testing.T) {
		result, err := repo.GetByUserID(ctx, uuid.New().String())
		assert.Error(t, err)
		assert.True(t, err == shared.ErrNotFound)
		assert.Nil(t, result)
	})
}

func TestProfileRepository_Update(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	t.Run("successfully update profile", func(t *testing.T) {
		userID := uuid.New()
		profile := createTestProfile(userID)
		require.NoError(t, db.Create(profile).Error)

		// Update the profile
		newIncome := 10000.0
		profile.MonthlyIncomeAvg = &newIncome
		err := repo.Update(ctx, profile)
		assert.NoError(t, err)

		// Verify update
		var result domain.UserProfile
		require.NoError(t, db.First(&result, "user_id = ?", userID).Error)
		assert.Equal(t, newIncome, *result.MonthlyIncomeAvg)
	})
}

func TestProfileRepository_UpdateColumns(t *testing.T) {
	// NOTE: These tests are skipped because the repository uses gorm.Expr("NOW()")
	// which is PostgreSQL-specific and not supported by SQLite.
	// For proper testing, use testcontainers with PostgreSQL or mock the repository.
	t.Skip("Skipping UpdateColumns tests - uses PostgreSQL NOW() function not supported by SQLite")
}
