package domain

import "time"

// TokenType represents the type of verification token.
type TokenType string

const (
	TokenTypeEmailVerification TokenType = "email_verification"
	TokenTypePasswordReset     TokenType = "password_reset"
)

// VerificationToken is the in-memory representation of a short-lived token.
// Storage is Redis — there is no database table for this type.
type VerificationToken struct {
	UserID    string
	Token     string
	Type      string
	ExpiresAt time.Time
	IPAddress *string
	UserAgent *string
}

// IsExpired reports whether the token's TTL has passed.
func (t *VerificationToken) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// IsValid reports whether the token can still be used.
func (t *VerificationToken) IsValid() bool {
	return !t.IsExpired()
}
