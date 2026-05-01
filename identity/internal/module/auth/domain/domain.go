package domain

import (
	"mallow/identity/internal/module/user/domain"

	"github.com/google/uuid"
)

type GoogleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	VerifiedEmail bool   `json:"verified_email"`
	Audience      string `json:"audience"`
	//Sub		  	  string `json:"sub"`
}

// AuthUser represents a minimal user information for authentication context
type AuthUser struct {
	ID       uuid.UUID       `json:"id"`
	Username string          `json:"username"`
	Role     domain.UserRole `json:"role"`
}
