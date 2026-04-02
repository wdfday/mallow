package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// UpdateColumns — PostgreSQL-specific (uses gorm.Expr("NOW()"))
// Covered by integration tests tagged //go:build integration
// ---------------------------------------------------------------------------

func TestProfileRepository_UpdateColumns_NotFound(t *testing.T) {
	t.Skip("UpdateColumns uses gorm.Expr(NOW()) — Postgres only, run with integration tag")
}

func TestProfileRepository_UpdateColumns_Success(t *testing.T) {
	t.Skip("UpdateColumns uses gorm.Expr(NOW()) — Postgres only, run with integration tag")
}

// ---------------------------------------------------------------------------
// GetByUserID — additional edge cases
// ---------------------------------------------------------------------------

func TestProfileRepository_GetByUserID_AfterDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	userID := uuid.New()
	profile := createTestProfile(userID)
	require.NoError(t, db.Create(profile).Error)

	// Manually delete the record.
	require.NoError(t, db.Exec("DELETE FROM user_profiles WHERE user_id = ?", userID.String()).Error)

	result, err := repo.GetByUserID(ctx, userID.String())
	assert.ErrorIs(t, err, shared.ErrNotFound)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// Create — empty optional fields
// ---------------------------------------------------------------------------

func TestProfileRepository_Create_MinimalProfile(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	userID := uuid.New()
	profile := &domain.UserProfile{
		ID:     uuid.New(),
		UserID: userID,
	}

	err := repo.Create(ctx, profile)
	assert.NoError(t, err)

	// Verify retrieval.
	result, err := repo.GetByUserID(ctx, userID.String())
	require.NoError(t, err)
	assert.Equal(t, userID, result.UserID)
	assert.Nil(t, result.Occupation)
}

// ---------------------------------------------------------------------------
// Update — verify field persistence
// ---------------------------------------------------------------------------

func TestProfileRepository_Update_AllFields(t *testing.T) {
	db := setupTestDB(t)
	repo := New(db)
	ctx := context.Background()

	userID := uuid.New()
	profile := createTestProfile(userID)
	require.NoError(t, db.Create(profile).Error)

	newIncome := 99000.0
	newOccupation := "Architect"
	profile.MonthlyIncomeAvg = &newIncome
	profile.Occupation = &newOccupation
	profile.RiskTolerance = domain.RiskToleranceAggressive

	err := repo.Update(ctx, profile)
	require.NoError(t, err)

	result, err := repo.GetByUserID(ctx, userID.String())
	require.NoError(t, err)
	assert.Equal(t, newIncome, *result.MonthlyIncomeAvg)
	assert.Equal(t, newOccupation, *result.Occupation)
	assert.Equal(t, domain.RiskToleranceAggressive, result.RiskTolerance)
}
