//go:build integration

package repository

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/user/domain"
)

// setupIntegrationDB connects to real Postgres using DSN from config (reads .env via Viper).
func setupIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_ = logger

	db, err := gorm.Open(postgres.Open(cfg.Database.URL), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Skipf("Postgres unreachable (%v) — run: docker compose -f identity/docker-compose.yml up -d", err)
	}

	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS citext`).Error)
	require.NoError(t, db.AutoMigrate(&domain.User{}))
	require.NoError(t, db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_users_linked_accounts
		ON users USING GIN (linked_accounts)
		WHERE deleted_at IS NULL
	`).Error)

	return db
}

func freshUser(t *testing.T, db *gorm.DB) *domain.User {
	t.Helper()
	now := time.Now()
	u := &domain.User{
		ID:               uuid.New(), // generate in Go so u.ID is populated after Create
		Email:            "integ+" + t.Name() + "@example.com",
		Password:         "hashed",
		FullName:         "Integration Tester",
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusPendingVerification,
		TermsAccepted:    true,
		AnalyticsConsent: true,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, db.Create(u).Error)
	t.Cleanup(func() { db.Unscoped().Delete(u) })
	return u
}

func TestIntegrationLinkAccount(t *testing.T) {
	db := setupIntegrationDB(t)
	repo := New(db)
	ctx := context.Background()

	u := freshUser(t, db)
	acc := domain.LinkedAccount{
		Provider:   "telegram",
		ProviderID: "9876543",
		Username:   "@integ_user",
		LinkedAt:   time.Now(),
	}

	err := repo.LinkAccount(ctx, u.ID.String(), acc)
	require.NoError(t, err)

	found, err := repo.GetByID(ctx, u.ID.String())
	require.NoError(t, err)
	require.Len(t, found.LinkedAccounts, 1)
	assert.Equal(t, "telegram", found.LinkedAccounts[0].Provider)
	assert.Equal(t, "9876543", found.LinkedAccounts[0].ProviderID)
}

func TestIntegrationGetByLinkedAccountFound(t *testing.T) {
	db := setupIntegrationDB(t)
	repo := New(db)
	ctx := context.Background()

	u := freshUser(t, db)
	providerID := "integ-discord-" + u.ID.String()[:8]

	err := repo.LinkAccount(ctx, u.ID.String(), domain.LinkedAccount{
		Provider:   "discord",
		ProviderID: providerID,
		LinkedAt:   time.Now(),
	})
	require.NoError(t, err)

	result, err := repo.GetByLinkedAccount(ctx, "discord", providerID)
	require.NoError(t, err)
	assert.Equal(t, u.ID, result.ID)
}

func TestIntegrationGetByLinkedAccountNotFound(t *testing.T) {
	db := setupIntegrationDB(t)
	repo := New(db)
	ctx := context.Background()

	result, err := repo.GetByLinkedAccount(ctx, "github", "nonexistent-999")
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestIntegrationUnlinkAccount(t *testing.T) {
	db := setupIntegrationDB(t)
	repo := New(db)
	ctx := context.Background()

	u := freshUser(t, db)
	providerID := "unlink-" + u.ID.String()[:8]

	require.NoError(t, repo.LinkAccount(ctx, u.ID.String(), domain.LinkedAccount{
		Provider:   "telegram",
		ProviderID: providerID,
		LinkedAt:   time.Now(),
	}))
	require.NoError(t, repo.LinkAccount(ctx, u.ID.String(), domain.LinkedAccount{
		Provider:   "discord",
		ProviderID: "keep-this",
		LinkedAt:   time.Now(),
	}))

	require.NoError(t, repo.UnlinkAccount(ctx, u.ID.String(), "telegram", providerID))

	found, err := repo.GetByID(ctx, u.ID.String())
	require.NoError(t, err)
	require.Len(t, found.LinkedAccounts, 1)
	assert.Equal(t, "discord", found.LinkedAccounts[0].Provider)
}
