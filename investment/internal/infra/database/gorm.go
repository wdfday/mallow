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

	// Rename legacy tables before AutoMigrate so existing data is preserved.
	db.Exec(`ALTER TABLE IF EXISTS portfolio_positions    RENAME TO positions`)
	db.Exec(`ALTER TABLE IF EXISTS derivative_positions   RENAME TO contract_positions`)
	db.Exec(`ALTER TABLE IF EXISTS portfolio_transactions RENAME TO trades`)
	db.Exec(`ALTER TABLE IF EXISTS portfolio_cash_flows   RENAME TO cash_flows`)
	db.Exec(`ALTER TABLE IF EXISTS portfolio_snapshots    RENAME TO account_snapshots`)

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
	)
	if err != nil {
		return nil, err
	}

	// Drop columns removed in this migration (AutoMigrate never drops columns).
	db.Exec(`ALTER TABLE positions         DROP COLUMN IF EXISTS asset_class`)
	db.Exec(`ALTER TABLE trades            DROP COLUMN IF EXISTS broker`)
	db.Exec(`ALTER TABLE trades            DROP COLUMN IF EXISTS trade_id`)

	// Unique constraints — rename old indexes to match new table names.
	db.Exec(`ALTER INDEX IF EXISTS uidx_portfolio_positions_account_symbol  RENAME TO uidx_positions_account_symbol`)
	db.Exec(`ALTER INDEX IF EXISTS uidx_portfolio_snapshots_account_date_type RENAME TO uidx_account_snapshots_account_date_type`)
	db.Exec(`ALTER INDEX IF EXISTS uidx_portfolio_transactions_external_id  RENAME TO uidx_trades_external_id`)
	db.Exec(`ALTER INDEX IF EXISTS uidx_portfolio_transactions_source_event RENAME TO uidx_trades_source_event`)
	db.Exec(`ALTER INDEX IF EXISTS uidx_portfolio_cash_flows_source_event   RENAME TO uidx_cash_flows_source_event`)
	db.Exec(`ALTER INDEX IF EXISTS uidx_derivative_positions_open_event     RENAME TO uidx_contract_positions_open_event`)
	db.Exec(`ALTER INDEX IF EXISTS idx_portfolio_transactions_account_date  RENAME TO idx_trades_account_date`)
	db.Exec(`ALTER INDEX IF EXISTS idx_portfolio_cash_flows_account_date    RENAME TO idx_cash_flows_account_date`)

	// Create indexes that don't exist yet (fresh installs or after rename).
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_events_seq
		ON investment_events (aggregate_id, sequence)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_positions_account_symbol
		ON positions (account_id, symbol)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_broker_connections_user_id
		ON broker_connections (user_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_accounts_user_id_created_at
		ON accounts (user_id, created_at DESC)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_account_snapshots_account_date_type
		ON account_snapshots (account_id, snapshot_date, snapshot_type)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_aggregate_snapshots_latest
		ON aggregate_snapshots (aggregate_id, sequence)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_trades_external_id
		ON trades (external_id) WHERE external_id IS NOT NULL AND external_id != ''`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_trades_source_event
		ON trades (source_event_id)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_cash_flows_source_event
		ON cash_flows (source_event_id)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uidx_contract_positions_open_event
		ON contract_positions (open_event_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_trades_account_date
		ON trades (account_id, tx_date)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_cash_flows_account_date
		ON cash_flows (account_id, occurred_at)`)

	logger.Info("Investment database migrated successfully")
	return db, nil
}
