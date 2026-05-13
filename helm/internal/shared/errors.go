package shared

import (
	"net/http"
	"strconv"

	pkgshared "mallow/pkg/shared"
)

// Re-export generic codes from pkg/shared for convenience.
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

// Numeric error codes — helm-specific.
// Ranges:
//
//	20000–20099  helm lifecycle
//	20100–20199  hand lifecycle
//	20200–20299  exchange
const (
	// Helm lifecycle errors
	CodeErrHelmNotFound           = 20000
	CodeErrHelmDisabled           = 20001 // admin soft-lock; no trading allowed
	CodeErrHelmPaused             = 20002 // cascade-paused; all hands suspended
	CodeErrHelmHalted             = 20003 // post-kill; requires /halt/reset
	CodeErrHelmRuntimeUnavailable = 20004 // in-memory runtime not yet spawned

	// Hand lifecycle errors
	CodeErrHandNotFound         = 20100
	CodeErrHandRunning          = 20101 // must stop before update/delete
	CodeErrHandNotRunning       = 20102 // can't stop/kill what is already stopped
	CodeErrHandInsufficientCap  = 20103
	CodeErrHandNoFuturesSupport = 20104 // exchange does not support futures

	// Exchange errors
	CodeErrExchangeOrderFailed = 20200
	CodeErrExchangeUnsupported = 20201
)

type AppError = pkgshared.AppError
type ErrorResponse struct {
	Status  int            `json:"status"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func errCode(n int) string { return strconv.Itoa(n) }

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

	// Helm errors
	ErrHelmNotFound           = NewAppError(errCode(CodeErrHelmNotFound), "Helm not found", http.StatusNotFound)
	ErrHelmDisabled           = NewAppError(errCode(CodeErrHelmDisabled), "Helm is disabled", http.StatusForbidden)
	ErrHelmPaused             = NewAppError(errCode(CodeErrHelmPaused), "Helm is paused", http.StatusConflict)
	ErrHelmHalted             = NewAppError(errCode(CodeErrHelmHalted), "Helm is halted — call /halt/reset first", http.StatusConflict)
	ErrHelmRuntimeUnavailable = NewAppError(errCode(CodeErrHelmRuntimeUnavailable), "Helm runtime not available", http.StatusServiceUnavailable)

	// Hand errors
	ErrHandNotFound         = NewAppError(errCode(CodeErrHandNotFound), "Hand not found", http.StatusNotFound)
	ErrHandRunning          = NewAppError(errCode(CodeErrHandRunning), "Hand is running — stop it first", http.StatusConflict)
	ErrHandNotRunning       = NewAppError(errCode(CodeErrHandNotRunning), "Hand is not running", http.StatusConflict)
	ErrHandInsufficientCap  = NewAppError(errCode(CodeErrHandInsufficientCap), "Insufficient capital", http.StatusUnprocessableEntity)
	ErrHandNoFuturesSupport = NewAppError(errCode(CodeErrHandNoFuturesSupport), "Exchange does not support futures trading", http.StatusUnprocessableEntity)

	// Exchange errors
	ErrExchangeOrderFailed = NewAppError(errCode(CodeErrExchangeOrderFailed), "Exchange rejected the order", http.StatusBadGateway)
	ErrExchangeUnsupported = NewAppError(errCode(CodeErrExchangeUnsupported), "Operation not supported by this exchange", http.StatusUnprocessableEntity)
)
