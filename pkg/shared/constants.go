package shared

// HTTP status codes
const (
	StatusOK                  = 200
	StatusCreated             = 201
	StatusNoContent           = 204
	StatusBadRequest          = 400
	StatusUnauthorized        = 401
	StatusForbidden           = 403
	StatusNotFound            = 404
	StatusConflict            = 409
	StatusUnprocessableEntity = 422
	StatusInternalServerError = 500
)

// Pagination defaults
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
	DefaultPage     = 1
)

// Common response messages
const (
	MessageSuccess         = "Success"
	MessageCreated         = "Created successfully"
	MessageUpdated         = "Updated successfully"
	MessageDeleted         = "Deleted successfully"
	MessageNotFound        = "Resource not found"
	MessageBadRequest      = "Bad request"
	MessageUnauthorized    = "Unauthorized"
	MessageForbidden       = "Forbidden"
	MessageConflict        = "Conflict"
	MessageValidationError = "Validation error"
	MessageInternalError   = "Internal server error"
)
