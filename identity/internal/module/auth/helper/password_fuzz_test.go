package helper

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzIsPasswordStrong tests password strength validation with random inputs
// Run: go test -fuzz=FuzzIsPasswordStrong -fuzztime=30s
func FuzzIsPasswordStrong(f *testing.F) {
	// Seed corpus with various password patterns
	f.Add("Password123!")
	f.Add("weak")
	f.Add("NoNumbers!")
	f.Add("nouppercaseorspecial123")
	f.Add("NOLOWERCASE123!")
	f.Add("NoSpecialChars123")
	f.Add("")
	f.Add("a1A!")
	f.Add(strings.Repeat("A", 100) + "1!")
	f.Add("密码123!")     // Chinese
	f.Add("пароль123!") // Russian
	f.Add("🔐Pass123!")  // Emoji

	f.Fuzz(func(t *testing.T, password string) {
		// Test that validation never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("IsPasswordStrong panicked with password: %q, panic: %v", password, r)
			}
		}()

		// Call the function
		isStrong := IsPasswordStrong(password)
		errors := PasswordValidationErrors(password)

		// Consistency check: strong password should have no errors
		if isStrong && len(errors) > 0 {
			t.Errorf("Password marked as strong but has errors: password=%q, errors=%v",
				password, errors)
		}

		// Weak password should have errors
		if !isStrong && len(errors) == 0 {
			t.Errorf("Password marked as weak but has no errors: password=%q", password)
		}

		// Manual validation for consistency
		hasMinLength := len(password) >= 8
		hasUpper := false
		hasLower := false
		hasDigit := false
		hasSpecial := false

		for _, char := range password {
			if unicode.IsUpper(char) {
				hasUpper = true
			}
			if unicode.IsLower(char) {
				hasLower = true
			}
			if unicode.IsDigit(char) {
				hasDigit = true
			}
			if strings.ContainsRune("!@#$%^&*()_+-=[]{}|;:,.<>?", char) {
				hasSpecial = true
			}
		}

		manualValidation := hasMinLength && hasUpper && hasLower && hasDigit && hasSpecial

		// Compare manual validation with function result
		if manualValidation != isStrong {
			t.Logf("Validation mismatch: password=%q, expected=%v, got=%v",
				password, manualValidation, isStrong)
			t.Logf("  hasMinLength=%v, hasUpper=%v, hasLower=%v, hasDigit=%v, hasSpecial=%v",
				hasMinLength, hasUpper, hasLower, hasDigit, hasSpecial)
		}
	})
}

// FuzzPasswordValidationErrors tests error reporting with random inputs
// Run: go test -fuzz=FuzzPasswordValidationErrors -fuzztime=30s
func FuzzPasswordValidationErrors(f *testing.F) {
	// Seed corpus
	f.Add("Password123!")
	f.Add("")
	f.Add("short")
	f.Add("alllowercase123!")
	f.Add("ALLUPPERCASE123!")
	f.Add("NoDigitsHere!")
	f.Add("NoSpecialChars123")

	f.Fuzz(func(t *testing.T, password string) {
		// Test that error reporting never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PasswordValidationErrors panicked: password=%q, panic=%v",
					password, r)
			}
		}()

		errors := PasswordValidationErrors(password)

		// Verify error messages are reasonable
		for _, errMsg := range errors {
			if errMsg == "" {
				t.Errorf("Empty error message returned for password: %q", password)
			}

			if len(errMsg) > 200 {
				t.Errorf("Error message too long (%d chars): %q", len(errMsg), errMsg)
			}
		}

		// If there are no errors, password should be strong
		if len(errors) == 0 && !IsPasswordStrong(password) {
			t.Errorf("No errors but password not marked as strong: %q", password)
		}
	})
}

