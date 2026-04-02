package helper

import (
	"strings"
	"unicode"
)

const (
	minPasswordLength        = 8
	maxPasswordLength        = 128
	requiredCategoryCoverage = 3
)

// List of common/weak passwords (lowercase for comparison)
var commonPasswords = map[string]struct{}{
	"password":    {},
	"password123": {},
	"12345678":    {},
	"123456789":   {},
	"qwerty":      {},
	"qwerty123":   {},
	"admin":       {},
	"admin123":    {},
	"letmein":     {},
	"welcome":     {},
	"welcome123":  {},
	"monkey":      {},
	"dragon":      {},
	"master":      {},
	"sunshine":    {},
	"princess":    {},
	"football":    {},
	"iloveyou":    {},
	"abc123":      {},
	"123123":      {},
}

// IsPasswordStrong verifies if the password meets baseline security requirements.
func IsPasswordStrong(password string) bool {
	length := len(password)
	if length < minPasswordLength || length > maxPasswordLength {
		return false
	}

	if _, exists := commonPasswords[strings.ToLower(password)]; exists {
		return false
	}

	categories := countPasswordCategories(password)
	return categories >= requiredCategoryCoverage
}

// PasswordValidationErrors returns detailed validation issues for client feedback.
func PasswordValidationErrors(password string) []string {
	var errors []string
	length := len(password)

	if length < minPasswordLength {
		errors = append(errors, "Password must be at least 8 characters long")
	}

	if length > maxPasswordLength {
		errors = append(errors, "Password must not exceed 128 characters")
	}

	if _, exists := commonPasswords[strings.ToLower(password)]; exists {
		errors = append(errors, "This password is too weak and easily guessable")
	}

	categories := countPasswordCategories(password)
	if categories < requiredCategoryCoverage {
		errors = append(errors, "Password must contain at least 3 of the following: uppercase letter, lowercase letter, digit, special character")
	}

	return errors
}

func countPasswordCategories(password string) int {
	var (
		hasUpper   bool
		hasLower   bool
		hasDigit   bool
		hasSpecial bool
	)

	for _, char := range password {
		switch {
		case unicode.IsUpper(char):
			hasUpper = true
		case unicode.IsLower(char):
			hasLower = true
		case unicode.IsDigit(char):
			hasDigit = true
		case unicode.IsPunct(char) || unicode.IsSymbol(char):
			hasSpecial = true
		}
	}

	count := 0
	if hasUpper {
		count++
	}
	if hasLower {
		count++
	}
	if hasDigit {
		count++
	}
	if hasSpecial {
		count++
	}

	return count
}
