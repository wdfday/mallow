package service

import (
	"context"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/broker/domain"
	"mallow/helm/internal/module/broker/dto"
)

// BrokerConnectionService defines the business logic for broker connections.
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

	// ReBroker changes the broker connection linked to an account and notifies the runtime.
	// accountID is the Account; newBrokerID is the new BrokerConnection to attach.
	ReBroker(ctx context.Context, accountID uuid.UUID, newBrokerID uuid.UUID, userID uuid.UUID) error

	// Activate marks a broker connection as active.
	Activate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// Deactivate marks a broker connection as disconnected.
	Deactivate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// TestConnection verifies the broker credentials are still valid.
	TestConnection(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// RotateKey validates the new credentials with the broker, replaces the stored
	// key/secret/passphrase atomically, and triggers a helm runtime respawn.
	RotateKey(ctx context.Context, id uuid.UUID, userID uuid.UUID, req *RotateKeyRequest) (*domain.BrokerConnection, error)

	// GetCredentialsByAccountID looks up the broker connection for accountID,
	// decrypts the credentials, and returns a CredentialsFetchResp ready for
	// spawning a helm runtime. Replaces the old NATS investment.accounts.credentials round-trip.
	GetCredentialsByAccountID(ctx context.Context, accountID string) (natsapi.CredentialsFetchResp, error)
}

// RotateKeyRequest carries the new plaintext credentials for a key rotation.
type RotateKeyRequest struct {
	APIKey     string
	APISecret  string
	Passphrase *string // nil for exchanges that don't use a passphrase
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
