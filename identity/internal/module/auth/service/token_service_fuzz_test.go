package service

import (
	"strings"
	"testing"
)

// FuzzTokenGeneration tests token generation never crashes
// Run: go test -fuzz=FuzzTokenGeneration -fuzztime=30s
func FuzzTokenGeneration(f *testing.F) {
	service := NewTokenService()

	// Seed with various prefixes
	f.Add("")
	f.Add("reset_")
	f.Add("verify_")
	f.Add("token_")
	f.Add("🔑_")
	f.Add("very_long_prefix_" + strings.Repeat("x", 100))
	f.Add("\x00\x01\x02") // Special bytes
	f.Add("\n\t\r")       // Whitespace

	f.Fuzz(func(t *testing.T, prefix string) {
		// Test that token generation never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("GenerateTokenWithPrefix panicked: prefix=%q, panic=%v", prefix, r)
			}
		}()

		// Skip extremely long prefixes
		if len(prefix) > 500 {
			t.Skip()
		}

		token, err := service.GenerateTokenWithPrefix(prefix)

		if err != nil {
			// Error is acceptable for invalid input
			return
		}

		// Token should not be empty
		if token == "" {
			t.Errorf("Generated empty token for prefix: %q", prefix)
		}

		// Token should start with prefix
		if !strings.HasPrefix(token, prefix) {
			t.Errorf("Token doesn't have prefix: prefix=%q, token=%q", prefix, token)
		}

		// Token should have reasonable length (prefix + random part)
		if len(token) < len(prefix) {
			t.Errorf("Token shorter than prefix: prefix=%q, token=%q", prefix, token)
		}
	})
}

// FuzzUUIDValidation tests UUID validation with random strings
// Run: go test -fuzz=FuzzUUIDValidation -fuzztime=30s
func FuzzUUIDValidation(f *testing.F) {
	service := NewTokenService()

	// Seed with various UUID formats
	f.Add("550e8400-e29b-41d4-a716-446655440000") // Valid UUID
	f.Add("550e8400e29b41d4a716446655440000")     // No dashes
	f.Add("550e8400-e29b-41d4-a716")              // Incomplete
	f.Add("not-a-uuid")
	f.Add("")
	f.Add("00000000-0000-0000-0000-000000000000") // Nil UUID
	f.Add("FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF") // Max values
	f.Add("123")
	f.Add(strings.Repeat("a", 100))
	f.Add("550e8400-e29b-41d4-a716-4466554400001") // Too long

	f.Fuzz(func(t *testing.T, uuidStr string) {
		// Test that validation never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("ValidateUUID panicked: uuid=%q, panic=%v", uuidStr, r)
			}
		}()

		isValid := service.ValidateUUID(uuidStr)

		// Check basic properties
		if isValid {
			// Valid UUID should have correct length (with or without dashes)
			if len(uuidStr) != 36 && len(uuidStr) != 32 {
				t.Errorf("Invalid UUID marked as valid: %q (length %d)", uuidStr, len(uuidStr))
			}

			// If 36 chars, should have dashes in correct positions
			if len(uuidStr) == 36 {
				if uuidStr[8] != '-' || uuidStr[13] != '-' || uuidStr[18] != '-' || uuidStr[23] != '-' {
					t.Errorf("Invalid UUID format marked as valid: %q", uuidStr)
				}
			}
		}
	})
}

// FuzzTokenWithoutPrefix tests basic token generation
// Run: go test -fuzz=FuzzTokenWithoutPrefix -fuzztime=30s
func FuzzTokenWithoutPrefix(f *testing.F) {
	service := NewTokenService()

	// Add seed values - fuzz needs at least one input
	f.Add(1)

	f.Fuzz(func(t *testing.T, _ int) {
		// Test that basic token generation is stable
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("GenerateToken panicked: %v", r)
			}
		}()

		token1, err1 := service.GenerateToken()
		token2, err2 := service.GenerateToken()

		if err1 != nil || err2 != nil {
			t.Errorf("Token generation failed: err1=%v, err2=%v", err1, err2)
		}

		// Tokens should not be empty
		if token1 == "" || token2 == "" {
			t.Error("Generated empty token")
		}

		// Tokens should be different (extremely unlikely to collide)
		if token1 == token2 {
			t.Error("Generated identical tokens")
		}

		// Tokens should have reasonable length
		if len(token1) < 20 || len(token2) < 20 {
			t.Errorf("Generated short tokens: %d, %d", len(token1), len(token2))
		}
	})
}
