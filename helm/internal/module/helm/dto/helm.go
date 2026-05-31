package dto

import (
	"time"

	"github.com/google/uuid"

	botdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/helm/domain"
)

// ── Request DTOs ───────────────────────────────────────────────────────────

type UpdateHelmReq struct {
	Name string         `json:"name" binding:"omitempty,min=1,max=128"`
	Risk *RiskConfigDTO `json:"risk"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type HelmResp struct {
	ID          uuid.UUID     `json:"id"`
	AccountID   uuid.UUID     `json:"account_id"`
	Name        string        `json:"name"`
	BrokerType  string        `json:"broker_type"`
	AccountType string        `json:"account_type"`
	Risk        RiskConfigDTO `json:"risk"`
	Status      string        `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// HelmDetailResp is the full helm view including live hand summaries.
type HelmDetailResp struct {
	HelmResp
	Hands   []botdomain.HandSummary `json:"hands"`
	Running bool                    `json:"running"`
	Paused  bool                    `json:"paused"`
	// Halted is true when the runtime risk manager has tripped a circuit-breaker
	// (max drawdown or daily loss limit). New entries are blocked; exits still pass.
	// Authoritative from the runtime; also reflected in Status="halted" after DB persist.
	Halted     bool       `json:"halted"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
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
		Risk:        riskToDTO(cfg.Risk),
		Status:      string(cfg.Status),
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
