package dto

import (
	userdto "mallow/identity/internal/module/user/dto"
	"time"
)

// Response DTOs for authentication
type AuthResponse struct {
	User  UserAuthInfo `json:"user"`
	Token TokenInfo    `json:"token"`
}

// UserAuthInfo contains authenticated user information
type UserAuthInfo struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	FullName      string     `json:"full_name"`
	DisplayName   *string    `json:"display_name,omitempty"`
	AvatarURL     *string    `json:"avatar_url,omitempty"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	EmailVerified bool       `json:"email_verified"`
	MFAEnabled    bool       `json:"mfa_enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
}

// TokenInfo contains token information
type TokenInfo struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"` // seconds until expiration
	ExpiresAt   int64  `json:"expires_at"` // unix timestamp
}

// LegacyAuthResponse is the old response format (deprecated, for backwards compatibility)
type LegacyAuthResponse struct {
	User         userdto.UserResponse `json:"user"`
	AccessToken  string               `json:"access_token"`
	RefreshToken string               `json:"refresh_token"` // No longer returned, use cookie
	ExpiresIn    int64                `json:"expires_in"`
	TokenType    string               `json:"token_type"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type MessageResponse struct {
	Message string `json:"message"`
	Success bool   `json:"success"`
}

type TelegramBotInfoResponse struct {
	BotUsername string `json:"bot_username"`
	Link        string `json:"link"`
}

type TelegramUserLookupResponse struct {
	ID       string                         `json:"id"`
	Email    string                         `json:"email"`
	FullName string                         `json:"full_name"`
	Telegram *userdto.LinkedAccountResponse `json:"telegram,omitempty"`
}
