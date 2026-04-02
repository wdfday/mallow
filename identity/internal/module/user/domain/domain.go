package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`

	// Authentication & Identity
	Email       string  `gorm:"type:citext;uniqueIndex:uniq_email_active,where:deleted_at IS NULL;column:email" json:"email"`
	Password    string  `gorm:"type:text;column:password" json:"-"`
	PhoneNumber *string `gorm:"uniqueIndex:uniq_phone_active,where:deleted_at IS NULL;column:phone_number" json:"phone_number,omitempty"`

	// Personal Information
	FullName    string     `gorm:"column:full_name" json:"full_name"`
	DisplayName *string    `gorm:"column:display_name" json:"display_name,omitempty"`
	DateOfBirth *time.Time `gorm:"column:date_of_birth" json:"date_of_birth,omitempty"`
	AvatarURL   *string    `gorm:"column:avatar_url" json:"avatar_url,omitempty"`

	// Account Status
	Role            UserRole   `gorm:"type:varchar(20);default:'user';column:role" json:"role"`
	Status          UserStatus `gorm:"type:varchar(30);default:'pending_verification';column:status" json:"status"`
	EmailVerified   bool       `gorm:"column:email_verified" json:"email_verified"`
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at" json:"email_verified_at,omitempty"`

	// Security
	MFAEnabled        bool       `gorm:"column:mfa_enabled" json:"mfa_enabled"`
	MFASecret         *string    `gorm:"type:text;column:mfa_secret" json:"-"`
	LastLoginAt       *time.Time `gorm:"column:last_login_at" json:"last_login_at,omitempty"`
	LastLoginIP       *string    `gorm:"column:last_login_ip" json:"last_login_ip,omitempty"`
	LoginAttempts     int        `gorm:"default:0;column:login_attempts" json:"login_attempts"`
	LockedUntil       *time.Time `gorm:"column:locked_until" json:"locked_until,omitempty"`
	PasswordChangedAt time.Time  `gorm:"autoCreateTime;column:password_changed_at" json:"password_changed_at"`

	// Consent & Terms
	TermsAccepted      bool       `gorm:"column:terms_accepted" json:"terms_accepted"`
	TermsAcceptedAt    *time.Time `gorm:"column:terms_accepted_at" json:"terms_accepted_at,omitempty"`
	DataSharingConsent bool       `gorm:"column:data_sharing_consent" json:"data_sharing_consent"`
	AnalyticsConsent   bool       `gorm:"default:true;column:analytics_consent" json:"analytics_consent"`
	MarketingConsent   bool       `gorm:"column:marketing_consent" json:"marketing_consent"`

	// External accounts (stored as JSONB; use GIN index for fast lookup)
	LinkedAccounts []LinkedAccount `gorm:"type:jsonb;serializer:json;column:linked_accounts" json:"linked_accounts,omitempty"`

	// Activity Tracking
	LastActiveAt time.Time `gorm:"index;column:last_active_at" json:"last_active_at"`

	CreatedAt time.Time      `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at" json:"deleted_at,omitempty"`
}

// TableName matches the database table.
func (User) TableName() string {
	return "users"
}

// ========== Helper Methods ==========

// IsAdmin checks if user has admin role
func (u *User) IsAdmin() bool {
	return u.Role == UserRoleAdmin
}

// IsActive checks if user account is active
func (u *User) IsActive() bool {
	return u.Status == UserStatusActive
}

// IsPendingVerification checks if user is pending email verification
func (u *User) IsPendingVerification() bool {
	return u.Status == UserStatusPendingVerification
}

// IsSuspended checks if user account is suspended
func (u *User) IsSuspended() bool {
	return u.Status == UserStatusSuspended
}

// IsEmailVerified checks if user email is verified
func (u *User) IsEmailVerified() bool {
	return u.EmailVerified
}

// IsMFAEnabled checks if MFA is enabled
func (u *User) IsMFAEnabled() bool {
	return u.MFAEnabled
}

// IsLocked checks if account is currently locked
func (u *User) IsLocked() bool {
	if u.LockedUntil == nil {
		return false
	}
	return time.Now().Before(*u.LockedUntil)
}

// CanLogin checks if user can login (not locked, not suspended, email verified)
func (u *User) CanLogin() bool {
	return !u.IsLocked() && !u.IsSuspended() && u.IsEmailVerified()
}

// LinkedAccount represents a linked external/social account stored in JSONB.
type LinkedAccount struct {
	Provider   string    `json:"provider"`           // "telegram", "discord", etc.
	ProviderID string    `json:"provider_id"`        // external account ID
	Username   string    `json:"username,omitempty"` // display handle, e.g. "@john"
	LinkedAt   time.Time `json:"linked_at"`
}

// GetLinkedAccount returns the linked account for the given provider, or nil if not found.
func (u *User) GetLinkedAccount(provider string) *LinkedAccount {
	for i := range u.LinkedAccounts {
		if u.LinkedAccounts[i].Provider == provider {
			return &u.LinkedAccounts[i]
		}
	}
	return nil
}

type ListUsersFilter struct {
	Query         string // tìm theo email/full_name/display_name
	Role          *UserRole
	Status        *UserStatus
	ActiveOnly    bool // lọc deleted_at IS NULL (mặc định true ở repo)
	EmailVerified *bool
}
