package infra

import (
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

// Module provides shared infra: NATS, Postgres, encryption, logger, and JetStream-backed perf logs.
var Module = fx.Module("infra",
	fx.Provide(infraNats.New),
	fx.Provide(infraNats.NewJetStream),
	fx.Provide(postgres.NewDB),
	fx.Provide(postgres.NewGORMDB),
	fx.Provide(newEncryptionService),
	fx.Provide(newLogger),
	fx.Provide(fx.Annotate(newEquityLog, fx.As(new(perf.EquityLog)))),
	fx.Provide(fx.Annotate(newTradeLog, fx.As(new(perf.TradeLog)))),
	fx.Provide(fx.Annotate(newPortfolioLog, fx.As(new(perf.PortfolioLog)))),
	fx.Provide(newFillLog),
)

func newLogger() *slog.Logger {
	return slog.Default()
}

func newEncryptionService(cfg *config.Config) (*internalService.EncryptionService, error) {
	return internalService.NewEncryptionService(cfg.Infra.EncryptionKey)
}

func newEquityLog(js natsgo.JetStreamContext) perf.EquityLog {
	l, err := perflog.NewEquityLog(js)
	if err != nil {
		slog.Warn("equity_log: JetStream init failed — equity points will not be persisted", "err", err)
		return nil
	}
	return l
}

func newTradeLog(js natsgo.JetStreamContext) perf.TradeLog {
	l, err := perflog.NewTradeLog(js)
	if err != nil {
		slog.Warn("trade_log: JetStream init failed — trade records will not be persisted", "err", err)
		return nil
	}
	return l
}

func newPortfolioLog(js natsgo.JetStreamContext) perf.PortfolioLog {
	l, err := perflog.NewPortfolioLog(js)
	if err != nil {
		slog.Warn("portfolio_log: JetStream init failed — portfolio snapshots will not be persisted", "err", err)
		return nil
	}
	return l
}

func newFillLog(js natsgo.JetStreamContext) *perflog.FillLog {
	l, err := perflog.NewFillLog(js)
	if err != nil {
		slog.Warn("fill_log: JetStream init failed — fill records will not be persisted", "err", err)
		return nil
	}
	return l
}
