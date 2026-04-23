package service

import (
	"context"
	"mallow/identity/internal/module/auth/dto"
	userDomain "mallow/identity/internal/module/user/domain"
	"time"
)

// Compile-time interface compliance checks
var (
	_ IPasswordService     = (*PasswordService)(nil)
	_ IJWTService          = (*JWTService)(nil)
	_ ITokenService        = (*TokenService)(nil)
	_ IVerificationService = (*VerificationService)(nil)
	_ IAuthService         = (*Service)(nil)
)

// IPasswordService defines password management operations
type IPasswordService interface {
	// Hash helpers
	HashPassword(password string) (string, error)
	VerifyPassword(hashedPassword, password string) error
	IsValidPassword(password string) bool
	ValidatePasswordStrength(password string) []string

	// Business operations
	ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordRequest) error
	ForgotPassword(ctx context.Context, email, ipAddress, userAgent string) error
	ResetPassword(ctx context.Context, tokenStr, newPassword string) error
}

// IJWTService defines JWT token operations
type IJWTService interface {
	GenerateAccessToken(userID, email, sessionID string, role userDomain.UserRole) (string, int64, error)
	GenerateRefreshToken(userID, sessionID string) (string, int64, error)
	ValidateToken(tokenString string) (*Claims, error)
	ValidateRefreshToken(tokenString string) (*RefreshClaims, error)
	ExtractSessionID(tokenString string) string
}

// ITokenService defines random token generation operations
type ITokenService interface {
	GenerateToken() (string, error)
	GenerateTokenWithPrefix(prefix string) (string, error)
	GetEmailVerificationExpiry() time.Time
	GetPasswordResetExpiry() time.Time
	ValidateUUID(id string) bool
}

// IVerificationService defines email verification operations
type IVerificationService interface {
	SendVerificationEmail(ctx context.Context, userID, ipAddress, userAgent string) error
	VerifyEmail(ctx context.Context, tokenStr string) error
	ResendVerificationEmail(ctx context.Context, email, ipAddress, userAgent string) error
	CleanupExpiredTokens(ctx context.Context) error
}

// IAuthService defines core authentication operations
type IAuthService interface {
	Register(ctx context.Context, req dto.RegisterRequest) (*dto.AuthResult, error)
	Login(ctx context.Context, req dto.LoginRequest) (*dto.AuthResult, error)
	Logout(ctx context.Context, userID, refreshToken, ipAddress string) error
	RefreshToken(ctx context.Context, refreshToken string) (*dto.TokenResponse, error)
	AuthenticateGoogle(ctx context.Context, req dto.GoogleAuthRequest) (*dto.AuthResult, error)
}
