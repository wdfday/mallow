package dto

import (
	profiledomain "mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/user/domain"
	"time"
)

// NewAuthResponse creates an AuthResponse from user, profile and token data.
// profile may be nil for legacy users without a profile row; personal info fields
// will default to zero values in that case.
func NewAuthResponse(user *domain.User, profile *profiledomain.UserProfile, accessToken string, expiresAt int64) *AuthResponse {
	now := time.Now().Unix()
	expiresIn := expiresAt - now
	if expiresIn < 0 {
		expiresIn = 0
	}

	var fullName string
	var displayName *string
	var avatarURL *string
	if profile != nil {
		fullName = profile.FullName
		displayName = profile.DisplayName
		avatarURL = profile.AvatarURL
	}

	return &AuthResponse{
		User: UserAuthInfo{
			ID:            user.ID.String(),
			Email:         user.Email,
			FullName:      fullName,
			DisplayName:   displayName,
			AvatarURL:     avatarURL,
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
