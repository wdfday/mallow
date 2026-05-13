package infra

import (
	"log/slog"

	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/fx"

	infraNats "mallow/helm/internal/infra/nats"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/postgres"
	"mallow/helm/internal/runtime/perf"
)

// Module provides shared infra: NATS, Postgres, and JetStream-backed perf logs.
var Module = fx.Module("infra",
	fx.Provide(infraNats.New),
	fx.Provide(infraNats.NewJetStream),
	fx.Provide(postgres.NewDB),
	fx.Provide(postgres.NewGORMDB),
	fx.Provide(fx.Annotate(newEquityLog, fx.As(new(perf.EquityLog)))),
	fx.Provide(fx.Annotate(newTradeLog, fx.As(new(perf.TradeLog)))),
)

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
