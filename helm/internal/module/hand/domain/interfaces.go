package domain

import "github.com/google/uuid"

// HandRepo is the port for persisting and retrieving hand instances.
// Implementations live in infra/postgres. Domain owns the contract.
type HandRepo interface {
	GenerateID() uuid.UUID
	Save(instance *Hand) error
	Get(id uuid.UUID) (*Hand, error)
	All() []*Hand
	AllByHelm(helmID uuid.UUID) []*Hand
	DeleteByHelm(helmID uuid.UUID) error
	Update(id uuid.UUID, fn func(*Hand) error) error
}
