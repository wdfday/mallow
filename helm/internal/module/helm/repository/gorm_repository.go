package repository

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mallow/helm/internal/module/helm/domain"
)

// GORMHelmRepo implements domain.HelmRepo using GORM + PostgreSQL.
// HelmConfig fields containing nested structs are stored as JSONB using a
// flat helmModel — UUID fields are strings to avoid pgx driver coercion issues.
type GORMHelmRepo struct{ db *gorm.DB }

func New(db *gorm.DB) *GORMHelmRepo { return &GORMHelmRepo{db: db} }

func (r *GORMHelmRepo) Save(o *domain.HelmConfig) error {
	exchJSON, err := jsonMarshal(o.Exchange)
	if err != nil {
		return fmt.Errorf("marshal exchange: %w", err)
	}
	pfJSON, err := jsonMarshal(o.Portfolio)
	if err != nil {
		return fmt.Errorf("marshal portfolio: %w", err)
	}
	riskJSON, err := jsonMarshal(o.Risk)
	if err != nil {
		return fmt.Errorf("marshal risk: %w", err)
	}
	m := helmModel{
		ID:              o.ID.String(),
		UserID:          o.UserID.String(),
		AccountID:       o.AccountID.String(),
		Name:            o.Name,
		Capital:         o.Capital,
		ExchangeConfig:  exchJSON,
		PortfolioConfig: pfJSON,
		RiskConfig:      riskJSON,
		Enabled:         o.Enabled,
		Status:          o.Status,
		LastSyncedAt:    o.LastSyncedAt,
		CreatedAt:       o.CreatedAt,
		UpdatedAt:       o.UpdatedAt,
	}
	return r.db.Save(&m).Error
}

func (r *GORMHelmRepo) Get(id uuid.UUID) (*domain.HelmConfig, error) {
	var m helmModel
	if err := r.db.First(&m, "id = ?", id.String()).Error; err != nil {
		return nil, fmt.Errorf("helm %q: %w", id, err)
	}
	return modelToDomain(&m)
}

func (r *GORMHelmRepo) GetByAccountID(accountID uuid.UUID) (*domain.HelmConfig, error) {
	var m helmModel
	if err := r.db.First(&m, "account_id = ?", accountID.String()).Error; err != nil {
		return nil, fmt.Errorf("helm for account %q: %w", accountID, err)
	}
	return modelToDomain(&m)
}

func (r *GORMHelmRepo) All() ([]*domain.HelmConfig, error) {
	var models []helmModel
	if err := r.db.Order("created_at").Find(&models).Error; err != nil {
		return nil, err
	}
	return sliceToDomain(models)
}

func (r *GORMHelmRepo) AllByUser(userID uuid.UUID) ([]*domain.HelmConfig, error) {
	var models []helmModel
	if err := r.db.Order("created_at").Where("user_id = ?", userID.String()).Find(&models).Error; err != nil {
		return nil, err
	}
	return sliceToDomain(models)
}

func (r *GORMHelmRepo) Update(id uuid.UUID, fn func(*domain.HelmConfig) error) error {
	o, err := r.Get(id)
	if err != nil {
		return err
	}
	if err := fn(o); err != nil {
		return err
	}
	o.UpdatedAt = time.Now().UTC()
	return r.Save(o)
}

func (r *GORMHelmRepo) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	return r.Update(id, func(o *domain.HelmConfig) error {
		if o.LastSyncedAt == nil || t.After(*o.LastSyncedAt) {
			o.LastSyncedAt = &t
		}
		return nil
	})
}

func (r *GORMHelmRepo) Delete(id uuid.UUID) error {
	res := r.db.Delete(&helmModel{}, "id = ?", id.String())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("helm %q not found", id)
	}
	return nil
}

// ── GORM model ────────────────────────────────────────────────────────────────

// helmModel is the internal GORM mapping for the helms table.
// UUID fields are stored as strings to avoid pgx type-coercion friction.
type helmModel struct {
	ID              string     `gorm:"column:id;type:uuid;primaryKey"`
	UserID          string     `gorm:"column:user_id;type:uuid;not null;index:idx_helms_user_id"`
	AccountID       string     `gorm:"column:account_id;type:uuid;not null;uniqueIndex"`
	Name            string     `gorm:"column:name;not null"`
	Capital         float64    `gorm:"column:capital;type:double precision;not null;default:0"`
	ExchangeConfig  []byte     `gorm:"column:exchange_config;type:jsonb;not null;default:'{}'"`
	PortfolioConfig []byte     `gorm:"column:portfolio_config;type:jsonb;not null;default:'{}'"`
	RiskConfig      []byte     `gorm:"column:risk_config;type:jsonb;not null;default:'{}'"`
	Enabled         bool       `gorm:"column:enabled;not null;default:false"`
	Status          string     `gorm:"column:status;not null;default:active"`
	LastSyncedAt    *time.Time `gorm:"column:last_synced_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (helmModel) TableName() string { return "helms" }

// ── mapping ───────────────────────────────────────────────────────────────────

func modelToDomain(m *helmModel) (*domain.HelmConfig, error) {
	id, err := uuid.Parse(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse id %q: %w", m.ID, err)
	}
	userID, err := uuid.Parse(m.UserID)
	if err != nil {
		return nil, fmt.Errorf("parse user_id %q: %w", m.UserID, err)
	}
	accountID, err := uuid.Parse(m.AccountID)
	if err != nil {
		return nil, fmt.Errorf("parse account_id %q: %w", m.AccountID, err)
	}
	o := &domain.HelmConfig{
		ID:           id,
		UserID:       userID,
		AccountID:    accountID,
		Name:         m.Name,
		Capital:      m.Capital,
		Enabled:      m.Enabled,
		Status:       m.Status,
		LastSyncedAt: m.LastSyncedAt,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
	_ = jsonUnmarshal(m.ExchangeConfig, &o.Exchange)
	_ = jsonUnmarshal(m.PortfolioConfig, &o.Portfolio)
	_ = jsonUnmarshal(m.RiskConfig, &o.Risk)
	return o, nil
}

func sliceToDomain(models []helmModel) ([]*domain.HelmConfig, error) {
	out := make([]*domain.HelmConfig, 0, len(models))
	for i := range models {
		o, err := modelToDomain(&models[i])
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(src any, dst any) error {
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	case nil:
		return nil
	default:
		return fmt.Errorf("jsonUnmarshal: unsupported type %T", src)
	}
	return json.Unmarshal(b, dst)
}
