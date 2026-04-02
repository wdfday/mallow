package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// ServiceAuth middleware tests
// ---------------------------------------------------------------------------

func setupServiceAuthRoute(secret string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/internal", ServiceAuth(secret), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func TestServiceAuth_CorrectSecret_Passes(t *testing.T) {
	router := setupServiceAuthRoute("my-super-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Service-Secret", "my-super-secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServiceAuth_WrongSecret_Unauthorized(t *testing.T) {
	router := setupServiceAuthRoute("my-super-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Service-Secret", "wrong-secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServiceAuth_MissingHeader_Unauthorized(t *testing.T) {
	router := setupServiceAuthRoute("my-super-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServiceAuth_EmptyConfiguredSecret_AlwaysUnauthorized(t *testing.T) {
	// Even if the caller sends an empty header, it should be rejected.
	router := setupServiceAuthRoute("")

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Service-Secret", "")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServiceAuth_EmptyHeaderValue_Unauthorized(t *testing.T) {
	router := setupServiceAuthRoute("my-super-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Service-Secret", "")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
