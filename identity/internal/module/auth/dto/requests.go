package dto

// Request DTOs for authentication
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email,max=255" example:"admin@example.com"`
	Password string `json:"password" binding:"required,min=8,max=128" example:"Admin@123456"`
	IP       string `json:"-"` // populated from request IP
}

type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email,max=255"`
	Password string `json:"password" binding:"required,min=8,max=128"`
	FullName string `json:"full_name" binding:"required,min=1,max=255"`
	Phone    string `json:"phone" binding:"omitempty,max=20"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8,max=128"`
}

type ForgotPasswordRequest struct {
	Email string `json:"email" binding:"required,email"`
}

type ResetPasswordRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=128"`
}

type VerifyEmailRequest struct {
	Token string `json:"token" binding:"required"`
}

type ResendVerificationRequest struct {
	Email string `json:"email" binding:"required,email"`
}

type GoogleAuthRequest struct {
	Token string `json:"token" binding:"required"`
}

type ConfirmTelegramLinkRequest struct {
	OTP string `json:"otp" binding:"required"`
}

type UnlinkTelegramRequest struct {
	ProviderID string `json:"provider_id" binding:"required"`
}
