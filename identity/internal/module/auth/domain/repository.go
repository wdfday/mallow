package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ISessionRepository defines session persistence operations.
type ISessionRepository interface {
	Create(ctx context.Context, session *Session) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]Session, error)
	GetBySID(ctx context.Context, sid string, userID uuid.UUID) (*Session, error)
	MarkRevoked(ctx context.Context, sid string, userID uuid.UUID) error
	RevokeAllByUser(ctx context.Context, userID uuid.UUID) error
}

// TokenRepository handles short-lived verification token persistence.
type TokenRepository interface {
	Create(ctx context.Context, token *VerificationToken) error
	GetByToken(ctx context.Context, tokenStr string) (*VerificationToken, error)
	// MarkAsUsed invalidates the token so it cannot be reused.
	MarkAsUsed(ctx context.Context, tokenStr string) error
	// DeleteExpired removes expired tokens (no-op for TTL-backed stores).
	DeleteExpired(ctx context.Context) error
	DeleteByUserIDAndType(ctx context.Context, userID, tokenType string) error
}

// ITokenBlacklistRepository defines operations for token blacklist management.
type ITokenBlacklistRepository interface {
	Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error
	IsBlacklisted(ctx context.Context, token string) (bool, error)
	IsRevokedByFields(ctx context.Context, sid, userID string) (bool, error)
	CleanupExpired(ctx context.Context) error
	BlacklistAllUserTokens(ctx context.Context, userID uuid.UUID, reason string) error
	RevokeSession(ctx context.Context, sid string, expiresAt time.Time) error
}
