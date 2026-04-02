package service

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// TokenService handles token generation
type TokenService struct {
	tokenLength int
}

// NewTokenService creates a new token service
func NewTokenService() *TokenService {
	return &TokenService{
		tokenLength: 32, // 32 bytes = 64 hex characters
	}
}

// GenerateToken generates a cryptographically secure random token
func (s *TokenService) GenerateToken() (string, error) {
	bytes := make([]byte, s.tokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateTokenWithPrefix generates a token with a prefix for easy identification
func (s *TokenService) GenerateTokenWithPrefix(prefix string) (string, error) {
	token, err := s.GenerateToken()
	if err != nil {
		return "", err
	}
	return prefix + "_" + token, nil
}

// GetEmailVerificationExpiry returns expiration time for email verification (24 hours)
func (s *TokenService) GetEmailVerificationExpiry() time.Time {
	return time.Now().Add(24 * time.Hour)
}

// GetPasswordResetExpiry returns expiration time for password reset (1 hour)
func (s *TokenService) GetPasswordResetExpiry() time.Time {
	return time.Now().Add(1 * time.Hour)
}

// ValidateUUID validates if a string is a valid UUID
func (s *TokenService) ValidateUUID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}
