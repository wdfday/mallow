package dto

import "errors"

var (
	// Validation errors
	ErrInvalidBrokerType      = errors.New("invalid broker type")
	ErrOKXCredentialsRequired = errors.New("OKX credentials (api_key, api_secret, passphrase) are required")
)
