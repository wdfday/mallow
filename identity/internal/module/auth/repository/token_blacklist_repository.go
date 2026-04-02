package repository

import (
	"context"
	"crypto/sha256"
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
}

// TokenBlacklistEntry is the GORM model persisted to PostgreSQL.
// It is exported so gorm.go can include it in AutoMigrate.
type TokenBlacklistEntry struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	TokenHash string    `gorm:"uniqueIndex;size:64;not null"`
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

func redisTokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("blacklist:token:%x", h)
}

func redisUserKey(userID uuid.UUID) string {
	return fmt.Sprintf("blacklist:user:%s", userID.String())
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h)
}

// Add persists the revoked token to PostgreSQL (if available) and caches it in Redis.
func (r *tokenBlacklistRepository) Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil // already expired, skip
	}

	if r.db != nil {
		entry := TokenBlacklistEntry{
			TokenHash: hashToken(token),
			UserID:    userID,
			Reason:    reason,
			ExpiresAt: expiresAt,
		}
		if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return fmt.Errorf("blacklist db write: %w", err)
		}
	}

	_ = r.redis.Set(ctx, redisTokenKey(token), reason, ttl).Err()
	return nil
}

// IsBlacklisted checks Redis first (O(1)), then falls back to PostgreSQL and re-populates the cache.
func (r *tokenBlacklistRepository) IsBlacklisted(ctx context.Context, token string) (bool, error) {
	// Fast path: Redis cache hit for this specific token.
	n, err := r.redis.Exists(ctx, redisTokenKey(token)).Result()
	if err == nil && n > 0 {
		return true, nil
	}

	// Best-effort: check user-wide revocation marker in Redis.
	// We decode the JWT sub claim without verification — only used as a lookup key.
	if sub := extractJWTSub(token); sub != "" {
		if uid, parseErr := uuid.Parse(sub); parseErr == nil {
			if n2, _ := r.redis.Exists(ctx, redisUserKey(uid)).Result(); n2 > 0 {
				return true, nil
			}
		}
	}

	// DB fallback for individual token (handles Redis eviction / restart).
	if r.db == nil {
		return false, nil
	}
	var entry TokenBlacklistEntry
	result := r.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ?", hashToken(token), time.Now()).
		First(&entry)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("blacklist db read: %w", result.Error)
	}

	// Re-populate Redis cache.
	if ttl := time.Until(entry.ExpiresAt); ttl > 0 {
		_ = r.redis.Set(ctx, redisTokenKey(token), entry.Reason, ttl).Err()
	}
	return true, nil
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

// extractJWTSub decodes the JWT payload (without signature verification) to read the 'sub' claim.
// This is intentionally unverified — it is only used as a cache lookup key for revocation checks,
// never for authentication or authorization decisions.
func extractJWTSub(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}
