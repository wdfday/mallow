package shared

import pkgshared "mallow/pkg/shared"

const (
	ErrCodeValidation      = pkgshared.ErrCodeValidation
	ErrCodeNotFound        = pkgshared.ErrCodeNotFound
	ErrCodeUnauthorized    = pkgshared.ErrCodeUnauthorized
	ErrCodeForbidden       = pkgshared.ErrCodeForbidden
	ErrCodeConflict        = pkgshared.ErrCodeConflict
	ErrCodeInternal        = pkgshared.ErrCodeInternal
	ErrCodeBadRequest      = pkgshared.ErrCodeBadRequest
	ErrCodeUnprocessable   = pkgshared.ErrCodeUnprocessable
	ErrCodeUserNotFound    = pkgshared.ErrCodeUserNotFound
	ErrCodeUserExists      = pkgshared.ErrCodeUserExists
	ErrCodeTokenNotFound   = pkgshared.ErrCodeTokenNotFound
	ErrCodeTokenExpired    = pkgshared.ErrCodeTokenExpired
	ErrCodeTokenUsed       = pkgshared.ErrCodeTokenUsed
	ErrCodeTokenInvalid    = pkgshared.ErrCodeTokenInvalid
	ErrCodeProfileNotFound = pkgshared.ErrCodeProfileNotFound
)

type AppError = pkgshared.AppError
type ErrorResponse struct {
	Status  int            `json:"status"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

var (
	NewAppError = pkgshared.NewAppError
	IsAppError  = pkgshared.IsAppError
	ToAppError  = pkgshared.ToAppError

	ErrValidation      = pkgshared.ErrValidation
	ErrNotFound        = pkgshared.ErrNotFound
	ErrUnauthorized    = pkgshared.ErrUnauthorized
	ErrForbidden       = pkgshared.ErrForbidden
	ErrConflict        = pkgshared.ErrConflict
	ErrInternal        = pkgshared.ErrInternal
	ErrBadRequest      = pkgshared.ErrBadRequest
	ErrUnprocessable   = pkgshared.ErrUnprocessable
	ErrUserNotFound    = pkgshared.ErrUserNotFound
	ErrUserExists      = pkgshared.ErrUserExists
	ErrTokenNotFound   = pkgshared.ErrTokenNotFound
	ErrTokenExpired    = pkgshared.ErrTokenExpired
	ErrTokenUsed       = pkgshared.ErrTokenUsed
	ErrTokenInvalid    = pkgshared.ErrTokenInvalid
	ErrProfileNotFound = pkgshared.ErrProfileNotFound
)
