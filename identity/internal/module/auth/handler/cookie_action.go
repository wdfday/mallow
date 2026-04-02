package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	refreshTokenCookieName = "refresh_token"
)

// getRefreshTokenFromCookie gets the refresh token from cookie
func (h *AuthHandler) getRefreshTokenFromCookie(c *gin.Context) (string, error) {
	return c.Cookie(refreshTokenCookieName)
}

// setRefreshTokenCookie sets the refresh token in an HTTP-only cookie
func (h *AuthHandler) setRefreshTokenCookie(c *gin.Context, refreshToken string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		refreshTokenCookieName, // name
		refreshToken,           // value
		7*24*60*60,             // maxAge: 7 days in seconds
		"/",                    // path
		"",                     // domain
		false,                  // secure
		true,                   // httpOnly
	)
}

// clearRefreshTokenCookie clears the refresh token cookie
func (h *AuthHandler) clearRefreshTokenCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		refreshTokenCookieName,
		"",
		-1,    // maxAge -1 deletes the cookie
		"/",   // path
		"",    // domain
		false, // secure
		true,  // httpOnly
	)
}
