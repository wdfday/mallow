package dto

import (
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
		FullName:        user.FullName,
		DisplayName:     user.DisplayName,
		PhoneNumber:     user.PhoneNumber,
		AvatarURL:       user.AvatarURL,
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

// UserToProfileResponse converts domain User to UserProfileResponse DTO
func UserToProfileResponse(user domain.User) UserProfileResponse {
	linked := make([]LinkedAccountResponse, len(user.LinkedAccounts))
	for i, a := range user.LinkedAccounts {
		linked[i] = LinkedAccountResponse{
			Provider:   a.Provider,
			ProviderID: a.ProviderID,
			Username:   a.Username,
			LinkedAt:   a.LinkedAt,
		}
	}
	return UserProfileResponse{
		ID:              user.ID.String(),
		Email:           user.Email,
		FullName:        user.FullName,
		DisplayName:     user.DisplayName,
		PhoneNumber:     user.PhoneNumber,
		DateOfBirth:     user.DateOfBirth,
		AvatarURL:       user.AvatarURL,
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
		FullName:         strings.TrimSpace(req.FullName),
		PhoneNumber:      phoneNumber,
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusPendingVerification,
		AnalyticsConsent: true,
	}

	user.ID = uuid.New()

	return user, nil
}

// ApplyUpdateUserProfileRequest converts UpdateUserProfileRequest to update map for GORM
func ApplyUpdateUserProfileRequest(req UpdateUserProfileRequest) (map[string]any, error) {
	updates := make(map[string]any)

	if req.FullName != nil {
		trimmed := strings.TrimSpace(*req.FullName)
		if trimmed == "" {
			return nil, domain.ErrInvalidEmail
		}
		updates["full_name"] = trimmed
	}

	if req.DisplayName != nil {
		trimmed := strings.TrimSpace(*req.DisplayName)
		if trimmed == "" {
			updates["display_name"] = nil
		} else {
			updates["display_name"] = trimmed
		}
	}

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
