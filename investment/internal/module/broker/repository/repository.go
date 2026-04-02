package repository

import (
	"context"
	"mallow/investment/internal/module/broker/domain"
	"time"

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

	// GetNeedingSync retrieves all broker connections that need syncing
	// Returns connections where:
	// - AutoSync is enabled
	// - Status is active
	// - LastSyncAt is nil OR (now - LastSyncAt) > SyncFrequency
	GetNeedingSync(ctx context.Context, limit int) ([]*domain.BrokerConnection, error)

	// GetExpiredTokens retrieves broker connections with expired tokens
	GetExpiredTokens(ctx context.Context, limit int) ([]*domain.BrokerConnection, error)

	// UpdateSyncStatus updates sync-related fields
	UpdateSyncStatus(ctx context.Context, id uuid.UUID, lastSyncAt time.Time, syncStatus, syncError *string, stats map[string]int) error

	// UpdateTokens updates access token and expiration
	UpdateTokens(ctx context.Context, id uuid.UUID, accessToken *string, refreshToken *string, expiresAt *time.Time) error

	// UpdateStatus updates the connection status
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BrokerConnectionStatus) error

	// Count returns the total number of broker connections for a user
	Count(ctx context.Context, userID uuid.UUID) (int64, error)

	// CountByType returns the number of connections by broker type for a user
	CountByType(ctx context.Context, userID uuid.UUID, brokerType domain.BrokerType) (int64, error)
}
