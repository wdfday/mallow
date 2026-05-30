package infra

import (
	"database/sql"
	"log/slog"

	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"mallow/helm/internal/config"
	infraNats "mallow/helm/internal/infra/nats"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/postgres"
	"mallow/helm/internal/runtime/perf"
	internalService "mallow/helm/internal/service"
)

// Module provides shared infra: NATS, Postgres, encryption, logger, and perf logs.
var Module = fx.Module("infra",
	fx.Provide(infraNats.New),
	fx.Provide(infraNats.NewJetStream),
	fx.Provide(postgres.NewDB),
	fx.Provide(postgres.NewGORMDB),
	fx.Provide(newEncryptionService),
	fx.Provide(newLogger),
	fx.Provide(fx.Annotate(newTradeLog, fx.As(new(perf.TradeLog)))),
	fx.Provide(newFillLog),
)

func newLogger() *slog.Logger {
	return slog.Default()
}

func newEncryptionService(cfg *config.Config) (*internalService.EncryptionService, error) {
	return internalService.NewEncryptionService(cfg.Infra.EncryptionKey)
}

func newTradeLog(js natsgo.JetStreamContext) perf.TradeLog {
	l, err := perflog.NewTradeLog(js)
	if err != nil {
		slog.Warn("trade_log: JetStream init failed — trade records will not be persisted", "err", err)
		return nil
	}
	return l
}

func newFillLog(db *sql.DB) *perflog.FillLog {
	return perflog.NewFillLog(db)
}
