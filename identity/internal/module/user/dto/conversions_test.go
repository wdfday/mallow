package dto

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

func stringPtr(s string) *string {
	return &s
}

func TestUserToResponse(t *testing.T) {
	t.Run("converts user to response with all fields", func(t *testing.T) {
		userID := uuid.New()
		now := time.Now()
		verifiedAt := now.Add(-24 * time.Hour)
		loginAt := now.Add(-1 * time.Hour)
		displayName := "Display Name"
		phone := "+1234567890"
		avatar := "https://example.com/avatar.jpg"

		user := domain.User{
			ID:              userID,
			Email:           "test@example.com",
			FullName:        "Test User",
			DisplayName:     &displayName,
			PhoneNumber:     &phone,
			AvatarURL:       &avatar,
			Role:            domain.UserRoleAdmin,
			Status:          domain.UserStatusActive,
			EmailVerified:   true,
			EmailVerifiedAt: &verifiedAt,
			LastLoginAt:     &loginAt,
			LastActiveAt:    now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		response := UserToResponse(user)

		assert.Equal(t, userID.String(), response.ID)
		assert.Equal(t, "test@example.com", response.Email)
		assert.Equal(t, "Test User", response.FullName)
		assert.Equal(t, "Display Name", *response.DisplayName)
		assert.Equal(t, "+1234567890", *response.PhoneNumber)
		assert.Equal(t, "https://example.com/avatar.jpg", *response.AvatarURL)
		assert.Equal(t, "admin", response.Role)
		assert.Equal(t, "active", response.Status)
		assert.True(t, response.EmailVerified)
	})

	t.Run("handles nil optional fields", func(t *testing.T) {
		user := domain.User{
			ID:          uuid.New(),
			Email:       "test@example.com",
			FullName:    "Test",
			DisplayName: nil,
			PhoneNumber: nil,
			AvatarURL:   nil,
			Role:        domain.UserRoleUser,
		}

		response := UserToResponse(user)

		assert.Nil(t, response.DisplayName)
		assert.Nil(t, response.PhoneNumber)
		assert.Nil(t, response.AvatarURL)
	})
}

func TestUserToProfileResponse(t *testing.T) {
	t.Run("converts user to profile response", func(t *testing.T) {
		userID := uuid.New()
		now := time.Now()
		dob := time.Date(1990, 1, 15, 0, 0, 0, 0, time.UTC)

		user := domain.User{
			ID:          userID,
			Email:       "test@example.com",
			FullName:    "Test User",
			DateOfBirth: &dob,
			Role:        domain.UserRoleUser,
			Status:      domain.UserStatusActive,
			CreatedAt:   now,
		}

		response := UserToProfileResponse(user)

		assert.Equal(t, userID.String(), response.ID)
		assert.Equal(t, "test@example.com", response.Email)
		assert.NotNil(t, response.DateOfBirth)
		assert.Equal(t, dob, *response.DateOfBirth)
	})
}

func TestUsersPageToResponse(t *testing.T) {
	t.Run("converts page of users to page of responses", func(t *testing.T) {
		users := []domain.User{
			{ID: uuid.New(), Email: "user1@example.com", Role: domain.UserRoleUser},
			{ID: uuid.New(), Email: "user2@example.com", Role: domain.UserRoleAdmin},
		}

		page := shared.Page[domain.User]{
			Data:         users,
			TotalItems:   100,
			TotalPages:   5,
			ItemsPerPage: 20,
			CurrentPage:  1,
		}

		result := UsersPageToResponse(page)

		assert.Len(t, result.Data, 2)
		assert.Equal(t, int64(100), result.TotalItems)
		assert.Equal(t, 5, result.TotalPages)
		assert.Equal(t, 20, result.ItemsPerPage)
		assert.Equal(t, 1, result.CurrentPage)
		assert.Equal(t, "user1@example.com", result.Data[0].Email)
		assert.Equal(t, "user2@example.com", result.Data[1].Email)
	})

	t.Run("handles empty page", func(t *testing.T) {
		page := shared.Page[domain.User]{
			Data:         []domain.User{},
			TotalItems:   0,
			TotalPages:   0,
			ItemsPerPage: 20,
			CurrentPage:  1,
		}

		result := UsersPageToResponse(page)

		assert.Len(t, result.Data, 0)
		assert.Equal(t, int64(0), result.TotalItems)
	})
}

func TestFromCreateUserRequest(t *testing.T) {
	t.Run("creates user from valid request", func(t *testing.T) {
		phone := "+1234567890"
		req := CreateUserRequest{
			Email:       "Test@Example.COM  ",
			Password:    "SecurePassword123!",
			FullName:    "  Test User  ",
			PhoneNumber: &phone,
		}

		user, err := FromCreateUserRequest(req)

		assert.NoError(t, err)
		assert.NotNil(t, user)
		assert.Equal(t, "test@example.com", user.Email) // Lowercased and trimmed
		assert.Equal(t, "Test User", user.FullName)     // Trimmed
		assert.Equal(t, "+1234567890", *user.PhoneNumber)
		assert.Equal(t, domain.UserRoleUser, user.Role)
		assert.Equal(t, domain.UserStatusPendingVerification, user.Status)
		assert.True(t, user.AnalyticsConsent)
	})

	t.Run("returns error for empty email", func(t *testing.T) {
		req := CreateUserRequest{
			Email:    "   ",
			Password: "password",
			FullName: "Test",
		}

		user, err := FromCreateUserRequest(req)

		assert.Error(t, err)
		assert.Nil(t, user)
		assert.Equal(t, domain.ErrInvalidEmail, err)
	})

	t.Run("handles nil phone number", func(t *testing.T) {
		req := CreateUserRequest{
			Email:       "test@example.com",
			Password:    "password",
			FullName:    "Test",
			PhoneNumber: nil,
		}

		user, err := FromCreateUserRequest(req)

		assert.NoError(t, err)
		assert.Nil(t, user.PhoneNumber)
	})

	t.Run("handles empty phone number string", func(t *testing.T) {
		phone := "   "
		req := CreateUserRequest{
			Email:       "test@example.com",
			Password:    "password",
			FullName:    "Test",
			PhoneNumber: &phone,
		}

		user, err := FromCreateUserRequest(req)

		assert.NoError(t, err)
		assert.Nil(t, user.PhoneNumber)
	})
}

func TestApplyUpdateUserProfileRequest(t *testing.T) {
	t.Run("applies full name update", func(t *testing.T) {
		fullName := "New Full Name"
		req := UpdateUserProfileRequest{
			FullName: &fullName,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "New Full Name", updates["full_name"])
	})

	t.Run("returns error for empty full name", func(t *testing.T) {
		emptyName := "   "
		req := UpdateUserProfileRequest{
			FullName: &emptyName,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.Error(t, err)
		assert.Nil(t, updates)
	})

	t.Run("applies display name update", func(t *testing.T) {
		displayName := "New Display"
		req := UpdateUserProfileRequest{
			DisplayName: &displayName,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "New Display", updates["display_name"])
	})

	t.Run("clears display name with empty string", func(t *testing.T) {
		emptyDisplay := "   "
		req := UpdateUserProfileRequest{
			DisplayName: &emptyDisplay,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Nil(t, updates["display_name"])
	})

	t.Run("applies phone number update", func(t *testing.T) {
		phone := "+9876543210"
		req := UpdateUserProfileRequest{
			PhoneNumber: &phone,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "+9876543210", updates["phone_number"])
	})

	t.Run("clears phone number with empty string", func(t *testing.T) {
		emptyPhone := ""
		req := UpdateUserProfileRequest{
			PhoneNumber: &emptyPhone,
		}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Nil(t, updates["phone_number"])
	})

	t.Run("returns empty map for no updates", func(t *testing.T) {
		req := UpdateUserProfileRequest{}

		updates, err := ApplyUpdateUserProfileRequest(req)

		assert.NoError(t, err)
		assert.Len(t, updates, 0)
	})
}
