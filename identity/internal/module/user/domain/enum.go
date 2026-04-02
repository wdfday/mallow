package domain

type UserRole string

const (
	UserRoleAdmin UserRole = "admin"
	UserRoleUser  UserRole = "user"
)

type UserStatus string

const (
	UserStatusActive              UserStatus = "active"
	UserStatusPendingVerification UserStatus = "pending_verification"
	UserStatusSuspended           UserStatus = "suspended"
)

// ValidateUserRole checks if the given role is valid
func ValidateUserRole(role UserRole) bool {
	switch role {
	case UserRoleAdmin, UserRoleUser:
		return true
	default:
		return false
	}
}

// ValidateUserStatus checks if the given status is valid
func ValidateUserStatus(status UserStatus) bool {
	switch status {
	case UserStatusActive, UserStatusPendingVerification, UserStatusSuspended:
		return true
	default:
		return false
	}
}
