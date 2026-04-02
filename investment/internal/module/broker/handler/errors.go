package handler

import "errors"

var (
	ErrUserIDNotFound = errors.New("user ID not found in context")
	ErrInvalidUserID  = errors.New("invalid user ID in context")
)
