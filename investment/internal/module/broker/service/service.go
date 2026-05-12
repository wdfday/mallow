package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/dto"
)

// BrokerConnectionService defines the business logic for broker connections.
// Syncing positions/transactions is handled by the orchestrator service,
// not by investment service directly.
type BrokerConnectionService interface {
	// Create validates credentials with the broker, saves the connection,
	// and creates a linked Account for portfolio tracking.
	Create(ctx context.Context, req *dto.CreateBrokerConnectionServiceRequest) (*domain.BrokerConnection, error)

	// GetByID retrieves a broker connection by ID.
	GetByID(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*domain.BrokerConnection, error)

	// List retrieves all broker connections for a user.
	List(ctx context.Context, userID uuid.UUID, filters *ListFilters) ([]*domain.BrokerConnection, error)

	// Update updates a broker connection's name or credentials.
	Update(ctx context.Context, id uuid.UUID, userID uuid.UUID, req *UpdateBrokerConnectionRequest) (*domain.BrokerConnection, error)

	// Delete soft-deletes a broker connection and deactivates the linked account.
	Delete(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// ReBroker changes the broker connection linked to an account and notifies the orchestrator.
	// accountID is the investment Account; newBrokerID is the new BrokerConnection to attach.
	ReBroker(ctx context.Context, accountID uuid.UUID, newBrokerID uuid.UUID, userID uuid.UUID) error

	// Activate marks a broker connection as active.
	Activate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// Deactivate marks a broker connection as disconnected.
	Deactivate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// TestConnection verifies the broker credentials are still valid.
	TestConnection(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// SubscribeCredentials subscribes to NATS and handles credential fetch requests from helm.
	// Helm calls investment.accounts.credentials when it needs to spawn a runtime.
	SubscribeCredentials(nc *nats.Conn) error
}

// UpdateBrokerConnectionRequest is the request to update a broker connection.
type UpdateBrokerConnectionRequest struct {
	BrokerName *string
	Notes      *string

	// Credentials (optional — will be encrypted if provided)
	APIKey     *string
	APISecret  *string
	Passphrase *string
}

// ListFilters filters for listing broker connections.
type ListFilters struct {
	BrokerType *domain.BrokerType
	Status     *domain.BrokerConnectionStatus
	ActiveOnly bool
}
