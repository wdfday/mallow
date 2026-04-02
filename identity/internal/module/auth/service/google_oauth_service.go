package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mallow/identity/internal/module/auth/domain"
	"net/http"

	"mallow/identity/internal/shared"
)

const (
	// GoogleTokenInfoURL is the endpoint to verify Google OAuth tokens
	GoogleTokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"
)

// GoogleOAuthService handles Google OAuth operations
type GoogleOAuthService struct {
	httpClient *http.Client
}

// NewGoogleOAuthService creates a new Google OAuth service
func NewGoogleOAuthService() *GoogleOAuthService {
	return &GoogleOAuthService{
		httpClient: &http.Client{},
	}
}

// VerifyGoogleToken verifies a Google OAuth token and returns user info
func (s *GoogleOAuthService) VerifyGoogleToken(ctx context.Context, token string) (*domain.GoogleUserInfo, error) {
	// Build request URL with token
	url := fmt.Sprintf("%s?id_token=%s", GoogleTokenInfoURL, token)

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	// Execute request
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, shared.ErrUnauthorized.WithDetails(
			"message", "invalid Google token",
		).WithDetails(
			"details", string(body),
		)
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	var tokenInfo struct {
		Sub           string `json:"sub"` // Google user ID
		Email         string `json:"email"`
		EmailVerified string `json:"email_verified"` // "true" or "false" as string
		Name          string `json:"name"`
		Picture       string `json:"picture"`
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
		Locale        string `json:"locale"`
	}

	if err := json.Unmarshal(body, &tokenInfo); err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	// Validate required fields
	if tokenInfo.Email == "" || tokenInfo.Sub == "" {
		return nil, shared.ErrUnauthorized.WithDetails("message", "incomplete Google user info")
	}

	// Convert to domain model
	userInfo := &domain.GoogleUserInfo{
		ID:            tokenInfo.Sub,
		Email:         tokenInfo.Email,
		Name:          tokenInfo.Name,
		Picture:       tokenInfo.Picture,
		VerifiedEmail: tokenInfo.EmailVerified == "true",
	}

	return userInfo, nil
}
