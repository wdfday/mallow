package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/shared"
)

// TokenRepository handles short-lived verification token persistence in Redis.
type TokenRepository interface {
	Create(ctx context.Context, token *domain.VerificationToken) error
	GetByToken(ctx context.Context, tokenStr string) (*domain.VerificationToken, error)
	// MarkAsUsed invalidates the token by deleting it from Redis.
	MarkAsUsed(ctx context.Context, tokenStr string) error
	// DeleteExpired is a no-op: Redis TTL handles expiry automatically.
	DeleteExpired(ctx context.Context) error
	DeleteByUserIDAndType(ctx context.Context, userID, tokenType string) error
}

// tokenPayload is the JSON value stored under the primary key.
type tokenPayload struct {
	UserID    string  `json:"user_id"`
	Type      string  `json:"type"`
	ExpiresAt int64   `json:"expires_at"` // Unix timestamp
	IPAddress *string `json:"ip,omitempty"`
	UserAgent *string `json:"ua,omitempty"`
}

type tokenRepository struct {
	rdb *redis.Client
}

// NewTokenRepository creates a Redis-backed token repository.
func NewTokenRepository(rdb *redis.Client) TokenRepository {
	return &tokenRepository{rdb: rdb}
}

// tokKey is the primary lookup key: verif:tok:{tokenStr}
func tokKey(tokenStr string) string { return fmt.Sprintf("verif:tok:%s", tokenStr) }

// verifUserKey is the secondary index: verif:user:{type}:{userID}
// Stores the current active tokenStr for a (user, type) pair.
func verifUserKey(userID, tokenType string) string {
	return fmt.Sprintf("verif:user:%s:%s", tokenType, userID)
}

func (r *tokenRepository) Create(ctx context.Context, token *domain.VerificationToken) error {
	ttl := time.Until(token.ExpiresAt)
	if ttl <= 0 {
		return nil // already expired before it was stored — no-op
	}

	p := tokenPayload{
		UserID:    token.UserID,
		Type:      token.Type,
		ExpiresAt: token.ExpiresAt.Unix(),
		IPAddress: token.IPAddress,
		UserAgent: token.UserAgent,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}

	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, tokKey(token.Token), data, ttl)
	pipe.Set(ctx, verifUserKey(token.UserID, token.Type), token.Token, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *tokenRepository) GetByToken(ctx context.Context, tokenStr string) (*domain.VerificationToken, error) {
	data, err := r.rdb.Get(ctx, tokKey(tokenStr)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, shared.ErrTokenNotFound
		}
		return nil, err
	}

	var p tokenPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}

	return &domain.VerificationToken{
		UserID:    p.UserID,
		Token:     tokenStr,
		Type:      p.Type,
		ExpiresAt: time.Unix(p.ExpiresAt, 0),
		IPAddress: p.IPAddress,
		UserAgent: p.UserAgent,
	}, nil
}

// MarkAsUsed deletes the token so it cannot be reused.
// In Redis, absence == used OR expired — both are equivalent states.
func (r *tokenRepository) MarkAsUsed(ctx context.Context, tokenStr string) error {
	return r.rdb.Del(ctx, tokKey(tokenStr)).Err()
}

// DeleteExpired is a no-op: Redis TTL handles expiry automatically.
func (r *tokenRepository) DeleteExpired(_ context.Context) error { return nil }

// DeleteByUserIDAndType removes the current active token for a (user, type) pair.
func (r *tokenRepository) DeleteByUserIDAndType(ctx context.Context, userID, tokenType string) error {
	uk := verifUserKey(userID, tokenType)
	oldToken, err := r.rdb.Get(ctx, uk).Result()
	if err == redis.Nil {
		return nil // nothing to delete
	}
	if err != nil {
		return err
	}

	pipe := r.rdb.Pipeline()
	pipe.Del(ctx, tokKey(oldToken))
	pipe.Del(ctx, uk)
	_, err = pipe.Exec(ctx)
	return err
}
