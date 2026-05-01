package database

import (
	"log/slog"
	"mallow/identity/internal/config"

	"go.uber.org/fx"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Module provides database to fx.
var Module = fx.Module("database", fx.Provide(New))

// New creates a GORM database connection.
// Schema is managed via deployment/postgres/identity_schema.sql — no AutoMigrate.
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
	return db, nil
}
