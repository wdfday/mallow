package shared

import (
	"errors"
	"maps"
	"net/http"
)

// Error codes
const (
	ErrCodeValidation    = "VALIDATION_ERROR"
	ErrCodeNotFound      = "NOT_FOUND"
	ErrCodeUnauthorized  = "UNAUTHORIZED"
	ErrCodeForbidden     = "FORBIDDEN"
	ErrCodeConflict      = "CONFLICT"
	ErrCodeInternal      = "INTERNAL_ERROR"
	ErrCodeBadRequest    = "BAD_REQUEST"
	ErrCodeUnprocessable = "UNPROCESSABLE_ENTITY"

	ErrCodeUserNotFound    = "USER_NOT_FOUND"
	ErrCodeUserExists      = "USER_EXISTS"
	ErrCodeTokenNotFound   = "TOKEN_NOT_FOUND"
	ErrCodeTokenExpired    = "TOKEN_EXPIRED"
	ErrCodeTokenUsed       = "TOKEN_USED"
	ErrCodeTokenInvalid    = "TOKEN_INVALID"
	ErrCodeProfileNotFound = "PROFILE_NOT_FOUND"
)

// AppError is a structured application error with HTTP status and error code.
type AppError struct {
	Code       string
	Message    string
	StatusCode int
	Details    map[string]any
	Err        error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Err }

func NewAppError(code, message string, statusCode int) *AppError {
	return &AppError{Code: code, Message: message, StatusCode: statusCode, Details: make(map[string]any)}
}

func (e *AppError) clone() *AppError {
	if e == nil {
		return nil
	}

	cloned := *e
	cloned.Details = maps.Clone(e.Details)
	if cloned.Details == nil {
		cloned.Details = make(map[string]any)
	}
	return &cloned
}

func (e *AppError) WithDetails(key string, value any) *AppError {
	cloned := e.clone()
	cloned.Details[key] = value
	return cloned
}

func (e *AppError) WithError(err error) *AppError {
	cloned := e.clone()
	cloned.Err = err
	return cloned
}

// ErrorResponse is the JSON body for error responses.
type ErrorResponse struct {
	Status  int            `json:"status"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *AppError) ToResponse() ErrorResponse {
	d := maps.Clone(e.Details)
	if d == nil {
		d = make(map[string]any)
	}
	messageStr := e.Message
	if detailMessage, ok := d["message"].(string); ok && detailMessage != "" {
		messageStr = detailMessage
	}
	delete(d, "message")

	return ErrorResponse{
		Status:  e.StatusCode,
		Code:    e.Code,
		Message: messageStr,
		Details: d,
	}
}

func IsAppError(err error) bool {
	var appErr *AppError
	return errors.As(err, &appErr)
}

func ToAppError(err error) *AppError {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return ErrInternal.WithError(err)
}

// Predefined errors
var (
	ErrValidation    = NewAppError(ErrCodeValidation, "Validation error", http.StatusBadRequest)
	ErrNotFound      = NewAppError(ErrCodeNotFound, "Resource not found", http.StatusNotFound)
	ErrUnauthorized  = NewAppError(ErrCodeUnauthorized, "Unauthorized", http.StatusUnauthorized)
	ErrForbidden     = NewAppError(ErrCodeForbidden, "Forbidden", http.StatusForbidden)
	ErrConflict      = NewAppError(ErrCodeConflict, "Resource conflict", http.StatusConflict)
	ErrInternal      = NewAppError(ErrCodeInternal, "Internal server error", http.StatusInternalServerError)
	ErrBadRequest    = NewAppError(ErrCodeBadRequest, "Bad request", http.StatusBadRequest)
	ErrUnprocessable = NewAppError(ErrCodeUnprocessable, "Unprocessable entity", http.StatusUnprocessableEntity)

	ErrUserNotFound    = NewAppError(ErrCodeUserNotFound, "User not found", http.StatusNotFound)
	ErrUserExists      = NewAppError(ErrCodeUserExists, "User already exists", http.StatusConflict)
	ErrTokenNotFound   = NewAppError(ErrCodeTokenNotFound, "Token not found", http.StatusNotFound)
	ErrTokenExpired    = NewAppError(ErrCodeTokenExpired, "Token has expired", http.StatusUnauthorized)
	ErrTokenUsed       = NewAppError(ErrCodeTokenUsed, "Token has already been used", http.StatusUnauthorized)
	ErrTokenInvalid    = NewAppError(ErrCodeTokenInvalid, "Invalid token", http.StatusUnauthorized)
	ErrProfileNotFound = NewAppError(ErrCodeProfileNotFound, "Profile not found", http.StatusNotFound)
)
