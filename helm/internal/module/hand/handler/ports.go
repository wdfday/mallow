package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime"
)

// HelmService is the subset of helm/service.Service used by the hand handler.
type HelmService interface {
	Get(id uuid.UUID) (*helmdomain.Helm, error)
	GetByAccount(accountID uuid.UUID) (*helmdomain.Helm, error)
	CheckOwner(id, userID uuid.UUID) error
	ListByUser(userID uuid.UUID) ([]*helmdomain.Helm, error)
}

// HandService is the full hand management interface used by this handler.
type HandService interface {
	Get(id uuid.UUID) (*runtime.HandRef, error)
	GetSummary(id uuid.UUID) (*domain.HandSummary, error)
	List() []domain.HandSummary
	ListByHelm(helmID uuid.UUID) []domain.HandSummary
	Create(cfg domain.HandConfig) (*runtime.HandRef, error)
	Update(id uuid.UUID, patch domain.HandConfig) error
	Delete(id uuid.UUID) error
	Start(id uuid.UUID) error
	Stop(id uuid.UUID) error
	Restart(id uuid.UUID) error
	Pause(id uuid.UUID) error
	Resume(id uuid.UUID) error
	Kill(ctx context.Context, id uuid.UUID) error
	Release(ctx context.Context, id uuid.UUID) error
	RunningHands() []*runtime.HandRef
	AllocateCapital(id uuid.UUID, delta decimal.Decimal) (decimal.Decimal, error)
}

// RuntimeRegistry is the subset of runtime.Registry used by hand handlers.
type RuntimeRegistry interface {
	Get(id uuid.UUID) (*runtime.HelmRuntime, error)
	All() []*runtime.HelmRuntime
	// DispatchStats returns registry-level signal routing error counts.
	DispatchStats() runtime.DispatchStats
	// NATSStats returns NATS-level dispatcher counters (total, missing IDs, nil payloads).
	// Returns zero values if no dispatcher has been wired.
	NATSStats() runtime.DispatcherSnapshot
}
