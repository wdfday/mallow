package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	authDomain "mallow/identity/internal/module/auth/domain"
)

// SessionEntry is the GORM model persisted to PostgreSQL.
// It is exported so gorm.go can include it in AutoMigrate.
type SessionEntry struct {
	SID       string    `gorm:"primaryKey;size:36"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	IP        string    `gorm:"size:45"`
	UserAgent string    `gorm:"size:512"`
	ExpiresAt time.Time `gorm:"not null"`
	CreatedAt time.Time
	RevokedAt *time.Time `gorm:"index"`
}

func (SessionEntry) TableName() string { return "sessions" }

func (e *SessionEntry) toDomain() authDomain.Session {
	return authDomain.Session{
		SID:       e.SID,
		UserID:    e.UserID,
		IP:        e.IP,
		UserAgent: e.UserAgent,
		ExpiresAt: e.ExpiresAt,
		CreatedAt: e.CreatedAt,
		RevokedAt: e.RevokedAt,
	}
}

// ISessionRepository defines session persistence operations.
type ISessionRepository interface {
	Create(ctx context.Context, entry *SessionEntry) error
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

func (r *sessionRepository) Create(ctx context.Context, entry *SessionEntry) error {
	return r.db.WithContext(ctx).Create(entry).Error
}

func (r *sessionRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]authDomain.Session, error) {
	var entries []SessionEntry
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("created_at DESC").
		Find(&entries).Error
	if err != nil {
		return nil, err
	}
	sessions := make([]authDomain.Session, len(entries))
	for i, e := range entries {
		sessions[i] = e.toDomain()
	}
	return sessions, nil
}

func (r *sessionRepository) GetBySID(ctx context.Context, sid string, userID uuid.UUID) (*authDomain.Session, error) {
	var entry SessionEntry
	err := r.db.WithContext(ctx).
		Where("sid = ? AND user_id = ?", sid, userID).
		First(&entry).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	s := entry.toDomain()
	return &s, nil
}

func (r *sessionRepository) MarkRevoked(ctx context.Context, sid string, userID uuid.UUID) error {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&SessionEntry{}).
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
		Model(&SessionEntry{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}
