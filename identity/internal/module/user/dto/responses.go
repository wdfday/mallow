package dto

import (
	"time"
)

// LinkedAccountResponse is the public representation of a linked social account.
type LinkedAccountResponse struct {
	Provider   string    `json:"provider"`
	ProviderID string    `json:"provider_id"`
	Username   string    `json:"username,omitempty"`
	LinkedAt   time.Time `json:"linked_at"`
}

// Response DTOs for user
type UserResponse struct {
	ID              string     `json:"id"`
	Email           string     `json:"email"`
	FullName        string     `json:"full_name"`
	DisplayName     *string    `json:"display_name,omitempty"`
	PhoneNumber     *string    `json:"phone_number,omitempty"`
	AvatarURL       *string    `json:"avatar_url,omitempty"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	EmailVerified   bool       `json:"email_verified"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	LastActiveAt    time.Time  `json:"last_active_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type UserListResponse struct {
	Items      []UserResponse `json:"items"`
	Total      int64          `json:"total"`
	Page       int            `json:"page"`
	PerPage    int            `json:"per_page"`
	TotalPages int            `json:"total_pages"`
}

type UserProfileResponse struct {
	ID              string                  `json:"id"`
	Email           string                  `json:"email"`
	FullName        string                  `json:"full_name"`
	DisplayName     *string                 `json:"display_name,omitempty"`
	PhoneNumber     *string                 `json:"phone_number,omitempty"`
	DateOfBirth     *time.Time              `json:"date_of_birth,omitempty"`
	AvatarURL       *string                 `json:"avatar_url,omitempty"`
	Role            string                  `json:"role"`
	Status          string                  `json:"status"`
	EmailVerified   bool                    `json:"email_verified"`
	EmailVerifiedAt *time.Time              `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time              `json:"last_login_at,omitempty"`
	LastActiveAt    time.Time               `json:"last_active_at"`
	LinkedAccounts  []LinkedAccountResponse `json:"linked_accounts"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}
