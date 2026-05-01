package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	authDomain "mallow/identity/internal/module/auth/domain"
)

// ISessionRepository defines session persistence operations.
type ISessionRepository interface {
	Create(ctx context.Context, session *authDomain.Session) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]authDomain.Session, error)
	GetBySID(ctx context.Context, sid string, userID uuid.UUID) (*authDomain.Session, error)
	MarkRevoked(ctx context.Context, sid string, userID uuid.UUID) error
	RevokeAllByUser(ctx context.Context, userID uuid.UUID) error
}

type sessionRepository struct {
	db *gorm.DB
}

// NewSessionRepository creates a PostgreSQL-backed session repository.
func NewSessionRepository(db *gorm.DB) ISessionRepository {
	return &sessionRepository{db: db}
}

func (r *sessionRepository) Create(ctx context.Context, session *authDomain.Session) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *sessionRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]authDomain.Session, error) {
	var sessions []authDomain.Session
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("created_at DESC").
		Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (r *sessionRepository) GetBySID(ctx context.Context, sid string, userID uuid.UUID) (*authDomain.Session, error) {
	var session authDomain.Session
	err := r.db.WithContext(ctx).
		Where("sid = ? AND user_id = ?", sid, userID).
		First(&session).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

func (r *sessionRepository) MarkRevoked(ctx context.Context, sid string, userID uuid.UUID) error {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&authDomain.Session{}).
		Where("sid = ? AND user_id = ? AND revoked_at IS NULL", sid, userID).
		Update("revoked_at", &now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *sessionRepository) RevokeAllByUser(ctx context.Context, userID uuid.UUID) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&authDomain.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}
