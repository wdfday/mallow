package domain

import (
	"time"

	"github.com/google/uuid"
)

// Session represents an authenticated user session linked to a token pair by SID.
type Session struct {
	SID       string    `gorm:"column:sid;primaryKey;size:36"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	IP        string    `gorm:"size:45"`
	UserAgent string    `gorm:"size:512"`
	ExpiresAt time.Time `gorm:"not null"`
	CreatedAt time.Time
	RevokedAt *time.Time `gorm:"index"`
}

func (Session) TableName() string { return "sessions" }
