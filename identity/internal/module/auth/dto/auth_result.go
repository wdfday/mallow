package dto

import (
	profiledomain "mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/user/domain"
)

// AuthResult contains the result of an authentication operation
// This is an internal type used between service and handler layers
type AuthResult struct {
	User         *domain.User
	Profile      *profiledomain.UserProfile
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64  // unix timestamp when access token expires
	SessionID    string // SID shared between the access and refresh token pair
}
