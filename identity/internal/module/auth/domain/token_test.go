package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVerificationToken_IsExpired(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		tok := &VerificationToken{ExpiresAt: time.Now().Add(-time.Hour)}
		assert.True(t, tok.IsExpired())
	})
	t.Run("not expired", func(t *testing.T) {
		tok := &VerificationToken{ExpiresAt: time.Now().Add(time.Hour)}
		assert.False(t, tok.IsExpired())
	})
}

func TestVerificationToken_IsValid(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		tok := &VerificationToken{ExpiresAt: time.Now().Add(time.Hour)}
		assert.True(t, tok.IsValid())
	})
	t.Run("expired", func(t *testing.T) {
		tok := &VerificationToken{ExpiresAt: time.Now().Add(-time.Hour)}
		assert.False(t, tok.IsValid())
	})
}

func TestTokenType_Constants(t *testing.T) {
	assert.Equal(t, TokenType("email_verification"), TokenTypeEmailVerification)
	assert.Equal(t, TokenType("password_reset"), TokenTypePasswordReset)
}

func TestVerificationToken_Fields(t *testing.T) {
	ip := "192.168.1.1"
	ua := "test-agent"

	tok := &VerificationToken{
		UserID:    "user-123",
		Token:     "test_token_123",
		Type:      string(TokenTypeEmailVerification),
		ExpiresAt: time.Now().Add(time.Hour),
		IPAddress: &ip,
		UserAgent: &ua,
	}

	assert.Equal(t, "user-123", tok.UserID)
	assert.Equal(t, "test_token_123", tok.Token)
	assert.Equal(t, "email_verification", tok.Type)
	assert.Equal(t, "192.168.1.1", *tok.IPAddress)
	assert.Equal(t, "test-agent", *tok.UserAgent)
}
