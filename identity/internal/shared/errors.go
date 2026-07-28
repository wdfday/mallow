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
type ErrorResponse = pkgshared.ErrorResponse

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

// Handler-layer sentinels for the two most common request-level failures, so every
// handler produces the same structured {status, code, message, details} shape instead
// of ad-hoc raw strings via RespondWithError.
var (
	ErrInvalidRequestBody = ErrValidation.WithDetails("message", "invalid request data")
	ErrUserNotInContext   = ErrUnauthorized.WithDetails("message", "user not found in context")
)

// WrapRepoErr translates a repository-layer error into a client-facing AppError at the
// service boundary: an existing AppError passes through unchanged (its real code/status
// survives), anything else is wrapped as Internal so the opaque cause never leaks into
// the response body. Standard replacement for the repeated
//
//	if shared.IsAppError(err) { return err }
//	return shared.ErrInternal.WithError(err)
//
// idiom scattered across service methods that call straight through to a repository.
func WrapRepoErr(err error) error {
	if err == nil {
		return nil
	}
	if IsAppError(err) {
		return err
	}
	return ErrInternal.WithError(err)
}
