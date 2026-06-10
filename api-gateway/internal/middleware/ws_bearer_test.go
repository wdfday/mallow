package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func runWSBearer(t *testing.T, proto, existingAuth string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest("GET", "/api/v1/stream", nil)
	if proto != "" {
		req.Header.Set("Sec-WebSocket-Protocol", proto)
	}
	if existingAuth != "" {
		req.Header.Set("Authorization", existingAuth)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	WSBearerFromProtocol()(c)
	return c.Request.Header.Get("Authorization")
}

func TestWSBearer_ExtractsToken(t *testing.T) {
	if got := runWSBearer(t, "bearer, tok123", ""); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want Bearer tok123", got)
	}
}

func TestWSBearer_PreservesExistingAuth(t *testing.T) {
	if got := runWSBearer(t, "bearer, tok123", "Bearer original"); got != "Bearer original" {
		t.Fatalf("Authorization = %q, want Bearer original", got)
	}
}

func TestWSBearer_NoProtocol(t *testing.T) {
	if got := runWSBearer(t, "", ""); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
}

func TestWSBearer_NonBearerProtocol(t *testing.T) {
	if got := runWSBearer(t, "graphql-ws", ""); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
}
