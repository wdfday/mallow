package shared

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// getLogger retrieves the slog logger from gin context; defaults to slog.Default().
func getLogger(c *gin.Context) *slog.Logger {
	const loggerKey = "logger"
	if logger, exists := c.Get(loggerKey); exists {
		if slogLogger, ok := logger.(*slog.Logger); ok {
			return slogLogger
		}
	}
	return slog.Default()
}

func RespondWithError(c *gin.Context, statusCode int, message string) {
	logger := getLogger(c)
	logger.Error("HTTP Error Response",
		"status_code", statusCode,
		"error", http.StatusText(statusCode),
		"message", message,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, ErrorResponse{
		Status:  statusCode,
		Message: message,
	})
}

func RespondWithAppError(c *gin.Context, err *AppError) {
	logger := getLogger(c)
	logger.Error("HTTP AppError Response",
		"status_code", err.StatusCode,
		"error_code", err.Code,
		"message", err.Message,
		"details", err.Details,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(err.StatusCode, err.ToResponse())
}

func RespondWithErrorDetails(c *gin.Context, statusCode int, message string, details map[string]interface{}) {
	logger := getLogger(c)
	logger.Error("HTTP Error Response with Details",
		"status_code", statusCode,
		"error", http.StatusText(statusCode),
		"message", message,
		"details", details,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, ErrorResponse{
		Status:  statusCode,
		Message: message,
		Details: details,
	})
}

func RespondWithSuccess[T any](c *gin.Context, statusCode int, message string, data T) {
	if message == "" {
		message = http.StatusText(statusCode)
	}

	logger := getLogger(c)
	logger.Info("HTTP Success Response",
		"status_code", statusCode,
		"message", message,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, SuccessResponse[T]{
		Status:  statusCode,
		Message: message,
		Data:    data,
	})
}

func RespondWithSuccessNoData(c *gin.Context, statusCode int, message string) {
	if message == "" {
		message = http.StatusText(statusCode)
	}

	logger := getLogger(c)
	logger.Info("HTTP Success Response (No Data)",
		"status_code", statusCode,
		"message", message,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, Success{
		Status:  statusCode,
		Message: message,
	})
}

func RespondWithNoContent(c *gin.Context) {
	logger := getLogger(c)
	logger.Info("HTTP No Content Response",
		"status_code", http.StatusNoContent,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.Status(http.StatusNoContent)
}

func RespondWithPagination[T any](c *gin.Context, statusCode int, data []T, pagination Page[T]) {
	pagination.Data = data

	logger := getLogger(c)
	logger.Info("HTTP Paginated Response",
		"status_code", statusCode,
		"total_items", pagination.TotalItems,
		"total_pages", pagination.TotalPages,
		"current_page", pagination.CurrentPage,
		"items_per_page", pagination.ItemsPerPage,
		"returned_items", len(data),
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, pagination)
}

func RespondWithPaginationTimeCursor[T any](c *gin.Context, statusCode int, data *T, pagination PaginationTimeCursor[T]) {
	pagination.Data = data

	logger := getLogger(c)
	logger.Info("HTTP Time-Cursor Paginated Response",
		"status_code", statusCode,
		"time_cursor", pagination.TimeCursor,
		"has_more", pagination.HasMore,
		"items_per_page", pagination.ItemsPerPage,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"client_ip", c.ClientIP(),
	)

	c.JSON(statusCode, pagination)
}

func HandleError(c *gin.Context, err error) {
	if appErr := ToAppError(err); appErr != nil {
		RespondWithAppError(c, appErr)
		return
	}
	RespondWithError(c, http.StatusInternalServerError, "Internal server error")
}

func ParseID(c *gin.Context, paramName string) (int64, *AppError) {
	idStr := c.Param(paramName)
	if idStr == "" {
		return 0, ErrBadRequest.WithDetails("param", paramName).WithDetails("reason", "missing parameter")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, ErrBadRequest.WithDetails("param", paramName).WithDetails("reason", "invalid format")
	}
	if id <= 0 {
		return 0, ErrBadRequest.WithDetails("param", paramName).WithDetails("reason", "must be positive")
	}
	return id, nil
}
