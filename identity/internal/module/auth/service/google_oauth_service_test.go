package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/shared"
)

// newTestGoogleOAuth creates a GoogleOAuthService whose httpClient talks to the given test server.
func newTestGoogleOAuth(serverURL string) *GoogleOAuthService {
	return &GoogleOAuthService{
		httpClient: &http.Client{
			Transport: &rewriteTransport{baseURL: serverURL},
		},
	}
}

// rewriteTransport rewrites all outgoing requests to point at a test server.
type rewriteTransport struct {
	baseURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to use our test server, preserving query params
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[len("http://"):]
	return http.DefaultTransport.RoundTrip(req)
}

func TestVerifyGoogleToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "id_token=valid-token")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"sub":            "google-123",
			"email":          "user@gmail.com",
			"email_verified": "true",
			"name":           "Test User",
			"picture":        "https://example.com/photo.jpg",
		})
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "valid-token")

	require.NoError(t, err)
	assert.Equal(t, "google-123", info.ID)
	assert.Equal(t, "user@gmail.com", info.Email)
	assert.Equal(t, "Test User", info.Name)
	assert.Equal(t, "https://example.com/photo.jpg", info.Picture)
	assert.True(t, info.VerifiedEmail)
}

func TestVerifyGoogleToken_EmailNotVerified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"sub":            "google-456",
			"email":          "unverified@gmail.com",
			"email_verified": "false",
			"name":           "Unverified",
		})
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "some-token")

	require.NoError(t, err)
	assert.False(t, info.VerifiedEmail)
	assert.Equal(t, "unverified@gmail.com", info.Email)
}

func TestVerifyGoogleToken_InvalidToken_Returns401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid_token"}`))
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "bad-token")

	assert.Nil(t, info)
	require.Error(t, err)
	appErr := shared.ToAppError(err)
	assert.Equal(t, http.StatusUnauthorized, appErr.StatusCode)
}

func TestVerifyGoogleToken_InvalidJSON_ReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "token")

	assert.Nil(t, info)
	require.Error(t, err)
}

func TestVerifyGoogleToken_MissingEmail_Returns401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"sub":  "google-789",
			"name": "No Email",
		})
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "token")

	assert.Nil(t, info)
	require.Error(t, err)
	appErr := shared.ToAppError(err)
	assert.Equal(t, http.StatusUnauthorized, appErr.StatusCode)
}

func TestVerifyGoogleToken_MissingSub_Returns401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"email": "user@gmail.com",
			"name":  "No Sub",
		})
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	info, err := svc.VerifyGoogleToken(context.Background(), "token")

	assert.Nil(t, info)
	require.Error(t, err)
}

func TestVerifyGoogleToken_ServerUnreachable_ReturnsError(t *testing.T) {
	// Point to a server that doesn't exist
	svc := newTestGoogleOAuth("http://127.0.0.1:1")
	info, err := svc.VerifyGoogleToken(context.Background(), "token")

	assert.Nil(t, info)
	require.Error(t, err)
}

func TestVerifyGoogleToken_CancelledContext_ReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"sub":   "123",
			"email": "user@example.com",
		})
	}))
	defer ts.Close()

	svc := newTestGoogleOAuth(ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	info, err := svc.VerifyGoogleToken(ctx, "token")
	assert.Nil(t, info)
	require.Error(t, err)
}
