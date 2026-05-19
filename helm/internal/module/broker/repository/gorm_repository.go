package repository

import (
	"context"
	"fmt"
	"mallow/helm/internal/module/broker/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type gormBrokerConnectionRepository struct {
	db *gorm.DB
}

// NewGormBrokerConnectionRepository creates a new GORM-based broker connection repository.
func NewGormBrokerConnectionRepository(db *gorm.DB) BrokerConnectionRepository {
	return &gormBrokerConnectionRepository{db: db}
}

// Migrate runs AutoMigrate for the broker_connections table.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&domain.BrokerConnection{})
}

func (r *gormBrokerConnectionRepository) Create(ctx context.Context, connection *domain.BrokerConnection) error {
	return r.db.WithContext(ctx).Create(connection).Error
}

func (r *gormBrokerConnectionRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.BrokerConnection, error) {
	var connection domain.BrokerConnection
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&connection).Error
	if err != nil {
		return nil, err
	}
	return &connection, nil
}

func (r *gormBrokerConnectionRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error) {
	var connections []*domain.BrokerConnection
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&connections).Error
	if err != nil {
		return nil, err
	}
	return connections, nil
}

func (r *gormBrokerConnectionRepository) GetActiveByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.BrokerConnection, error) {
	var connections []*domain.BrokerConnection
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, domain.BrokerConnectionStatusActive).
		Order("created_at DESC").
		Find(&connections).Error
	if err != nil {
		return nil, err
	}
	return connections, nil
}

func (r *gormBrokerConnectionRepository) GetByUserIDAndType(ctx context.Context, userID uuid.UUID, brokerType domain.BrokerType) ([]*domain.BrokerConnection, error) {
	var connections []*domain.BrokerConnection
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND broker_type = ?", userID, brokerType).
		Order("created_at DESC").
		Find(&connections).Error
	if err != nil {
		return nil, err
	}
	return connections, nil
}

func (r *gormBrokerConnectionRepository) Update(ctx context.Context, connection *domain.BrokerConnection) error {
	return r.db.WithContext(ctx).Save(connection).Error
}

func (r *gormBrokerConnectionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&domain.BrokerConnection{}).Error
}

func (r *gormBrokerConnectionRepository) HardDelete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&domain.BrokerConnection{}).Error
}

func (r *gormBrokerConnectionRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BrokerConnectionStatus) error {
	return r.db.WithContext(ctx).
		Model(&domain.BrokerConnection{}).
		Where("id = ?", id).
		Update("status", status).Error
}

func (r *gormBrokerConnectionRepository) Count(ctx context.Context, userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&domain.BrokerConnection{}).
		Where("user_id = ?", userID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count broker connections: %w", err)
	}
	return count, nil
}

func (r *gormBrokerConnectionRepository) CountByType(ctx context.Context, userID uuid.UUID, brokerType domain.BrokerType) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&domain.BrokerConnection{}).
		Where("user_id = ? AND broker_type = ?", userID, brokerType).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count broker connections by type: %w", err)
	}
	return count, nil
}
