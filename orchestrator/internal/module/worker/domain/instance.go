package domain

import (
	"time"

	"github.com/google/uuid"
)

// BotInstance is the persisted shape of a bot (ID, config, status). Used by repo.
// Runtime (Bot + Exchange) is attached in the service layer via OrchestratorRuntime.
type BotInstance struct {
	ID             string
	OrchestratorID uuid.UUID // → orchestrators.id (determines account + exchange)
	Config         BotConfig
	Status         string // "stopped" | "running"
	CreatedAt      time.Time
}
