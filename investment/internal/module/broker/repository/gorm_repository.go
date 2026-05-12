package repository

import (
	"context"
	"fmt"
	"mallow/investment/internal/module/broker/domain"
	"time"

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

func (r *gormBrokerConnectionRepository) GetNeedingSync(ctx context.Context, limit int) ([]*domain.BrokerConnection, error) {
	var connections []*domain.BrokerConnection

	// Raw SQL to handle the sync frequency logic
	// A connection needs sync if:
	// 1. auto_sync = true
	// 2. status = 'active'
	// 3. last_sync_at IS NULL OR (NOW() - last_sync_at) > (sync_frequency * INTERVAL '1 minute')
	query := r.db.WithContext(ctx).
		Where("auto_sync = ? AND status = ?", true, domain.BrokerConnectionStatusActive).
		Where(r.db.Where("last_sync_at IS NULL").Or("NOW() - last_sync_at > sync_frequency * INTERVAL '1 minute'")).
		Order("last_sync_at ASC NULLS FIRST"). // Sync never-synced first, then oldest
		Limit(limit)

	err := query.Find(&connections).Error
	if err != nil {
		return nil, err
	}

	return connections, nil
}

func (r *gormBrokerConnectionRepository) UpdateSyncStatus(ctx context.Context, id uuid.UUID, lastSyncAt time.Time, syncStatus, syncError *string, stats map[string]int) error {
	updates := map[string]interface{}{
		"last_sync_at":     lastSyncAt,
		"last_sync_status": syncStatus,
		"last_sync_error":  syncError,
	}

	// Update statistics if provided
	if total, ok := stats["total"]; ok {
		updates["total_syncs"] = gorm.Expr("total_syncs + ?", total)
	}
	if successful, ok := stats["successful"]; ok {
		updates["successful_syncs"] = gorm.Expr("successful_syncs + ?", successful)
	}
	if failed, ok := stats["failed"]; ok {
		updates["failed_syncs"] = gorm.Expr("failed_syncs + ?", failed)
	}

	return r.db.WithContext(ctx).
		Model(&domain.BrokerConnection{}).
		Where("id = ?", id).
		Updates(updates).Error
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
