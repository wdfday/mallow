package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ITokenBlacklistRepository defines operations for token blacklist management
type ITokenBlacklistRepository interface {
	Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error
	IsBlacklisted(ctx context.Context, token string) (bool, error)
	CleanupExpired(ctx context.Context) error
	BlacklistAllUserTokens(ctx context.Context, userID uuid.UUID, reason string) error
	RevokeSession(ctx context.Context, sid string, expiresAt time.Time) error
}

// TokenBlacklistEntry is the GORM model persisted to PostgreSQL.
// It is exported so gorm.go can include it in AutoMigrate.
// NOTE: the legacy token_hash column remains in the DB after this migration — drop it manually when ready.
type TokenBlacklistEntry struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	JTI       string    `gorm:"column:jti;uniqueIndex;size:36;not null"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	Reason    string    `gorm:"not null;default:'logout'"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}

func (TokenBlacklistEntry) TableName() string { return "token_blacklist" }

type tokenBlacklistRepository struct {
	db    *gorm.DB
	redis *redis.Client
}

// NewTokenBlacklistRepository creates a DB+Redis-backed token blacklist repository.
// PostgreSQL is the persistent source of truth; Redis is a fast cache.
func NewTokenBlacklistRepository(db *gorm.DB, rdb *redis.Client) ITokenBlacklistRepository {
	return &tokenBlacklistRepository{db: db, redis: rdb}
}

func redisJTIKey(jti string) string {
	return fmt.Sprintf("blacklist:jti:%s", jti)
}

func redisUserKey(userID uuid.UUID) string {
	return fmt.Sprintf("blacklist:user:%s", userID.String())
}

func redisSIDKey(sid string) string {
	return fmt.Sprintf("blacklist:sid:%s", sid)
}

// Add persists the revoked token to PostgreSQL (if available) and caches it in Redis.
// The JTI claim is extracted from the token for storage; the full token text is never persisted.
func (r *tokenBlacklistRepository) Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil // already expired, skip
	}

	jti, _, _ := extractJWTFields(token)
	if jti == "" {
		return fmt.Errorf("blacklist add: token missing jti claim")
	}

	if r.db != nil {
		entry := TokenBlacklistEntry{
			JTI:       jti,
			UserID:    userID,
			Reason:    reason,
			ExpiresAt: expiresAt,
		}
		if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return fmt.Errorf("blacklist db write: %w", err)
		}
	}

	_ = r.redis.Set(ctx, redisJTIKey(jti), reason, ttl).Err()
	return nil
}

// IsBlacklisted checks Redis first (O(1)), then falls back to PostgreSQL and re-populates the cache.
func (r *tokenBlacklistRepository) IsBlacklisted(ctx context.Context, token string) (bool, error) {
	// Decode JTI, sub, and sid without verifying the signature — lookup keys only, not auth decisions.
	jti, sub, sid := extractJWTFields(token)

	// Fast path: single Redis round-trip checking all applicable keys.
	var keys []string
	if jti != "" {
		keys = append(keys, redisJTIKey(jti))
	}
	if sub != "" {
		if uid, err := uuid.Parse(sub); err == nil {
			keys = append(keys, redisUserKey(uid))
		}
	}
	if sid != "" {
		keys = append(keys, redisSIDKey(sid))
	}
	if len(keys) > 0 {
		n, err := r.redis.Exists(ctx, keys...).Result()
		if err == nil && n > 0 {
			return true, nil
		}
	}

	// DB fallback for individual token (handles Redis eviction / restart).
	if r.db == nil || jti == "" {
		return false, nil
	}
	var entry TokenBlacklistEntry
	result := r.db.WithContext(ctx).
		Where("jti = ? AND expires_at > ?", jti, time.Now()).
		First(&entry)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("blacklist db read: %w", result.Error)
	}

	// Re-populate Redis cache.
	if ttl := time.Until(entry.ExpiresAt); ttl > 0 {
		_ = r.redis.Set(ctx, redisJTIKey(jti), entry.Reason, ttl).Err()
	}
	return true, nil
}

// RevokeSession marks an entire session as revoked by its SID.
// All tokens (access + refresh) sharing this SID will be rejected until the marker expires.
func (r *tokenBlacklistRepository) RevokeSession(ctx context.Context, sid string, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil
	}
	return r.redis.Set(ctx, redisSIDKey(sid), "revoked", ttl).Err()
}

// CleanupExpired removes expired rows from PostgreSQL.
// Redis TTL handles its own expiry automatically.
func (r *tokenBlacklistRepository) CleanupExpired(ctx context.Context) error {
	if r.db == nil {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&TokenBlacklistEntry{}).Error
}

// BlacklistAllUserTokens stores a user-level revocation marker in Redis.
// Any token whose JWT sub matches this userID will be considered revoked until the marker expires.
// TTL is set to the maximum refresh token lifetime (7 days).
func (r *tokenBlacklistRepository) BlacklistAllUserTokens(ctx context.Context, userID uuid.UUID, reason string) error {
	const refreshTokenLifetime = 7 * 24 * time.Hour
	return r.redis.Set(ctx, redisUserKey(userID), reason, refreshTokenLifetime).Err()
}

// extractJWTFields decodes the JWT payload without signature verification to read 'jti', 'sub', and 'sid'.
// Intentionally unverified — used only as lookup keys for revocation checks, never for auth decisions.
func extractJWTFields(token string) (jti, sub, sid string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", ""
	}
	var claims struct {
		JTI string `json:"jti"`
		Sub string `json:"sub"`
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", ""
	}
	return claims.JTI, claims.Sub, claims.SID
}
