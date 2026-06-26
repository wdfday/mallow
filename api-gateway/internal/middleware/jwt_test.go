package middleware

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func newRedisWithError(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func generateEdDSA(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func edPublicKeyToPEM(pub ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func makeHMACToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

func makeEdDSAToken(t *testing.T, priv ed25519.PrivateKey, claims jwt.MapClaims, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func validClaims(sub, jti, sid string) jwt.MapClaims {
	return jwt.MapClaims{
		"sub": sub,
		"jti": jti,
		"sid": sid,
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
}

func expiredClaims(sub, jti string) jwt.MapClaims {
	return jwt.MapClaims{
		"sub": sub,
		"jti": jti,
		"exp": time.Now().Add(-1 * time.Minute).Unix(),
	}
}

func newRouter(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func get(router *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func bearer(token string) string { return "Bearer " + token }

const testSecret = "super-secret-for-tests"

// ─── isBlacklisted ──────────────────────────────────────────────────────────

func TestIsBlacklisted(t *testing.T) {
	ctx := context.Background()

	t.Run("empty jti and sub → not blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		ok, err := isBlacklisted(ctx, rdb, "", "")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("sid key set → blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		sid := "abc-sid-001"
		require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:sid:%s", sid), "logout", 0).Err())

		ok, err := isBlacklisted(ctx, rdb, sid, "")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("user key set → blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		sub := "user-uuid-banned"
		require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:user:%s", sub), "ban", 0).Err())

		ok, err := isBlacklisted(ctx, rdb, "", sub)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("keys exist but different values → not blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		ok, err := isBlacklisted(ctx, rdb, "sid-unknown", "user-unknown")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("both keys set → blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		sid, sub := "sid-both", "user-both"
		require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:sid:%s", sid), "logout", 0).Err())
		require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:user:%s", sub), "ban", 0).Err())

		ok, err := isBlacklisted(ctx, rdb, sid, sub)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("only sid set, sub provided but not blacklisted → still blacklisted", func(t *testing.T) {
		rdb := newRedis(t)
		sid := "sid-set"
		require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:sid:%s", sid), "logout", 0).Err())

		ok, err := isBlacklisted(ctx, rdb, sid, "user-not-banned")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("redis error → returns error", func(t *testing.T) {
		rdb, mr := newRedisWithError(t)
		mr.SetError("FAKE_ERROR")

		_, err := isBlacklisted(ctx, rdb, "sid-1", "sub-1")
		assert.Error(t, err)
	})
}

// ─── JWTAuth middleware ──────────────────────────────────────────────────────

func TestJWTAuth_NoAuthorizationHeader(t *testing.T) {
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	w := get(r, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTAuth_WrongPrefix(t *testing.T) {
	claims := validClaims("user-1", "jti-1", "sid-1")
	token := makeHMACToken(t, testSecret, claims)

	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	w := get(r, "Token "+token)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	token := makeHMACToken(t, testSecret, expiredClaims("user-1", "jti-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

func TestJWTAuth_InvalidSignature(t *testing.T) {
	token := makeHMACToken(t, "wrong-secret", validClaims("user-1", "jti-1", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

func TestJWTAuth_ValidHMAC_PassesThrough(t *testing.T) {
	token := makeHMACToken(t, testSecret, validClaims("user-1", "jti-1", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_ValidHMAC_SetsContextValues(t *testing.T) {
	claims := validClaims("user-42", "jti-2", "sid-2")
	token := makeHMACToken(t, testSecret, claims)

	var capturedUserID string
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil), func(c *gin.Context) {
		capturedUserID = c.GetString("user_id")
		c.Status(http.StatusOK)
	})

	get(r, bearer(token))
	assert.Equal(t, "user-42", capturedUserID)
}

func TestJWTAuth_WrongIssuer(t *testing.T) {
	claims := validClaims("user-1", "jti-1", "sid-1")
	claims["iss"] = "evil-issuer"
	token := makeHMACToken(t, testSecret, claims)

	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret, Issuer: "expected-issuer"}, nil, nil))
	w := get(r, bearer(token))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid issuer")
}

func TestJWTAuth_CorrectIssuer(t *testing.T) {
	claims := validClaims("user-1", "jti-1", "sid-1")
	claims["iss"] = "my-issuer"
	token := makeHMACToken(t, testSecret, claims)

	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret, Issuer: "my-issuer"}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

// JTI check is temporarily disabled — no intrusion-detection mechanism exists yet
// to trigger per-token revocation. The key is written on logout but not checked here.
func TestJWTAuth_JTIBlacklisted_NotChecked(t *testing.T) {
	ctx := context.Background()
	rdb := newRedis(t)
	jti := "revoked-jti-001"
	require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:jti:%s", jti), "logout", 0).Err())

	token := makeHMACToken(t, testSecret, validClaims("user-1", jti, "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, rdb, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_UserBlacklisted(t *testing.T) {
	ctx := context.Background()
	rdb := newRedis(t)
	sub := "banned-user-uuid"
	require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:user:%s", sub), "ban", 0).Err())

	token := makeHMACToken(t, testSecret, validClaims(sub, "jti-fresh", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, rdb, nil))
	w := get(r, bearer(token))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "token revoked")
}

// Session revocation is enforced immediately at the gateway by checking the blacklisted SID.
// A blacklisted SID must block existing access tokens.
func TestJWTAuth_SIDBlacklisted_BlocksAccessToken(t *testing.T) {
	ctx := context.Background()
	rdb := newRedis(t)
	sid := "revoked-session-sid"
	require.NoError(t, rdb.Set(ctx, fmt.Sprintf("blacklist:sid:%s", sid), "revoked", 0).Err())

	token := makeHMACToken(t, testSecret, validClaims("user-1", "jti-fresh", sid))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, rdb, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

func TestJWTAuth_RedisDown_FailsOpen(t *testing.T) {
	rdb, mr := newRedisWithError(t)
	mr.SetError("REDIS_DOWN")

	token := makeHMACToken(t, testSecret, validClaims("user-1", "jti-1", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, rdb, nil))
	// fail open: Redis errors should not block valid tokens
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_NilRedis_SkipsBlacklistCheck(t *testing.T) {
	token := makeHMACToken(t, testSecret, validClaims("user-1", "jti-1", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_EmailNotVerified(t *testing.T) {
	claims := validClaims("user-1", "jti-1", "sid-1")
	claims["email_verified"] = false
	token := makeHMACToken(t, testSecret, claims)

	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	w := get(r, bearer(token))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "email not verified")
}

func TestJWTAuth_EmailVerifiedTrue(t *testing.T) {
	claims := validClaims("user-1", "jti-1", "sid-1")
	claims["email_verified"] = true
	token := makeHMACToken(t, testSecret, claims)

	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_EmailVerifiedAbsent_NotChecked(t *testing.T) {
	// When email_verified is absent from token, the check is skipped
	token := makeHMACToken(t, testSecret, validClaims("user-1", "jti-1", "sid-1"))
	r := newRouter(JWTAuth(JWTAuthConfig{Secret: testSecret}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

// ─── EdDSA / JWKS ───────────────────────────────────────────────────────────

func TestJWTAuth_EdDSA_LocalPublicKey(t *testing.T) {
	pub, priv := generateEdDSA(t)
	token := makeEdDSAToken(t, priv, validClaims("user-ed", "jti-ed", "sid-ed"), "k1")

	r := newRouter(JWTAuth(JWTAuthConfig{PublicKeyPEM: edPublicKeyToPEM(pub)}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_EdDSA_WrongLocalKey(t *testing.T) {
	_, priv := generateEdDSA(t)
	wrongPub, _ := generateEdDSA(t)
	token := makeEdDSAToken(t, priv, validClaims("user-ed", "jti-ed", "sid-ed"), "")

	r := newRouter(JWTAuth(JWTAuthConfig{PublicKeyPEM: edPublicKeyToPEM(wrongPub)}, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

func TestJWTAuth_EdDSA_JWKS_KnownKID(t *testing.T) {
	pub, priv := generateEdDSA(t)
	kid := "jwks-kid-1"

	srv := jwksServer(t, pub, kid)
	defer srv.Close()

	token := makeEdDSAToken(t, priv, validClaims("user-jwks", "jti-jwks", "sid-jwks"), kid)
	r := newRouter(JWTAuth(JWTAuthConfig{JWKSURL: srv.URL}, nil, nil))
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
}

func TestJWTAuth_EdDSA_JWKS_UnknownKID(t *testing.T) {
	pub, _ := generateEdDSA(t)
	_, priv2 := generateEdDSA(t)

	// JWKS has pub under "known-kid", token uses priv2 under "unknown-kid"
	srv := jwksServer(t, pub, "known-kid")
	defer srv.Close()

	token := makeEdDSAToken(t, priv2, validClaims("user-1", "jti-1", "sid-1"), "unknown-kid")
	r := newRouter(JWTAuth(JWTAuthConfig{JWKSURL: srv.URL}, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

func TestJWTAuth_EdDSA_JWKS_CacheServedOnSecondRequest(t *testing.T) {
	pub, priv := generateEdDSA(t)
	kid := "cached-kid"

	callCount := 0
	jwksBody := buildJWKSBody(pub, kid)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksBody)
	}))
	defer srv.Close()

	mw := JWTAuth(JWTAuthConfig{JWKSURL: srv.URL, CacheTTL: 10 * time.Minute}, nil, nil)
	r := newRouter(mw)

	token := makeEdDSAToken(t, priv, validClaims("u", "j", "s"), kid)
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
	assert.Equal(t, http.StatusOK, get(r, bearer(token)).Code)
	assert.Equal(t, 1, callCount, "JWKS should only be fetched once within cache TTL")
}

func TestJWTAuth_EdDSA_JWKS_Unavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, priv := generateEdDSA(t)
	token := makeEdDSAToken(t, priv, validClaims("u", "j", "s"), "some-kid")
	r := newRouter(JWTAuth(JWTAuthConfig{JWKSURL: srv.URL}, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, get(r, bearer(token)).Code)
}

// ─── JWKS helpers ───────────────────────────────────────────────────────────

func buildJWKSBody(pub ed25519.PublicKey, kid string) []byte {
	x := base64.RawURLEncoding.EncodeToString([]byte(pub))
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": x},
		},
	})
	return body
}

func jwksServer(t *testing.T, pub ed25519.PublicKey, kid string) *httptest.Server {
	t.Helper()
	body := buildJWKSBody(pub, kid)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
}
