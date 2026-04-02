package portfolio

import (
	"github.com/nats-io/nats.go"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/investment/internal/module/portfolio/command"
	"mallow/investment/internal/module/portfolio/consumer"
	"mallow/investment/internal/module/portfolio/projection"
	"mallow/investment/internal/module/portfolio/publisher"
	"mallow/investment/internal/module/portfolio/replay"
	"mallow/investment/internal/module/portfolio/store"
	portfoliosync "mallow/investment/internal/module/portfolio/sync"
)

// Module wires all portfolio event-sourcing components.
var Module = fx.Module("portfolio",
	fx.Provide(
		// EventStore
		func(db *gorm.DB) store.EventStore { return store.NewGORMStore(db) },

		// Aggregate SnapshotStore
		func(db *gorm.DB) store.SnapshotStore { return store.NewGORMSnapshotStore(db) },

		// PositionProjector
		func(db *gorm.DB) *projection.PositionProjector {
			return projection.NewPositionProjector(db)
		},

		// ProjectionRunner
		provideRunner,

		// EventPublisher
		func(js nats.JetStreamContext) publisher.EventPublisher {
			return publisher.NewJetStreamPublisher(js)
		},

		// CommandHandler
		command.NewHandler,

		// JetStream pull consumer
		consumer.New,

		// Portfolio sync subscriber (portfolio.synced.> → UpdateAvailableBalance)
		portfoliosync.New,

		// Replay service + HTTP handler
		replay.NewService,
		replay.NewHandler,
	),

	fx.Invoke(func(*consumer.Consumer) {}),
	fx.Invoke(func(*portfoliosync.Subscriber) {}),
)

func provideRunner(p *projection.PositionProjector, db *gorm.DB) *projection.Runner {
	return projection.NewRunner(
		p,
		projection.NewTransactionProjector(db),
		projection.NewCashFlowProjector(db),
		projection.NewDerivativeProjector(db),
		projection.NewSnapshotProjector(db),
	)
}
