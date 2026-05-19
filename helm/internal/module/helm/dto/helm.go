package dto

import (
	"time"

	"github.com/google/uuid"

	botdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/helm/domain"
)

// ── Request DTOs ───────────────────────────────────────────────────────────

type UpdateHelmReq struct {
	Name      string              `json:"name" binding:"omitempty,min=1,max=128"`
	Portfolio *PortfolioConfigDTO `json:"portfolio"`
	Risk      *RiskConfigDTO      `json:"risk"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type HelmResp struct {
	ID          uuid.UUID          `json:"id"`
	AccountID   uuid.UUID          `json:"account_id"`
	Name        string             `json:"name"`
	BrokerType  string             `json:"broker_type"`
	AccountType string             `json:"account_type"`
	Portfolio   PortfolioConfigDTO `json:"portfolio"`
	Risk        RiskConfigDTO      `json:"risk"`
	Enabled     bool               `json:"enabled"`
	Status      string             `json:"status"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// HelmDetailResp is the full helm view including live hand summaries.
type HelmDetailResp struct {
	HelmResp
	Hands      []botdomain.HandSummary `json:"hands"`
	Running    bool                    `json:"running"`
	Paused     bool                    `json:"paused"`
	LastSyncAt *time.Time              `json:"last_sync_at,omitempty"`
}

type ActionResp struct {
	Status string    `json:"status"`
	ID     uuid.UUID `json:"id"`
}

// ── Conversions ────────────────────────────────────────────────────────────

func HelmToResp(cfg *domain.Helm) HelmResp {
	return HelmResp{
		ID:          cfg.ID,
		AccountID:   cfg.AccountID,
		Name:        cfg.Name,
		BrokerType:  cfg.BrokerType,
		AccountType: cfg.AccountType,
		Portfolio:   portfolioToDTO(cfg.Portfolio),
		Risk:        riskToDTO(cfg.Risk),
		Enabled:     cfg.Enabled,
		Status:      cfg.Status,
		CreatedAt:   cfg.CreatedAt,
		UpdatedAt:   cfg.UpdatedAt,
	}
}

func HelmsToResp(cfgs []*domain.Helm) []HelmResp {
	out := make([]HelmResp, len(cfgs))
	for i, cfg := range cfgs {
		out[i] = HelmToResp(cfg)
	}
	return out
}
