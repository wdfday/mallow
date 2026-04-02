package dto

import (
	"mallow/identity/internal/module/user/domain"
	"time"
)

// NewAuthResponse creates an AuthResponse from user and token data
func NewAuthResponse(user *domain.User, accessToken string, expiresAt int64) *AuthResponse {
	now := time.Now().Unix()
	expiresIn := expiresAt - now
	if expiresIn < 0 {
		expiresIn = 0
	}

	return &AuthResponse{
		User: UserAuthInfo{
			ID:            user.ID.String(),
			Email:         user.Email,
			FullName:      user.FullName,
			DisplayName:   user.DisplayName,
			AvatarURL:     user.AvatarURL,
			Role:          string(user.Role),
			Status:        string(user.Status),
			EmailVerified: user.EmailVerified,
			MFAEnabled:    user.MFAEnabled,
			CreatedAt:     user.CreatedAt,
			LastLoginAt:   user.LastLoginAt,
		},
		Token: TokenInfo{
			AccessToken: accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   expiresIn,
			ExpiresAt:   expiresAt,
		},
	}
}

// NewTokenResponse creates a TokenResponse from token data
func NewTokenResponse(accessToken string, expiresIn int64) *TokenResponse {
	return &TokenResponse{
		AccessToken: accessToken,
		ExpiresIn:   expiresIn,
		TokenType:   "Bearer",
	}
}
