package repository

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"mallow/helm/internal/module/hand/domain"
)

// GORMHandRepo implements domain.HandRepo using GORM + PostgreSQL.
type GORMHandRepo struct{ db *gorm.DB }

func New(db *gorm.DB) *GORMHandRepo { return &GORMHandRepo{db: db} }

func (r *GORMHandRepo) GenerateID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func (r *GORMHandRepo) Save(b *domain.Hand) error {
	return r.db.Save(toModel(b)).Error
}

func (r *GORMHandRepo) Get(id uuid.UUID) (*domain.Hand, error) {
	var m handModel
	if err := r.db.First(&m, "id = ?", id.String()).Error; err != nil {
		return nil, fmt.Errorf("hand %q: %w", id, err)
	}
	return toDomain(&m)
}

func (r *GORMHandRepo) All() []*domain.Hand {
	var models []handModel
	r.db.Order("created_at").Find(&models)
	return sliceToDomain(models)
}

func (r *GORMHandRepo) AllByHelm(helmID uuid.UUID) []*domain.Hand {
	var models []handModel
	r.db.Order("created_at").Where("helm_id = ?", helmID.String()).Find(&models)
	return sliceToDomain(models)
}

func (r *GORMHandRepo) Delete(id uuid.UUID) error {
	res := r.db.Delete(&handModel{}, "id = ?", id.String())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("hand %q not found", id)
	}
	return nil
}

func (r *GORMHandRepo) Update(id uuid.UUID, fn func(*domain.Hand) error) error {
	b, err := r.Get(id)
	if err != nil {
		return err
	}
	if err := fn(b); err != nil {
		return err
	}
	b.UpdatedAt = time.Now().UTC()
	return r.Save(b)
}

// ── GORM model ────────────────────────────────────────────────────────────────

// handModel is the internal GORM mapping for the hands table.
// UUID fields are stored as strings to avoid pgx type-coercion friction.
// JSONB columns use domain types directly — they implement driver.Valuer/sql.Scanner.
type handModel struct {
	ID               string                  `gorm:"column:id;type:uuid;primaryKey"`
	HelmID           string                  `gorm:"column:helm_id;type:uuid;not null;index:idx_hands_helm_id"`
	Name             string                  `gorm:"column:name;not null"`
	Type             domain.HandType         `gorm:"column:type;not null;default:signal_follower"`
	Market           domain.MarketType       `gorm:"column:market;not null;default:spot"`
	Status           domain.HandStatus       `gorm:"column:status;not null;default:stopped"`
	Symbols          domain.StringSlice      `gorm:"column:symbols;type:jsonb;not null;default:'[]'"`
	Strategy         domain.StrategySpec     `gorm:"column:strategy;type:jsonb;not null;default:'{}'"`
	Position         domain.PositionConfig   `gorm:"column:position;type:jsonb;not null;default:'{}'"`
	Guard            domain.HandGuardConfig  `gorm:"column:risk;type:jsonb;not null;default:'{}'"`
	Futures          *domain.FuturesConfig   `gorm:"column:futures;type:jsonb"`
	AllocatedCapital decimal.Decimal         `gorm:"column:allocated_capital;type:numeric(20,8);not null;default:0"`
	SignalTTLSec     int                     `gorm:"column:signal_ttl_sec;not null;default:0"`
	OrderType        domain.OrderType        `gorm:"column:order_type;not null;default:market"`
	LimitTimeoutSec  int                     `gorm:"column:limit_timeout_sec;not null;default:0"`
	LimitFallback    domain.LimitFallback    `gorm:"column:limit_fallback;not null;default:cancel"`
	FinalMetrics     *domain.HandMetricsView `gorm:"column:final_metrics;type:jsonb"`
	CreatedAt        time.Time               `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt        time.Time               `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (handModel) TableName() string { return "hands" }

// ── mapping ───────────────────────────────────────────────────────────────────

func toModel(h *domain.Hand) *handModel {
	return &handModel{
		ID:               h.ID.String(),
		HelmID:           h.HelmID.String(),
		Name:             h.Name,
		Type:             h.Type,
		Market:           h.Market,
		Status:           h.Status,
		Symbols:          h.Symbols,
		Strategy:         h.Strategy,
		Position:         h.Position,
		Guard:            h.Guard,
		Futures:          h.Futures,
		AllocatedCapital: h.AllocatedCapital,
		SignalTTLSec:     h.SignalTTLSec,
		OrderType:        h.OrderType,
		LimitTimeoutSec:  h.LimitTimeoutSec,
		LimitFallback:    h.LimitFallback,
		FinalMetrics:     h.FinalMetrics,
		CreatedAt:        h.CreatedAt,
		UpdatedAt:        h.UpdatedAt,
	}
}

func toDomain(m *handModel) (*domain.Hand, error) {
	id, err := uuid.Parse(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse id %q: %w", m.ID, err)
	}
	helmID, err := uuid.Parse(m.HelmID)
	if err != nil {
		return nil, fmt.Errorf("parse helm_id %q: %w", m.HelmID, err)
	}
	return &domain.Hand{
		ID:               id,
		HelmID:           helmID,
		Name:             m.Name,
		Type:             m.Type,
		Market:           m.Market,
		Status:           m.Status,
		Symbols:          m.Symbols,
		Strategy:         m.Strategy,
		Position:         m.Position,
		Guard:            m.Guard,
		Futures:          m.Futures,
		AllocatedCapital: m.AllocatedCapital,
		SignalTTLSec:     m.SignalTTLSec,
		OrderType:        m.OrderType,
		LimitTimeoutSec:  m.LimitTimeoutSec,
		LimitFallback:    m.LimitFallback,
		FinalMetrics:     m.FinalMetrics,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}, nil
}

func sliceToDomain(models []handModel) []*domain.Hand {
	out := make([]*domain.Hand, 0, len(models))
	for i := range models {
		h, err := toDomain(&models[i])
		if err != nil {
			continue
		}
		out = append(out, h)
	}
	return out
}
