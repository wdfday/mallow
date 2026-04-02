package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

const LoggerKey = "logger"

// LoggerMiddleware injects the slog logger into the Gin context
func LoggerMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(LoggerKey, logger)
		c.Next()
	}
}

// GetLogger retrieves the slog logger from the Gin context
// Returns the default slog logger if not found or nil in context
func GetLogger(c *gin.Context) *slog.Logger {
	if logger, exists := c.Get(LoggerKey); exists {
		if slogLogger, ok := logger.(*slog.Logger); ok && slogLogger != nil {
			return slogLogger
		}
	}
	return slog.Default()
}
