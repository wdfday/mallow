package validation

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrRequired = errors.New("required")

	uuidRe = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[1-5][a-fA-F0-9]{3}-[89abAB][a-fA-F0-9]{3}-[a-fA-F0-9]{12}$`)
)

// RequiredTrimmed trims spaces and ensures the value is not empty.
func RequiredTrimmed(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", fmt.Errorf("%s: %w", field, ErrRequired)
	}
	return v, nil
}

// IsUUID reports whether value is a canonical UUID string.
func IsUUID(value string) bool {
	return uuidRe.MatchString(strings.TrimSpace(value))
}

// NormalizeSymbol uppercases and trims trading symbol text.
func NormalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}
