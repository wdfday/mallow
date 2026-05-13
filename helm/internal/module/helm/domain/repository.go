package domain

import (
	"time"

	"github.com/google/uuid"
)

// HelmRepo is the port for persisting and retrieving orchestrator configs.
type HelmRepo interface {
	Save(o *Helm) error
	Get(id uuid.UUID) (*Helm, error)
	GetByAccountID(accountID uuid.UUID) (*Helm, error)
	All() ([]*Helm, error)
	AllByUser(userID uuid.UUID) ([]*Helm, error)
	Update(id uuid.UUID, fn func(*Helm) error) error
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
	Delete(id uuid.UUID) error
}
