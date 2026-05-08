package repository

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mallow/helm/internal/module/hand/domain"
)

// GORMHandRepo implements domain.HandRepo using GORM + PostgreSQL.
// Hand is the GORM model directly — no intermediate mapping needed
// because all JSONB columns implement driver.Valuer / sql.Scanner.
type GORMHandRepo struct{ db *gorm.DB }

func New(db *gorm.DB) *GORMHandRepo { return &GORMHandRepo{db: db} }

func (r *GORMHandRepo) GenerateID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func (r *GORMHandRepo) Save(b *domain.Hand) error {
	return r.db.Save(b).Error
}

func (r *GORMHandRepo) Get(id uuid.UUID) (*domain.Hand, error) {
	var b domain.Hand
	if err := r.db.First(&b, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("hand %q: %w", id, err)
	}
	return &b, nil
}

func (r *GORMHandRepo) All() []*domain.Hand {
	var out []*domain.Hand
	r.db.Order("created_at").Find(&out)
	return out
}

func (r *GORMHandRepo) AllByHelm(helmID uuid.UUID) []*domain.Hand {
	var out []*domain.Hand
	r.db.Order("created_at").Where("helm_id = ?", helmID).Find(&out)
	return out
}

func (r *GORMHandRepo) Delete(id uuid.UUID) error {
	res := r.db.Delete(&domain.Hand{}, "id = ?", id)
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
	return r.Save(b)
}
