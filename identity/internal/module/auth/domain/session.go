package domain

import (
	"time"

	"github.com/google/uuid"
)

// Session represents an authenticated user session linked to a token pair by SID.
type Session struct {
	SID       string
	UserID    uuid.UUID
	IP        string
	UserAgent string
	ExpiresAt time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}
