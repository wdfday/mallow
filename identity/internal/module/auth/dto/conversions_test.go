package dto

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"mallow/identity/internal/module/user/domain"
)

func TestNewAuthResponse(t *testing.T) {
	t.Run("creates auth response with valid data", func(t *testing.T) {
		userID := uuid.New()
		now := time.Now()
		loginAt := now.Add(-1 * time.Hour)
		displayName := "Test Display"
		avatarURL := "https://example.com/avatar.jpg"

		user := &domain.User{
			ID:            userID,
			Email:         "test@example.com",
			FullName:      "Test User",
			DisplayName:   &displayName,
			AvatarURL:     &avatarURL,
			Role:          domain.UserRoleUser,
			Status:        domain.UserStatusActive,
			EmailVerified: true,
			MFAEnabled:    false,
			CreatedAt:     now,
			LastLoginAt:   &loginAt,
		}

		accessToken := "test_access_token"
		expiresAt := time.Now().Add(1 * time.Hour).Unix()

		response := NewAuthResponse(user, accessToken, expiresAt)

		assert.Equal(t, userID.String(), response.User.ID)
		assert.Equal(t, "test@example.com", response.User.Email)
		assert.Equal(t, "Test User", response.User.FullName)
		assert.NotNil(t, response.User.DisplayName)
		assert.Equal(t, "Test Display", *response.User.DisplayName)
		assert.Equal(t, "user", response.User.Role)
		assert.Equal(t, "active", response.User.Status)
		assert.True(t, response.User.EmailVerified)
		assert.False(t, response.User.MFAEnabled)
		assert.Equal(t, accessToken, response.Token.AccessToken)
		assert.Equal(t, "Bearer", response.Token.TokenType)
		assert.Equal(t, expiresAt, response.Token.ExpiresAt)
		assert.True(t, response.Token.ExpiresIn > 0)
	})

	t.Run("handles expired token with zero expires_in", func(t *testing.T) {
		user := &domain.User{
			ID:   uuid.New(),
			Role: domain.UserRoleUser,
		}
		expiresAt := time.Now().Add(-1 * time.Hour).Unix() // Expired

		response := NewAuthResponse(user, "token", expiresAt)

		assert.Equal(t, int64(0), response.Token.ExpiresIn)
	})

	t.Run("handles nil optional fields", func(t *testing.T) {
		user := &domain.User{
			ID:          uuid.New(),
			Email:       "test@example.com",
			FullName:    "Test",
			DisplayName: nil,
			AvatarURL:   nil,
			LastLoginAt: nil,
			Role:        domain.UserRoleUser,
		}

		response := NewAuthResponse(user, "token", time.Now().Add(1*time.Hour).Unix())

		assert.Nil(t, response.User.DisplayName)
		assert.Nil(t, response.User.AvatarURL)
		assert.Nil(t, response.User.LastLoginAt)
	})
}

func TestNewTokenResponse(t *testing.T) {
	t.Run("creates token response with valid data", func(t *testing.T) {
		accessToken := "new_access_token"
		expiresIn := int64(3600)

		response := NewTokenResponse(accessToken, expiresIn)

		assert.Equal(t, accessToken, response.AccessToken)
		assert.Equal(t, expiresIn, response.ExpiresIn)
		assert.Equal(t, "Bearer", response.TokenType)
	})

	t.Run("handles zero expires_in", func(t *testing.T) {
		response := NewTokenResponse("token", 0)

		assert.Equal(t, int64(0), response.ExpiresIn)
	})
}
