package infra

import (
	"go.uber.org/fx"

	"orchestrator/internal/infra/nats"
	"orchestrator/internal/infra/postgres"
)

// Module provides shared infra: NATS, optional Postgres.
// *sql.DB and *gorm.DB are both nil when POSTGRES_URL is empty.
var Module = fx.Module("infra",
	fx.Provide(nats.New),
	fx.Provide(nats.NewJetStream),
	fx.Provide(postgres.NewDB),
	fx.Provide(postgres.NewGORMDB),
)
