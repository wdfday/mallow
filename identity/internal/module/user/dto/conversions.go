package dto

import (
	"time"

	profiledomain "mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
	"strings"

	"github.com/google/uuid"
)

// ========== Entity to DTO Conversions ==========

// UserToResponse converts domain User to UserResponse DTO
func UserToResponse(user domain.User) UserResponse {
	return UserResponse{
		ID:              user.ID.String(),
		Email:           user.Email,
		PhoneNumber:     user.PhoneNumber,
		Role:            string(user.Role),
		Status:          string(user.Status),
		EmailVerified:   user.EmailVerified,
		EmailVerifiedAt: user.EmailVerifiedAt,
		LastLoginAt:     user.LastLoginAt,
		LastActiveAt:    user.LastActiveAt,
		CreatedAt:       user.CreatedAt,
		UpdatedAt:       user.UpdatedAt,
	}
}

// UserToProfileResponse converts domain User and UserProfile to UserProfileResponse DTO.
// profile may be nil for legacy users without a profile row.
func UserToProfileResponse(user domain.User, profile *profiledomain.UserProfile) UserProfileResponse {
	linked := make([]LinkedAccountResponse, len(user.LinkedAccounts))
	for i, a := range user.LinkedAccounts {
		linked[i] = LinkedAccountResponse{
			Provider:   a.Provider,
			ProviderID: a.ProviderID,
			Username:   a.Username,
			LinkedAt:   a.LinkedAt,
		}
	}

	var fullName string
	var displayName *string
	var dateOfBirth *time.Time
	var avatarURL *string
	if profile != nil {
		fullName = profile.FullName
		displayName = profile.DisplayName
		dateOfBirth = profile.DateOfBirth
		avatarURL = profile.AvatarURL
	}

	return UserProfileResponse{
		ID:              user.ID.String(),
		Email:           user.Email,
		FullName:        fullName,
		DisplayName:     displayName,
		PhoneNumber:     user.PhoneNumber,
		DateOfBirth:     dateOfBirth,
		AvatarURL:       avatarURL,
		Role:            string(user.Role),
		Status:          string(user.Status),
		EmailVerified:   user.EmailVerified,
		EmailVerifiedAt: user.EmailVerifiedAt,
		LastLoginAt:     user.LastLoginAt,
		LastActiveAt:    user.LastActiveAt,
		LinkedAccounts:  linked,
		CreatedAt:       user.CreatedAt,
		UpdatedAt:       user.UpdatedAt,
	}
}

// UsersPageToResponse converts paginated users to list response
func UsersPageToResponse(page shared.Page[domain.User]) shared.Page[UserResponse] {
	items := make([]UserResponse, len(page.Data))
	for i, user := range page.Data {
		items[i] = UserToResponse(user)
	}

	return shared.Page[UserResponse]{
		Data:         items,
		TotalItems:   page.TotalItems,
		TotalPages:   page.TotalPages,
		ItemsPerPage: page.ItemsPerPage,
		CurrentPage:  page.CurrentPage,
	}
}

// ========== DTO to Entity Conversions ==========

// FromCreateUserRequest converts CreateUserRequest DTO to User entity
func FromCreateUserRequest(req CreateUserRequest) (*domain.User, error) {
	// Validate and normalize email
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, domain.ErrInvalidEmail
	}

	// Normalize phone number
	var phoneNumber *string
	if req.PhoneNumber != nil {
		trimmed := strings.TrimSpace(*req.PhoneNumber)
		if trimmed != "" {
			phoneNumber = &trimmed
		}
	}

	// Create user entity
	user := &domain.User{
		Email:            email,
		Password:         req.Password, // Will be hashed by service layer
		PhoneNumber:      phoneNumber,
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusPendingVerification,
		AnalyticsConsent: true,
	}

	user.ID = uuid.New()

	return user, nil
}

// ApplyUpdateUserProfileRequest converts UpdateUserProfileRequest to update map for GORM.
// Only auth/security fields (PhoneNumber) are updated on the users table.
// Personal info (FullName, DisplayName) must be updated via profile service.
func ApplyUpdateUserProfileRequest(req UpdateUserProfileRequest) (map[string]any, error) {
	updates := make(map[string]any)

	if req.PhoneNumber != nil {
		trimmed := strings.TrimSpace(*req.PhoneNumber)
		if trimmed == "" {
			updates["phone_number"] = nil
		} else {
			updates["phone_number"] = trimmed
		}
	}

	return updates, nil
}
