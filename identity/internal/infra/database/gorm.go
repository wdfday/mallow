package database

import (
	"log/slog"
	"mallow/identity/internal/config"

	authRepository "mallow/identity/internal/module/auth/repository"
	profileDomain "mallow/identity/internal/module/profile/domain" //nolint
	userDomain "mallow/identity/internal/module/user/domain"

	"go.uber.org/fx"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Module provides database to fx.
var Module = fx.Module("database", fx.Provide(New))

// New creates a GORM database connection and auto-migrates all models.
func New(cfg *config.Config, logger *slog.Logger) (*gorm.DB, error) {
	logLevel := gormlogger.Warn
	if cfg.Server.GinMode == "debug" {
		logLevel = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.Database.URL), &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, err
	}

	logger.Info("Connected to PostgreSQL")

	// Auto-migrate all domain models.
	// token_blacklist is now PostgreSQL-persistent (Redis is cache only).
	// Verification tokens remain Redis-only (short-lived, no persistence needed).
	err = db.AutoMigrate(
		&userDomain.User{},
		&profileDomain.UserProfile{},
		&authRepository.TokenBlacklistEntry{},
	)
	if err != nil {
		return nil, err
	}

	// GIN index for fast JSONB containment queries on linked_accounts.
	if err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_users_linked_accounts
		ON users USING GIN (linked_accounts)
		WHERE deleted_at IS NULL
	`).Error; err != nil {
		return nil, err
	}

	logger.Info("Database migrated successfully")
	return db, nil
}
