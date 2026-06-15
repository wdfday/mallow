package handler

import (
	"github.com/google/uuid"

	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/runtime"
)

// HelmService is the full helm management interface used by this handler.
type HelmService interface {
	Get(id uuid.UUID) (*helmdomain.Helm, error)
	GetByAccount(accountID uuid.UUID) (*helmdomain.Helm, error)
	List() ([]*helmdomain.Helm, error)
	ListByUser(userID uuid.UUID) ([]*helmdomain.Helm, error)
	CheckOwner(id, userID uuid.UUID) error
	Enable(id uuid.UUID) error
	// Disable flattens all open positions, tears down the runtime, and marks the helm disabled.
	Disable(id uuid.UUID) error
	Pause(id uuid.UUID) error
	Resume(id uuid.UUID) error
	ResetHalt(id uuid.UUID) error
	Update(id uuid.UUID, req helmDto.UpdateReq) (*helmdomain.Helm, error)
	CreateForAccount(req helmDto.CreateForAccountReq) (*helmdomain.Helm, error)
	DeleteForAccount(accountID uuid.UUID) error
}

// HandManager is the subset of hand/service.Service used by the helm handler.
type HandManager interface {
	Get(id uuid.UUID) (*runtime.HandRef, error)
	ListByHelm(helmID uuid.UUID) []handdomain.HandSummary
	RunningHands() []*runtime.HandRef
}
