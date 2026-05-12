package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// IdentityClient calls identity's internal blacklist-check endpoint.
// Used by JWTAuth as a fallback when Redis is unavailable.
type IdentityClient struct {
	baseURL string
	secret  string
	client  *http.Client
}

// NewIdentityClient constructs an IdentityClient. If secret is empty the client is a no-op
// (CheckRevoked always returns false, nil) so callers need not nil-check.
func NewIdentityClient(baseURL, secret string) *IdentityClient {
	return &IdentityClient{
		baseURL: baseURL,
		secret:  secret,
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

// CheckRevoked queries /api/v1/internal/blacklist/check and returns whether the session is revoked.
// A missing or empty secret disables the check (returns false, nil).
func (c *IdentityClient) CheckRevoked(ctx context.Context, sid, sub string) (bool, error) {
	if c.secret == "" {
		return false, nil
	}
	params := url.Values{}
	if sid != "" {
		params.Set("sid", sid)
	}
	if sub != "" {
		params.Set("sub", sub)
	}
	target := c.baseURL + "/api/v1/internal/blacklist/check?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Service-Secret", c.secret)

	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("identity blacklist check: status %d", resp.StatusCode)
	}
	var body struct {
		Blacklisted bool `json:"blacklisted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.Blacklisted, nil
}
