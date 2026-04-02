package service

import (
	"log/slog"
	"testing"
)

// FuzzPasswordHashing tests password hashing with random inputs
// Run: go test -fuzz=FuzzPasswordHashing -fuzztime=30s
func FuzzPasswordHashing(f *testing.F) {
	service := &PasswordService{
		cost:   10,
		logger: slog.Default(),
	}

	// Seed corpus with known inputs
	f.Add("Password123!")
	f.Add("Short1!")
	f.Add("VeryLongPasswordWith123!@#$%^&*()")
	f.Add("密码123!")        // Unicode
	f.Add("پاس‌ورد123!")   // RTL text
	f.Add("🔐Password123!") // Emoji
	f.Add("")              // Empty string
	f.Add("a")             // Single char

	f.Fuzz(func(t *testing.T, password string) {
		// Skip extremely long inputs to avoid timeout
		if len(password) > 1000 {
			t.Skip()
		}

		// Test that hashing never panics
		hashedPassword, err := service.HashPassword(password)

		if err != nil {
			// Bcrypt might fail on empty or very short passwords
			// This is expected behavior
			return
		}

		// If hashing succeeded, verify should work
		err = service.VerifyPassword(hashedPassword, password)
		if err != nil {
			t.Errorf("VerifyPassword failed for password that was successfully hashed: %v", err)
		}

		// Verify wrong password fails
		wrongPassword := password + "wrong"
		err = service.VerifyPassword(hashedPassword, wrongPassword)
		if err == nil && password != wrongPassword {
			t.Errorf("VerifyPassword should fail for wrong password")
		}
	})
}

// FuzzPasswordStrengthValidation tests password validation with random inputs
// Run: go test -fuzz=FuzzPasswordStrengthValidation -fuzztime=30s
func FuzzPasswordStrengthValidation(f *testing.F) {
	service := &PasswordService{
		logger: slog.Default(),
	}

	// Seed corpus
	f.Add("Password123!")
	f.Add("weak")
	f.Add("NoNumbers!")
	f.Add("nouppercaseorspecial123")
	f.Add("NOLOWERCASE123!")
	f.Add("NoSpecialChars123")
	f.Add("")
	f.Add("abc")
	f.Add("🔐🔑🔓") // Emoji only

	f.Fuzz(func(t *testing.T, password string) {
		// Test that validation never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Validation panicked with password: %q, panic: %v", password, r)
			}
		}()

		isValid := service.IsValidPassword(password)
		errors := service.ValidatePasswordStrength(password)

		// Consistency check: if valid, should have no errors
		if isValid && len(errors) > 0 {
			t.Errorf("Password marked as valid but has validation errors: %v", errors)
		}

		// If not valid, should have at least one error
		if !isValid && len(errors) == 0 {
			t.Errorf("Password marked as invalid but has no validation errors")
		}
	})
}

// FuzzPasswordVerification tests password verification with random inputs
// Run: go test -fuzz=FuzzPasswordVerification -fuzztime=30s
func FuzzPasswordVerification(f *testing.F) {
	service := &PasswordService{
		cost:   10,
		logger: slog.Default(),
	}

	// Pre-hash some passwords for testing
	validHash, _ := service.HashPassword("ValidPassword123!")

	// Seed corpus
	f.Add(validHash, "ValidPassword123!")
	f.Add(validHash, "WrongPassword")
	f.Add("invalid_hash", "any_password")
	f.Add("", "")
	f.Add("$2a$10$invalid", "password")

	f.Fuzz(func(t *testing.T, hashedPassword, plainPassword string) {
		// Test that verification never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("VerifyPassword panicked: hash=%q, plain=%q, panic=%v",
					hashedPassword, plainPassword, r)
			}
		}()

		// Just test it doesn't crash
		_ = service.VerifyPassword(hashedPassword, plainPassword)
	})
}

// FuzzPasswordLength tests various password lengths
// Run: go test -fuzz=FuzzPasswordLength -fuzztime=30s
func FuzzPasswordLength(f *testing.F) {
	service := &PasswordService{
		cost:   10,
		logger: slog.Default(),
	}

	// Seed with various lengths
	f.Add(0)
	f.Add(1)
	f.Add(7)
	f.Add(8)
	f.Add(72) // bcrypt max
	f.Add(73)
	f.Add(100)
	f.Add(500)

	f.Fuzz(func(t *testing.T, length int) {
		if length < 0 || length > 1000 {
			t.Skip()
		}

		// Generate password of specific length
		password := make([]byte, length)
		for i := range password {
			password[i] = 'A' + byte(i%26) // Simple pattern
		}

		passwordStr := string(password)

		// Test hashing
		hashedPassword, err := service.HashPassword(passwordStr)

		if length == 0 {
			// Empty password should work with bcrypt
			if err != nil {
				return // Expected
			}
		}

		if length > 72 {
			// Bcrypt has 72 byte limit (but shouldn't crash)
			return
		}

		if err == nil {
			// Verify it works
			err = service.VerifyPassword(hashedPassword, passwordStr)
			if err != nil {
				t.Errorf("Verification failed for length %d", length)
			}
		}
	})
}

// FuzzPasswordSpecialCharacters tests passwords with various special chars
// Run: go test -fuzz=FuzzPasswordSpecialCharacters -fuzztime=30s
func FuzzPasswordSpecialCharacters(f *testing.F) {
	service := &PasswordService{
		cost:   10,
		logger: slog.Default(),
	}

	// Seed with various special characters
	f.Add("Pass!123")
	f.Add("Pass@123")
	f.Add("Pass#123")
	f.Add("Pass$123")
	f.Add("Pass%123")
	f.Add("Pass^123")
	f.Add("Pass&123")
	f.Add("Pass*123")
	f.Add("Pass\t123")     // Tab
	f.Add("Pass\n123")     // Newline
	f.Add("Pass\x00123")   // Null byte
	f.Add("Pass\u0000123") // Unicode null

	f.Fuzz(func(t *testing.T, password string) {
		// Skip very long inputs
		if len(password) > 500 {
			t.Skip()
		}

		// Test that hashing handles all special characters
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Hashing panicked with password containing special chars: %q, panic: %v",
					password, r)
			}
		}()

		hashedPassword, err := service.HashPassword(password)
		if err != nil {
			return
		}

		// Verify it works
		err = service.VerifyPassword(hashedPassword, password)
		if err != nil {
			t.Errorf("Verification failed for password with special chars: %q", password)
		}
	})
}

// FuzzPasswordUnicode tests passwords with Unicode characters
// Run: go test -fuzz=FuzzPasswordUnicode -fuzztime=30s
func FuzzPasswordUnicode(f *testing.F) {
	service := &PasswordService{
		cost:   10,
		logger: slog.Default(),
	}

	// Seed with various Unicode
	f.Add("Password123!密码")
	f.Add("пароль123!")
	f.Add("パスワード123!")
	f.Add("كلمة السر123!")
	f.Add("🔐🔑Pass123!")
	f.Add("Café123!")
	f.Add("Naïve123!")

	f.Fuzz(func(t *testing.T, password string) {
		if len(password) > 500 {
			t.Skip()
		}

		// Test Unicode handling
		hashedPassword, err := service.HashPassword(password)
		if err != nil {
			return
		}

		err = service.VerifyPassword(hashedPassword, password)
		if err != nil {
			t.Errorf("Unicode password verification failed: %q", password)
		}
	})
}
