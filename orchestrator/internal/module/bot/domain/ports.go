package domain

// BotRepo is the port for persisting and retrieving bot instances.
// Implementations live in repo (memory) or infra (e.g. DB). Domain owns the contract.
type BotRepo interface {
	GenerateID() string
	Save(instance *BotInstance) error
	Get(id string) (*BotInstance, error)
	All() []*BotInstance
	Delete(id string) error
	Update(id string, fn func(*BotInstance) error) error
}
