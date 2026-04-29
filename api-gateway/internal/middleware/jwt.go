package middleware

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

type JWTAuthConfig struct {
	Secret       string
	PublicKeyPEM string
	JWKSURL      string
	Issuer       string
	CacheTTL     time.Duration
}

type jwksCache struct {
	mu         sync.RWMutex
	keysByKid  map[string]ed25519.PublicKey
	keysNoKid  []ed25519.PublicKey
	expiresAt  time.Time
	httpClient *http.Client
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
}

// BlacklistFallback is called when Redis is unavailable to check revocation via an authoritative source.
type BlacklistFallback interface {
	CheckRevoked(ctx context.Context, sid, sub string) (bool, error)
}

// JWTAuth validates Bearer tokens, resolves EdDSA keys from local PEM or JWKS, and checks Redis blacklist.
// When Redis is unavailable it falls back to fallback (e.g. identity service) to wake the cache.
func JWTAuth(cfg JWTAuthConfig, rdb *redis.Client, fallback BlacklistFallback) gin.HandlerFunc {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}

	localPublicKey, err := parseEd25519PublicKey(normalizePEM(cfg.PublicKeyPEM))
	if err != nil && strings.TrimSpace(cfg.PublicKeyPEM) != "" {
		panic(fmt.Errorf("invalid JWT_PUBLIC_KEY: %w", err))
	}

	cache := &jwksCache{
		keysByKid: make(map[string]ed25519.PublicKey),
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
		},
	}

	keyFunc := func(t *jwt.Token) (any, error) {
		switch t.Method.Alg() {
		case jwt.SigningMethodEdDSA.Alg():
			if len(localPublicKey) > 0 {
				return localPublicKey, nil
			}
			return cache.resolveFromJWKS(t, cfg.JWKSURL, cfg.CacheTTL)
		case jwt.SigningMethodHS256.Alg():
			if cfg.Secret == "" {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(cfg.Secret), nil
		default:
			return nil, jwt.ErrSignatureInvalid
		}
	}

	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			slog.Warn("gateway auth rejected request",
				"path", c.Request.URL.Path,
				"reason", "missing_or_invalid_authorization_header",
				"auth_prefix", authPrefix(auth),
			)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid Authorization header"})
			return
		}

		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		tokenMeta := readTokenMeta(tokenStr)
		token, err := jwt.Parse(tokenStr, keyFunc)
		if err != nil || !token.Valid {
			attrs := []any{
				"path", c.Request.URL.Path,
				"reason", "invalid_token",
				"alg", tokenMeta.Alg,
				"kid", tokenMeta.Kid,
				"iss", tokenMeta.Iss,
				"sub", tokenMeta.Sub,
			}
			if err != nil {
				attrs = append(attrs, "detail", err.Error())
			}
			slog.Warn("gateway auth rejected request", attrs...)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			slog.Warn("gateway auth rejected request",
				"path", c.Request.URL.Path,
				"reason", "invalid_claims",
				"alg", tokenMeta.Alg,
				"kid", tokenMeta.Kid,
			)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid claims"})
			return
		}

		if cfg.Issuer != "" {
			iss, _ := claims["iss"].(string)
			if iss != cfg.Issuer {
				slog.Warn("gateway auth rejected request",
					"path", c.Request.URL.Path,
					"reason", "invalid_issuer",
					"expected_issuer", cfg.Issuer,
					"actual_issuer", iss,
					"kid", tokenMeta.Kid,
					"sub", claims["sub"],
				)
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid issuer"})
				return
			}
		}

		if rdb != nil {
			sub, _ := claims["sub"].(string)
			sid, _ := claims["sid"].(string)
			blacklisted, err := isBlacklisted(c.Request.Context(), rdb, sid, sub)
			if err != nil {
				slog.Warn("gateway auth redis unavailable, falling back to identity",
					"path", c.Request.URL.Path,
					"kid", tokenMeta.Kid,
					"sub", sub,
					"err", err,
				)
				if fallback != nil {
					blacklisted, err = fallback.CheckRevoked(c.Request.Context(), sid, sub)
					if err != nil {
						slog.Warn("gateway auth identity fallback failed, allowing request",
							"path", c.Request.URL.Path,
							"sub", sub,
							"err", err,
						)
					}
				}
			}
			if blacklisted {
				slog.Warn("gateway auth rejected request",
					"path", c.Request.URL.Path,
					"reason", "token_revoked",
					"kid", tokenMeta.Kid,
					"sub", sub,
					"sid", sid,
				)
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token revoked"})
				return
			}
		}

		if verified, exists := claims["email_verified"]; exists {
			if v, ok := verified.(bool); ok && !v {
				slog.Warn("gateway auth rejected request",
					"path", c.Request.URL.Path,
					"reason", "email_not_verified",
					"sub", claims["sub"],
				)
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "email not verified"})
				return
			}
		}

		slog.Info("gateway auth accepted request",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"kid", tokenMeta.Kid,
			"sub", claims["sub"],
		)
		c.Set("claims", claims)
		if sub, _ := claims["sub"].(string); sub != "" {
			c.Set("user_id", sub)
		} else {
			c.Set("user_id", claims["user_id"])
		}
		c.Next()
	}
}