// FuzzPasswordLength tests edge cases around password length
// Run: go test -fuzz=FuzzPasswordLength -fuzztime=30s
func FuzzPasswordLength(f *testing.F) {
	// Seed with various lengths
	f.Add(0)
	f.Add(1)
	f.Add(7)
	f.Add(8)
	f.Add(50)
	f.Add(100)
	f.Add(500)

	f.Fuzz(func(t *testing.T, length int) {
		if length < 0 || length > 1000 {
			t.Skip()
		}

		// Generate password with all requirements except length
		var builder strings.Builder

		// Add required characters first
		builder.WriteString("A") // Upper
		builder.WriteString("a") // Lower
		builder.WriteString("1") // Digit
		builder.WriteString("!") // Special

		// Fill remaining with 'x'
		remaining := length - 4
		if remaining > 0 {
			builder.WriteString(strings.Repeat("x", remaining))
		}

		password := builder.String()

		// Test validation
		isStrong := IsPasswordStrong(password)
		errors := PasswordValidationErrors(password)

		// Check length validation
		if length >= 8 {
			// Should not have length error
			for _, err := range errors {
				if strings.Contains(err, "at least 8 characters") {
					t.Errorf("Length error for password of length %d: %v", length, errors)
				}
			}
		} else {
			// Should have length error
			hasLengthError := false
			for _, err := range errors {
				if strings.Contains(err, "at least 8 characters") {
					hasLengthError = true
				}
			}
			if !hasLengthError && length < 8 {
				t.Errorf("Missing length error for password of length %d", length)
			}
		}

		// Length >= 8 and <= 128 with all requirements should be strong
		if length >= 8 && length <= 128 && !isStrong {
			t.Errorf("Password with length %d should be strong: %q, errors=%v",
				length, password, errors)
		}
	})
}

// FuzzPasswordCharacterSets tests passwords with different character combinations
// Run: go test -fuzz=FuzzPasswordCharacterSets -fuzztime=30s
func FuzzPasswordCharacterSets(f *testing.F) {
	// Seed with different character set combinations
	f.Add(true, true, true, true)     // All
	f.Add(false, false, false, false) // None
	f.Add(true, false, false, false)  // Only upper
	f.Add(false, true, false, false)  // Only lower
	f.Add(false, false, true, false)  // Only digit
	f.Add(false, false, false, true)  // Only special
	f.Add(true, true, false, false)   // Upper + Lower
	f.Add(true, true, true, false)    // No special
	f.Add(false, true, true, true)    // No upper

	f.Fuzz(func(t *testing.T, hasUpper, hasLower, hasDigit, hasSpecial bool) {
		// Build password based on flags
		var builder strings.Builder

		if hasUpper {
			builder.WriteString("ABCD")
		}
		if hasLower {
			builder.WriteString("abcd")
		}
		if hasDigit {
			builder.WriteString("1234")
		}
		if hasSpecial {
			builder.WriteString("!@#$")
		}

		password := builder.String()

		// Test validation
		isStrong := IsPasswordStrong(password)
		errors := PasswordValidationErrors(password)

		// Should be strong if at least 3 character types are present AND length >= 8
		categories := 0
		if hasUpper {
			categories++
		}
		if hasLower {
			categories++
		}
		if hasDigit {
			categories++
		}
		if hasSpecial {
			categories++
		}
		shouldBeStrong := categories >= 3 && len(password) >= 8 && len(password) <= 128

		if shouldBeStrong != isStrong {
			t.Errorf("Character set validation mismatch: password=%q, expected=%v, got=%v, errors=%v",
				password, shouldBeStrong, isStrong, errors)
		}

		// Check specific error messages only if password is not strong
		if !shouldBeStrong {
			if !hasUpper {
				hasUpperError := false
				for _, err := range errors {
					if strings.Contains(strings.ToLower(err), "uppercase") {
						hasUpperError = true
					}
				}
				if !hasUpperError {
					t.Errorf("Missing uppercase error for password: %q", password)
				}
			}

			if !hasLower {
				hasLowerError := false
				for _, err := range errors {
					if strings.Contains(strings.ToLower(err), "lowercase") {
						hasLowerError = true
					}
				}
				if !hasLowerError {
					t.Errorf("Missing lowercase error for password: %q", password)
				}
			}
		}
	})
}
