package shared

import pkgshared "mallow/pkg/shared"

// HTTP status codes (compatibility aliases)
const (
	StatusOK                  = pkgshared.StatusOK
	StatusCreated             = pkgshared.StatusCreated
	StatusNoContent           = pkgshared.StatusNoContent
	StatusBadRequest          = pkgshared.StatusBadRequest
	StatusUnauthorized        = pkgshared.StatusUnauthorized
	StatusForbidden           = pkgshared.StatusForbidden
	StatusNotFound            = pkgshared.StatusNotFound
	StatusConflict            = pkgshared.StatusConflict
	StatusUnprocessableEntity = pkgshared.StatusUnprocessableEntity
	StatusInternalServerError = pkgshared.StatusInternalServerError
)

// Common response messages (compatibility aliases)
const (
	MessageSuccess         = pkgshared.MessageSuccess
	MessageCreated         = pkgshared.MessageCreated
	MessageUpdated         = pkgshared.MessageUpdated
	MessageDeleted         = pkgshared.MessageDeleted
	MessageNotFound        = pkgshared.MessageNotFound
	MessageBadRequest      = pkgshared.MessageBadRequest
	MessageUnauthorized    = pkgshared.MessageUnauthorized
	MessageForbidden       = pkgshared.MessageForbidden
	MessageConflict        = pkgshared.MessageConflict
	MessageValidationError = pkgshared.MessageValidationError
	MessageInternalError   = pkgshared.MessageInternalError
)

// Pagination defaults (compatibility aliases)
const (
	DefaultPageSize = pkgshared.DefaultPageSize
	MaxPageSize     = pkgshared.MaxPageSize
	DefaultPage     = pkgshared.DefaultPage
)
