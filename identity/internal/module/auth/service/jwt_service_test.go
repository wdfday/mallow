package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	userDomain "mallow/identity/internal/module/user/domain"
)

func TestNewJWTService_EdDSASignAndValidate(t *testing.T) {
	privatePEM, publicPEM := mustGenerateEd25519PEM(t)

	svc, err := NewJWTService("", privatePEM, publicPEM, "k1", "http://identity.local", time.Hour, 24*time.Hour)
	require.NoError(t, err)

	token, _, err := svc.GenerateAccessToken("u-1", "u@test.dev", userDomain.UserRoleUser)
	require.NoError(t, err)

	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)
	assert.Equal(t, "u-1", claims.UserID)
	assert.Equal(t, "u@test.dev", claims.Email)
	assert.Equal(t, userDomain.UserRoleUser, claims.Role)
}

func TestNewJWTService_EdDSASigningWithLegacyHSValidation(t *testing.T) {
	privatePEM, publicPEM := mustGenerateEd25519PEM(t)
	legacySecret := "legacy-secret"

	svc, err := NewJWTService(legacySecret, privatePEM, publicPEM, "k1", "http://identity.local", time.Hour, 24*time.Hour)
	require.NoError(t, err)

	hsToken := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: "u-legacy",
		Email:  "legacy@test.dev",
		Role:   userDomain.UserRoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	hsTokenString, err := hsToken.SignedString([]byte(legacySecret))
	require.NoError(t, err)

	claims, err := svc.ValidateToken(hsTokenString)
	require.NoError(t, err)
	assert.Equal(t, "u-legacy", claims.UserID)
	assert.Equal(t, userDomain.UserRoleAdmin, claims.Role)
}

func TestNewJWTService_MissingSigningCredentials(t *testing.T) {
	_, err := NewJWTService("", "", "", "", "", time.Hour, time.Hour)
	require.Error(t, err)
}

func mustGenerateEd25519PEM(t *testing.T) (privatePEM string, publicPEM string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	privPKCS8, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privPKCS8}))

	pubPKIX, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubPKIX}))

	return privatePEM, publicPEM
}
