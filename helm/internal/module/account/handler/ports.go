package handler

import (
	"github.com/google/uuid"

	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
)

// HelmLookup is the subset of helm service used by the account handler.
type HelmLookup interface {
	GetByAccount(accountID uuid.UUID) (*helmdomain.Helm, error)
}

// HandLister lists hands by helm — used for equity / trades fan-out.
type HandLister interface {
	ListByHelm(helmID uuid.UUID) []handdomain.HandSummary
}
