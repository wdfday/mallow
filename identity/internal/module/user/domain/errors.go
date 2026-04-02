package domain

import "errors"

var (
	// ErrInvalidUserRole is returned when user role is invalid
	ErrInvalidUserRole = errors.New("invalid user role")

	// ErrInvalidUserStatus is returned when user status is invalid
	ErrInvalidUserStatus = errors.New("invalid user status")

	// ErrInvalidEmail is returned when email format is invalid
	ErrInvalidEmail = errors.New("invalid email format")

	// ErrInvalidPhoneNumber is returned when phone number is invalid
	ErrInvalidPhoneNumber = errors.New("invalid phone number")

	// ErrWeakPassword is returned when password does not meet requirements
	ErrWeakPassword = errors.New("password does not meet requirements")

	// ErrAccountLocked is returned when account is locked
	ErrAccountLocked = errors.New("account is locked")

	// ErrAccountSuspended is returned when account is suspended
	ErrAccountSuspended = errors.New("account is suspended")

	// ErrEmailNotVerified is returned when email is not verified
	ErrEmailNotVerified = errors.New("email not verified")

	// ErrUserNotFound is returned when user is not found
	ErrUserNotFound = errors.New("user not found")

	// ErrEmailAlreadyExists is returned when email already exists
	ErrEmailAlreadyExists = errors.New("email already exists")

	// ErrPhoneNumberAlreadyExists is returned when phone number already exists
	ErrPhoneNumberAlreadyExists = errors.New("phone number already exists")
)
