package repository

import (
	"context"
	"mallow/helm/internal/module/broker/domain"

	"github.com/google/uuid"
)

// BrokerConnectionRepository defines the interface for broker connection data access.
type BrokerConnectionRepository interface {
	// Create creates a new broker connection
	Create(ctx context.Context, connection *domain.BrokerConnection) error

	// GetByID retrieves a broker connection by its ID
	GetByID(ctx context.Context, id uuid.UUID) (*domain.BrokerConnection, error)

	// GetByUserID retrieves all broker connections for a user
	GetByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error)

	// GetActiveByUserID retrieves all active broker connections for a user
	GetActiveByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error)

	// GetByUserIDAndType retrieves broker connections by user and broker type
	GetByUserIDAndType(ctx context.Context, userID uuid.UUID, brokerType domain.BrokerType) ([]*domain.BrokerConnection, error)

	// Update updates an existing broker connection
	Update(ctx context.Context, connection *domain.BrokerConnection) error

	// Delete soft-deletes a broker connection
	Delete(ctx context.Context, id uuid.UUID) error

	// HardDelete permanently deletes a broker connection
	HardDelete(ctx context.Context, id uuid.UUID) error

	// UpdateStatus updates the connection status
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BrokerConnectionStatus) error

	// GetAllActive returns every active (non-deleted) broker connection across all users.
	// Used at startup to sync sub-accounts that may have been added since last link.
	GetAllActive(ctx context.Context) ([]*domain.BrokerConnection, error)

	// Count returns the total number of broker connections for a user
	Count(ctx context.Context, userID uuid.UUID) (int64, error)

	// CountByType returns the number of connections by broker type for a user
	CountByType(ctx context.Context, userID uuid.UUID, brokerType domain.BrokerType) (int64, error)
}