type tokenMeta struct {
	Alg string
	Kid string
	Iss string
	Sub string
}

func readTokenMeta(tokenStr string) tokenMeta {
	parser := jwt.NewParser()
	token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return tokenMeta{}
	}

	meta := tokenMeta{}
	if alg, _ := token.Header["alg"].(string); alg != "" {
		meta.Alg = alg
	}
	if kid, _ := token.Header["kid"].(string); kid != "" {
		meta.Kid = kid
	}
	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if iss, _ := claims["iss"].(string); iss != "" {
			meta.Iss = iss
		}
		if sub, _ := claims["sub"].(string); sub != "" {
			meta.Sub = sub
		}
	}
	return meta
}

func authPrefix(auth string) string {
	auth = strings.TrimSpace(auth)
	if auth == "" {
		return ""
	}
	if i := strings.IndexByte(auth, ' '); i >= 0 {
		return auth[:i]
	}
	if len(auth) > 12 {
		return auth[:12]
	}
	return auth
}

func (c *jwksCache) resolveFromJWKS(token *jwt.Token, jwksURL string, ttl time.Duration) (any, error) {
	if jwksURL == "" {
		return nil, jwt.ErrSignatureInvalid
	}

	now := time.Now()
	c.mu.RLock()
	expiresAt := c.expiresAt
	c.mu.RUnlock()
	if expiresAt.IsZero() || now.After(expiresAt) {
		if err := c.refresh(jwksURL, ttl); err != nil {
			return nil, err
		}
	}

	kid, _ := token.Header["kid"].(string)

	c.mu.RLock()
	defer c.mu.RUnlock()
	if kid != "" {
		if key, ok := c.keysByKid[kid]; ok {
			return key, nil
		}
		return nil, jwt.ErrSignatureInvalid
	}
	if len(c.keysNoKid) > 0 {
		return c.keysNoKid[0], nil
	}
	return nil, jwt.ErrSignatureInvalid
}

func (c *jwksCache) refresh(jwksURL string, ttl time.Duration) error {
	req, err := http.NewRequest(http.MethodGet, jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch failed with status %d", resp.StatusCode)
	}

	var payload jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	byKid := make(map[string]ed25519.PublicKey)
	noKid := make([]ed25519.PublicKey, 0, len(payload.Keys))
	for _, key := range payload.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" || key.X == "" {
			continue
		}
		rawKey, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil {
			continue
		}
		pubKey := ed25519.PublicKey(rawKey)
		if len(pubKey) != ed25519.PublicKeySize {
			continue
		}
		if key.Kid != "" {
			byKid[key.Kid] = pubKey
		} else {
			noKid = append(noKid, pubKey)
		}
	}

	c.mu.Lock()
	c.keysByKid = byKid
	c.keysNoKid = noKid
	c.expiresAt = time.Now().Add(ttl)
	c.mu.Unlock()
	return nil
}

func normalizePEM(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, `\n`, "\n")
}

func parseEd25519PublicKey(keyPEM string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(keyPEM) == "" {
		return nil, nil
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	parsedKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsedKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not Ed25519")
	}
	return key, nil
}

func isBlacklisted(ctx context.Context, rdb *redis.Client, sid, sub string) (bool, error) {
	var keys []string
	if sid != "" {
		keys = append(keys, fmt.Sprintf("blacklist:sid:%s", sid))
	}
	if sub != "" {
		keys = append(keys, fmt.Sprintf("blacklist:user:%s", sub))
	}
	if len(keys) == 0 {
		return false, nil
	}
	n, err := rdb.Exists(ctx, keys...).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
