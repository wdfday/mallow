package dto

// Request DTOs for user
type CreateUserRequest struct {
	Email       string  `json:"email" binding:"required,email,max=255"`
	Password    string  `json:"password" binding:"required,min=6,max=100"`
	FullName    string  `json:"full_name" binding:"required,min=1,max=255"`
	PhoneNumber *string `json:"phone_number,omitempty" binding:"omitempty,max=20"`
}

type UpdateUserProfileRequest struct {
	FullName    *string `json:"full_name,omitempty" binding:"omitempty,min=1,max=255"`
	DisplayName *string `json:"display_name,omitempty" binding:"omitempty,max=255"`
	PhoneNumber *string `json:"phone_number,omitempty" binding:"omitempty,max=20"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=6,max=100"`
}

type ListUsersRequest struct {
	Page    int    `form:"page" binding:"omitempty,min=1"`
	PerPage int    `form:"per_page" binding:"omitempty,min=1,max=100"`
	Query   string `form:"query"`
	Role    string `form:"role" binding:"omitempty,oneof=admin user"`
	Status  string `form:"status" binding:"omitempty,oneof=active pending_verification suspended"`
	Sort    string `form:"sort"`
}

type ChangeUserRoleRequest struct {
	Role string `json:"role" binding:"required,oneof=admin user"`
}
