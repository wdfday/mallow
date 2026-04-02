package database

import (
	"log/slog"

	"go.uber.org/fx"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"mallow/investment/internal/config"
	accountDomain "mallow/investment/internal/module/account/domain"
	brokerDomain "mallow/investment/internal/module/broker/domain"
	cashDomain "mallow/investment/internal/module/cash_flow/domain"
	derivDomain "mallow/investment/internal/module/derivative/domain"
	eventDomain "mallow/investment/internal/module/portfolio/event"
	"mallow/investment/internal/module/portfolio/store"
	positionDomain "mallow/investment/internal/module/position/domain"
	snapshotDomain "mallow/investment/internal/module/snapshot/domain"
	txDomain "mallow/investment/internal/module/transaction/domain"
	watchlistDomain "mallow/investment/internal/module/watchlist/domain"
)

var Module = fx.Module("database", fx.Provide(New))

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

	logger.Info("Connected to PostgreSQL (investment)")

	err = db.AutoMigrate(
		&eventDomain.InvestmentEvent{}, // event store — source of truth
		&store.AggregateSnapshot{},     // aggregate snapshots — load optimization
		&brokerDomain.BrokerConnection{},
		&accountDomain.Account{},
		&positionDomain.PortfolioPosition{},
		&txDomain.PortfolioTransaction{},
		&derivDomain.DerivativePosition{},
		&cashDomain.PortfolioCashFlow{},
		&snapshotDomain.PortfolioSnapshot{},
		&watchlistDomain.WatchlistItem{},
	)
	if err != nil {
		return nil, err
	}

	// Ensure unique constraints that GORM doesn't auto-create from tags alone
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_events_seq
		ON investment_events (aggregate_id, sequence)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_positions_account_symbol
		ON portfolio_positions (account_id, symbol)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_broker_connections_user_id
		ON broker_connections (user_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_accounts_user_id_created_at
		ON accounts (user_id, created_at DESC)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_snapshots_account_date_type
		ON portfolio_snapshots (account_id, snapshot_date, snapshot_type)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_watchlist_user_symbol
		ON investment_watchlist (user_id, symbol)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_aggregate_snapshots_latest
		ON aggregate_snapshots (aggregate_id, sequence)`)
	// Partial unique index: external_id deduplication for broker-synced transactions.
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_transactions_external_id
		ON portfolio_transactions (external_id) WHERE external_id IS NOT NULL AND external_id != ''`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_transactions_source_event
		ON portfolio_transactions (source_event_id)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_cash_flows_source_event
		ON portfolio_cash_flows (source_event_id)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_derivative_positions_open_event
		ON derivative_positions (open_event_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_portfolio_transactions_account_date
		ON portfolio_transactions (account_id, tx_date)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_portfolio_cash_flows_account_date
		ON portfolio_cash_flows (account_id, occurred_at)`)

	logger.Info("Investment database migrated successfully")
	return db, nil
}
