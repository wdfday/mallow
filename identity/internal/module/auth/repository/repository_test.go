package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/shared"
)

// setupTestRedis creates a real Redis client on DB 15 (reserved for tests).
// The test is skipped if Redis is unreachable.
func setupTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // isolated test DB
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis unavailable (%v), skipping integration test", err)
	}
	// Flush test DB before each test.
	rdb.FlushDB(ctx)
	t.Cleanup(func() { rdb.FlushDB(ctx); rdb.Close() })
	return rdb
}

func TestTokenRepository_Create(t *testing.T) {
	rdb := setupTestRedis(t)
	repo := NewTokenRepository(rdb)
	ctx := context.Background()

	userID := uuid.New().String()
	tokenStr := "test_token_" + uuid.New().String()

	token := &domain.VerificationToken{
		UserID:    userID,
		Token:     tokenStr,
		Type:      string(domain.TokenTypeEmailVerification),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	require.NoError(t, repo.Create(ctx, token))

	// Verify primary key exists.
	val, err := rdb.Exists(ctx, tokKey(tokenStr)).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), val)

	// Verify secondary index exists.
	val, err = rdb.Exists(ctx, verifUserKey(userID, string(domain.TokenTypeEmailVerification))).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), val)
}

func TestTokenRepository_GetByToken(t *testing.T) {
	rdb := setupTestRedis(t)
	repo := NewTokenRepository(rdb)
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		userID := uuid.New().String()
		tokenStr := "get_token_" + uuid.New().String()
		token := &domain.VerificationToken{
			UserID:    userID,
			Token:     tokenStr,
			Type:      string(domain.TokenTypeEmailVerification),
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, repo.Create(ctx, token))

		result, err := repo.GetByToken(ctx, tokenStr)
		assert.NoError(t, err)
		assert.Equal(t, userID, result.UserID)
		assert.Equal(t, tokenStr, result.Token)
		assert.Equal(t, string(domain.TokenTypeEmailVerification), result.Type)
	})

	t.Run("not found", func(t *testing.T) {
		result, err := repo.GetByToken(ctx, "nonexistent")
		assert.ErrorIs(t, err, shared.ErrTokenNotFound)
		assert.Nil(t, result)
	})
}

func TestTokenRepository_MarkAsUsed(t *testing.T) {
	rdb := setupTestRedis(t)
	repo := NewTokenRepository(rdb)
	ctx := context.Background()

	tokenStr := "mark_used_" + uuid.New().String()
	token := &domain.VerificationToken{
		UserID:    uuid.New().String(),
		Token:     tokenStr,
		Type:      string(domain.TokenTypeEmailVerification),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, repo.Create(ctx, token))

	require.NoError(t, repo.MarkAsUsed(ctx, tokenStr))

	// Primary key must be gone — token is invalidated.
	val, err := rdb.Exists(ctx, tokKey(tokenStr)).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), val)
}

func TestTokenRepository_DeleteExpired(t *testing.T) {
	rdb := setupTestRedis(t)
	repo := NewTokenRepository(rdb)
	ctx := context.Background()

	// DeleteExpired is a no-op; just verify it returns nil.
	assert.NoError(t, repo.DeleteExpired(ctx))
}

func TestTokenRepository_DeleteByUserIDAndType(t *testing.T) {
	rdb := setupTestRedis(t)
	repo := NewTokenRepository(rdb)
	ctx := context.Background()

	userID := uuid.New().String()
	emailTok := "email_" + uuid.New().String()
	pwdTok := "pwd_" + uuid.New().String()

	require.NoError(t, repo.Create(ctx, &domain.VerificationToken{
		UserID: userID, Token: emailTok,
		Type: string(domain.TokenTypeEmailVerification), ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.Create(ctx, &domain.VerificationToken{
		UserID: userID, Token: pwdTok,
		Type: string(domain.TokenTypePasswordReset), ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, repo.DeleteByUserIDAndType(ctx, userID, string(domain.TokenTypeEmailVerification)))

	// Email token gone.
	val, _ := rdb.Exists(ctx, tokKey(emailTok)).Result()
	assert.Equal(t, int64(0), val)

	// Password reset token intact.
	val, _ = rdb.Exists(ctx, tokKey(pwdTok)).Result()
	assert.Equal(t, int64(1), val)
}
