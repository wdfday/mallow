package postgres

import (
	jsonLib "encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	botdomain "orchestrator/internal/module/bot/domain"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
)

// ── GORMBotRepo ───────────────────────────────────────────────────────────────

// GORMBotRepo implements botdomain.BotRepo using GORM.
// BotInstance itself is the GORM model — no intermediate mapping struct needed.
type GORMBotRepo struct{ db *gorm.DB }

func NewGORMBotRepo(db *gorm.DB) *GORMBotRepo { return &GORMBotRepo{db: db} }

func (r *GORMBotRepo) GenerateID() string { return uuid.New().String() }

func (r *GORMBotRepo) Save(b *botdomain.BotInstance) error {
	return r.db.Save(b).Error
}

func (r *GORMBotRepo) Get(id string) (*botdomain.BotInstance, error) {
	var b botdomain.BotInstance
	if err := r.db.First(&b, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("bot %q: %w", id, err)
	}
	return &b, nil
}

func (r *GORMBotRepo) All() []*botdomain.BotInstance {
	var out []*botdomain.BotInstance
	r.db.Order("created_at").Find(&out)
	return out
}

func (r *GORMBotRepo) AllByOrchestrator(orchID uuid.UUID) []*botdomain.BotInstance {
	var out []*botdomain.BotInstance
	r.db.Order("created_at").Where("orchestrator_id = ?", orchID).Find(&out)
	return out
}

func (r *GORMBotRepo) Delete(id string) error {
	res := r.db.Delete(&botdomain.BotInstance{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("bot %q not found", id)
	}
	return nil
}

func (r *GORMBotRepo) Update(id string, fn func(*botdomain.BotInstance) error) error {
	b, err := r.Get(id)
	if err != nil {
		return err
	}
	if err := fn(b); err != nil {
		return err
	}
	return r.Save(b)
}

// ── GORMOrchestratorRepo ──────────────────────────────────────────────────────

// GORMOrchestratorRepo implements orchdomain.OrchestratorRepo using GORM.
type GORMOrchestratorRepo struct{ db *gorm.DB }

func NewGORMOrchestratorRepo(db *gorm.DB) *GORMOrchestratorRepo {
	return &GORMOrchestratorRepo{db: db}
}

func (r *GORMOrchestratorRepo) Save(o *orchdomain.OrchestratorConfig) error {
	exchJSON, err := jsonValue(o.Exchange)
	if err != nil {
		return fmt.Errorf("marshal exchange: %w", err)
	}
	pfJSON, err := jsonValue(o.Portfolio)
	if err != nil {
		return fmt.Errorf("marshal portfolio: %w", err)
	}
	riskJSON, err := jsonValue(o.Risk)
	if err != nil {
		return fmt.Errorf("marshal risk: %w", err)
	}
	m := orchestratorModel{
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

func (r *GORMOrchestratorRepo) Get(id uuid.UUID) (*orchdomain.OrchestratorConfig, error) {
	var m orchestratorModel
	if err := r.db.First(&m, "id = ?", id.String()).Error; err != nil {
		return nil, fmt.Errorf("orchestrator %q: %w", id, err)
	}
	return orchModelToDomain(&m)
}

func (r *GORMOrchestratorRepo) GetByAccountID(accountID uuid.UUID) (*orchdomain.OrchestratorConfig, error) {
	var m orchestratorModel
	if err := r.db.First(&m, "account_id = ?", accountID.String()).Error; err != nil {
		return nil, fmt.Errorf("orchestrator for account %q: %w", accountID, err)
	}
	return orchModelToDomain(&m)
}

func (r *GORMOrchestratorRepo) All() ([]*orchdomain.OrchestratorConfig, error) {
	var models []orchestratorModel
	if err := r.db.Order("created_at").Find(&models).Error; err != nil {
		return nil, err
	}
	return orchSliceToDomain(models)
}

func (r *GORMOrchestratorRepo) AllByUser(userID uuid.UUID) ([]*orchdomain.OrchestratorConfig, error) {
	var models []orchestratorModel
	if err := r.db.Order("created_at").Where("user_id = ?", userID.String()).Find(&models).Error; err != nil {
		return nil, err
	}
	return orchSliceToDomain(models)
}

func (r *GORMOrchestratorRepo) Update(id uuid.UUID, fn func(*orchdomain.OrchestratorConfig) error) error {
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

func (r *GORMOrchestratorRepo) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	return r.Update(id, func(o *orchdomain.OrchestratorConfig) error {
		if o.LastSyncedAt == nil || t.After(*o.LastSyncedAt) {
			o.LastSyncedAt = &t
		}
		return nil
	})
}

func (r *GORMOrchestratorRepo) Delete(id uuid.UUID) error {
	res := r.db.Delete(&orchestratorModel{}, "id = ?", id.String())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("orchestrator %q not found", id)
	}
	return nil
}

// ── orchestratorModel ─────────────────────────────────────────────────────────

// orchestratorModel is the internal GORM mapping for the orchestrators table.
// OrchestratorConfig is not used directly because UUID fields need string coercion for pgx.
type orchestratorModel struct {
	ID              string     `gorm:"column:id;type:uuid;primaryKey"`
	UserID          string     `gorm:"column:user_id;type:uuid;not null;index:idx_orchestrators_user_id"`
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

func (orchestratorModel) TableName() string { return "orchestrators" }

func orchModelToDomain(m *orchestratorModel) (*orchdomain.OrchestratorConfig, error) {
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
	o := &orchdomain.OrchestratorConfig{
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
	_ = jsonScan(m.ExchangeConfig, &o.Exchange)
	_ = jsonScan(m.PortfolioConfig, &o.Portfolio)
	_ = jsonScan(m.RiskConfig, &o.Risk)
	return o, nil
}

func orchSliceToDomain(models []orchestratorModel) ([]*orchdomain.OrchestratorConfig, error) {
	out := make([]*orchdomain.OrchestratorConfig, 0, len(models))
	for i := range models {
		o, err := orchModelToDomain(&models[i])
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// ── local JSON helpers for orchestratorModel ─────────────────────────────────

func jsonValue(v any) ([]byte, error) {
	return jsonLib.Marshal(v)
}

func jsonScan(src any, dst any) error {
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	case nil:
		return nil
	default:
		return fmt.Errorf("jsonScan: unsupported type %T", src)
	}
	return jsonLib.Unmarshal(b, dst)
}
