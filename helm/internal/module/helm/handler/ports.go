package handler

import (
	"github.com/google/uuid"

	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

// HandManager is the subset of hand/service.Service used by the helm handler.
type HandManager interface {
	Get(id uuid.UUID) (*runtime.HandRef, error)
	ListByHelm(helmID uuid.UUID) []handdomain.HandSummary
	RunningHands() []*runtime.HandRef
}
