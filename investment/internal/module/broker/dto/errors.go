package dto

import "errors"

var (
	// Validation errors
	ErrInvalidBrokerType        = errors.New("invalid broker type")
	ErrSSICredentialsRequired   = errors.New("SSI credentials (consumer_id, consumer_secret) are required")
	ErrOKXCredentialsRequired   = errors.New("OKX credentials (api_key, api_secret, passphrase) are required")
	ErrSepayCredentialsRequired = errors.New("SePay credentials (api_key) are required")
)
