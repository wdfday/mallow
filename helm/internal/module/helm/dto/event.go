package dto

import (
	"github.com/google/uuid"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/helm/domain"
)

// CreateForAccountReq is the input for auto-creating a Helm when an account is linked.
// Exchange and Creds are transient — used only for the initial Spawn; NOT stored to DB.
type CreateForAccountReq struct {
	UserID    uuid.UUID
	AccountID uuid.UUID
	Name      string
	Exchange  domain.ExchangeConfig
	Creds     natsapi.CredentialsFetchResp
	Portfolio domain.PortfolioConfig
	Risk      domain.RiskConfig
}

// UpdateReq is the patch payload for updating a Helm config.
type UpdateReq struct {
	Name      string
	Portfolio *domain.PortfolioConfig
	Risk      *domain.RiskConfig
}
