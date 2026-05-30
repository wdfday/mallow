package service

import (
	"context"

	"github.com/google/uuid"
)

// HelmAutoCreateReq carries the minimum info needed to auto-create a Helm after a broker
// account is linked. Credentials are plain-text (already decrypted / taken from request).
type HelmAutoCreateReq struct {
	UserID      uuid.UUID
	AccountID   uuid.UUID
	AccountName string
	BrokerType  string
	AccountType string // spot | futures_usdm | futures_coinm | unified | options
	APIKey      string
	APISecret   string
	Passphrase  string
	Paper       bool
}

// HelmCreator is the port used by the broker service to manage Helm runtimes
// in response to broker account lifecycle events.
type HelmCreator interface {
	// AutoCreateForAccount creates (or re-spawns) the Helm for a newly linked account.
	// Idempotent: safe to call if the Helm already exists.
	AutoCreateForAccount(ctx context.Context, req HelmAutoCreateReq) error

	// AutoDeleteForAccount tears down and removes the Helm for a delinked account.
	AutoDeleteForAccount(ctx context.Context, accountID uuid.UUID) error

	// RotateCredsForAccount updates the in-flight credentials for the Helm linked
	// to accountID and reconnects the WS stream without interrupting running hands.
	RotateCredsForAccount(ctx context.Context, accountID uuid.UUID, req HelmAutoCreateReq) error
}
