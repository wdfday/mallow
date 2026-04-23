package repository

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

// ===========================================================================
// TokenBlacklistRepository
// ===========================================================================

// setupMiniRedis spins up an in-memory Redis via miniredis — no external dependency.
func setupMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

// makeTestToken builds a minimal JWT with jti, sub, and sid claims.
// The signature is intentionally fake — extractJWTFields only decodes the payload,
// it never verifies the signature.
func makeTestToken(jti, sub, sid string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"jti":%q,"sub":%q,"sid":%q,"exp":%d}`, jti, sub, sid, time.Now().Add(time.Hour).Unix()),
	))
	return header + "." + payload + ".fakesig"
}

func TestTokenBlacklistRepository_AddAndIsBlacklisted(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	jti := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(jti, sub, uuid.New().String())
	userID := uuid.MustParse(sub)

	require.NoError(t, repo.Add(ctx, token, userID, "logout", time.Now().Add(time.Hour)))

	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.True(t, blacklisted)
}

func TestTokenBlacklistRepository_IsBlacklisted_UnknownToken(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	jti := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(jti, sub, uuid.New().String())

	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}

func TestTokenBlacklistRepository_Add_AlreadyExpired(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	jti := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(jti, sub, uuid.New().String())
	userID := uuid.MustParse(sub)

	// Add with a past expiry — should be silently skipped.
	require.NoError(t, repo.Add(ctx, token, userID, "logout", time.Now().Add(-time.Minute)))

	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}

func TestTokenBlacklistRepository_Add_MissingJTI(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	// Token payload has no jti field.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"some-user"}`))
	token := header + "." + payload + ".sig"

	err := repo.Add(ctx, token, uuid.New(), "logout", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jti")
}

func TestTokenBlacklistRepository_RedisKeyUsesJTI(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	jti := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(jti, sub, uuid.New().String())
	userID := uuid.MustParse(sub)

	require.NoError(t, repo.Add(ctx, token, userID, "logout", time.Now().Add(time.Hour)))

	// Verify key format is blacklist:jti:{jti}.
	n, err := rdb.Exists(ctx, "blacklist:jti:"+jti).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestTokenBlacklistRepository_UserWideBlacklist(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	userID := uuid.New()
	// Two different tokens for the same user.
	sid := uuid.New().String()
	token1 := makeTestToken(uuid.New().String(), userID.String(), sid)
	token2 := makeTestToken(uuid.New().String(), userID.String(), sid)

	require.NoError(t, repo.BlacklistAllUserTokens(ctx, userID, "password_change"))

	// Both tokens (never individually revoked) are now blocked by the user-wide marker.
	for _, tok := range []string{token1, token2} {
		blacklisted, err := repo.IsBlacklisted(ctx, tok)
		require.NoError(t, err)
		assert.True(t, blacklisted)
	}

	// Token for a different user is not affected.
	otherToken := makeTestToken(uuid.New().String(), uuid.New().String(), uuid.New().String())
	blacklisted, err := repo.IsBlacklisted(ctx, otherToken)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}

func TestTokenBlacklistRepository_UserWideBlacklist_ExpiredMarker(t *testing.T) {
	mr, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	userID := uuid.New()
	token := makeTestToken(uuid.New().String(), userID.String(), uuid.New().String())

	require.NoError(t, repo.BlacklistAllUserTokens(ctx, userID, "logout"))

	// Fast-forward miniredis clock past the 7-day TTL.
	mr.FastForward(8 * 24 * time.Hour)

	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}

func TestTokenBlacklistRepository_Add_TTLExpiry(t *testing.T) {
	mr, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	jti := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(jti, sub, uuid.New().String())
	userID := uuid.MustParse(sub)

	require.NoError(t, repo.Add(ctx, token, userID, "logout", time.Now().Add(5*time.Minute)))

	// Still blacklisted before expiry.
	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.True(t, blacklisted)

	// Fast-forward past the TTL.
	mr.FastForward(6 * time.Minute)

	blacklisted, err = repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}

func TestTokenBlacklistRepository_CleanupExpired_NilDB(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	// With nil DB, CleanupExpired is a no-op.
	assert.NoError(t, repo.CleanupExpired(ctx))
}

func TestTokenBlacklistRepository_RevokeSession(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	sid := uuid.New().String()
	sub := uuid.New().String()
	token := makeTestToken(uuid.New().String(), sub, sid)

	require.NoError(t, repo.RevokeSession(ctx, sid, time.Now().Add(time.Hour)))

	blacklisted, err := repo.IsBlacklisted(ctx, token)
	require.NoError(t, err)
	assert.True(t, blacklisted)
}

func TestTokenBlacklistRepository_RevokeSession_RedisKeyFormat(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	sid := uuid.New().String()
	require.NoError(t, repo.RevokeSession(ctx, sid, time.Now().Add(time.Hour)))

	n, err := rdb.Exists(ctx, "blacklist:sid:"+sid).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestTokenBlacklistRepository_RevokeSession_OtherSessionUnaffected(t *testing.T) {
	_, rdb := setupMiniRedis(t)
	repo := NewTokenBlacklistRepository(nil, rdb)
	ctx := context.Background()

	revokedSID := uuid.New().String()
	otherSID := uuid.New().String()
	otherToken := makeTestToken(uuid.New().String(), uuid.New().String(), otherSID)

	require.NoError(t, repo.RevokeSession(ctx, revokedSID, time.Now().Add(time.Hour)))

	blacklisted, err := repo.IsBlacklisted(ctx, otherToken)
	require.NoError(t, err)
	assert.False(t, blacklisted)
}
