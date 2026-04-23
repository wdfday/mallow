package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	userDomain "mallow/identity/internal/module/user/domain"
)

// JWTService handles JWT token generation and validation
type JWTService struct {
	jwtSecret       string
	privateKey      ed25519.PrivateKey
	publicKey       ed25519.PublicKey
	keyID           string
	issuer          string
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// Claims represents JWT claims for access tokens
type Claims struct {
	UserID    string              `json:"user_id"`
	Email     string              `json:"email"`
	Role      userDomain.UserRole `json:"role"`
	SessionID string              `json:"sid"`
	jwt.RegisteredClaims
}

// RefreshClaims represents JWT claims for refresh tokens
type RefreshClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// NewJWTService creates a new JWT service.
// When both private/public keys are provided, EdDSA is used for signing.
// JWT secret is kept as a legacy verifier fallback for migration.
func NewJWTService(
	jwtSecret string,
	privateKeyPEM string,
	publicKeyPEM string,
	keyID string,
	issuer string,
	accessTTL time.Duration,
	refreshTTL time.Duration,
) (*JWTService, error) {
	privateKeyPEM = normalizePEM(privateKeyPEM)
	publicKeyPEM = normalizePEM(publicKeyPEM)

	svc := &JWTService{
		jwtSecret: jwtSecret,
		keyID:     keyID,
		issuer:    issuer,
	}

	if accessTTL > 0 {
		svc.accessTokenTTL = accessTTL
	} else {
		svc.accessTokenTTL = 1 * time.Hour
	}
	if refreshTTL > 0 {
		svc.refreshTokenTTL = refreshTTL
	} else {
		svc.refreshTokenTTL = 7 * 24 * time.Hour
	}

	hasAnyEdKey := privateKeyPEM != "" || publicKeyPEM != ""
	if hasAnyEdKey {
		if privateKeyPEM == "" || publicKeyPEM == "" {
			return nil, fmt.Errorf("both JWT_PRIVATE_KEY and JWT_PUBLIC_KEY are required for EdDSA")
		}
		privateKey, err := parseEd25519PrivateKey(privateKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse JWT_PRIVATE_KEY: %w", err)
		}
		publicKey, err := parseEd25519PublicKey(publicKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse JWT_PUBLIC_KEY: %w", err)
		}
		svc.privateKey = privateKey
		svc.publicKey = publicKey
	}

	if len(svc.privateKey) == 0 && svc.jwtSecret == "" {
		return nil, fmt.Errorf("missing JWT signing credentials: set EdDSA keys or JWT_SECRET")
	}

	return svc, nil
}

// GenerateAccessToken generates an access JWT token
func (s *JWTService) GenerateAccessToken(userID, email, sessionID string, role userDomain.UserRole) (string, int64, error) {
	now := time.Now()
	expiresAt := now.Add(s.accessTokenTTL)

	claims := Claims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    s.issuer,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
	}

	token, signingKey, err := s.newSignedToken(claims)
	if err != nil {
		return "", 0, err
	}
	tokenString, err := token.SignedString(signingKey)
	if err != nil {
		return "", 0, err
	}

	return tokenString, expiresAt.Unix(), nil
}

// GenerateRefreshToken generates a refresh token
func (s *JWTService) GenerateRefreshToken(userID, sessionID string) (string, int64, error) {
	now := time.Now()
	expiresAt := now.Add(s.refreshTokenTTL)

	claims := RefreshClaims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    s.issuer,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
	}

	token, signingKey, err := s.newSignedToken(claims)
	if err != nil {
		return "", 0, err
	}
	tokenString, err := token.SignedString(signingKey)
	if err != nil {
		return "", 0, err
	}

	return tokenString, expiresAt.Unix(), nil
}

// ValidateToken validates a JWT token and returns the claims
func (s *JWTService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, s.keyFunc)

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// ValidateRefreshToken validates a refresh token and returns its claims.
func (s *JWTService) ValidateRefreshToken(tokenString string) (*RefreshClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &RefreshClaims{}, s.keyFunc)
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*RefreshClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, fmt.Errorf("invalid refresh token")
}

// ExtractSessionID decodes the JWT payload without verification to read the 'sid' claim.
// Intentionally unverified — used only as a session lookup key, never for auth decisions.
func (s *JWTService) ExtractSessionID(tokenString string) string {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return ""
	}
	return c.SID
}

func (s *JWTService) newSignedToken(claims jwt.Claims) (*jwt.Token, any, error) {
	if len(s.privateKey) > 0 {
		token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
		if s.keyID != "" {
			token.Header["kid"] = s.keyID
		}
		return token, s.privateKey, nil
	}
	if s.jwtSecret != "" {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token, []byte(s.jwtSecret), nil
	}
	return nil, nil, fmt.Errorf("missing signing key")
}

func (s *JWTService) keyFunc(token *jwt.Token) (any, error) {
	switch token.Method.Alg() {
	case jwt.SigningMethodEdDSA.Alg():
		if len(s.publicKey) == 0 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	case jwt.SigningMethodHS256.Alg():
		if s.jwtSecret == "" {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(s.jwtSecret), nil
	default:
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}
}

func normalizePEM(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, `\n`, "\n")
}

func parseEd25519PrivateKey(keyPEM string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsedKey.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	return key, nil
}

func parseEd25519PublicKey(keyPEM string) (ed25519.PublicKey, error) {
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
