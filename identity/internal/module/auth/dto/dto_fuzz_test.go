package dto

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzRegisterRequestJSON tests JSON parsing for RegisterRequest
// Run: go test -fuzz=FuzzRegisterRequestJSON -fuzztime=30s
func FuzzRegisterRequestJSON(f *testing.F) {
	// Seed with valid and invalid JSON
	f.Add(`{"email":"test@example.com","password":"Pass123!","full_name":"Test"}`)
	f.Add(`{"email":"test@example.com"}`) // Missing fields
	f.Add(`{}`)                           // Empty object
	f.Add(`[]`)                           // Wrong type
	f.Add(`null`)
	f.Add(`""`)
	f.Add(`{"email":null}`)
	f.Add(`{"email":123}`)                                              // Wrong type
	f.Add(`{"email":"` + strings.Repeat("a", 10000) + `@example.com"}`) // Very long
	f.Add(`{"email":"test@example.com","extra_field":"unexpected"}`)    // Extra fields

	f.Fuzz(func(t *testing.T, jsonData string) {
		// Test that JSON unmarshaling never panics
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("JSON unmarshal panicked: data=%q, panic=%v", jsonData, r)
			}
		}()

		var req RegisterRequest
		err := json.Unmarshal([]byte(jsonData), &req)

		// Error is acceptable for invalid JSON
		if err != nil {
			return
		}

		// If unmarshaling succeeded, test marshaling back
		data, err := json.Marshal(req)
		if err != nil {
			t.Errorf("Failed to marshal back: %v", err)
		}

		// Result should be valid JSON
		if !json.Valid(data) {
			t.Errorf("Marshaled data is not valid JSON: %s", data)
		}
	})
}

// FuzzLoginRequestJSON tests JSON parsing for LoginRequest
// Run: go test -fuzz=FuzzLoginRequestJSON -fuzztime=30s
func FuzzLoginRequestJSON(f *testing.F) {
	// Seed corpus
	f.Add(`{"email":"test@example.com","password":"Pass123!"}`)
	f.Add(`{"email":"test@example.com"}`)
	f.Add(`{"password":"Pass123!"}`)
	f.Add(`{}`)
	f.Add(`{"email":"","password":""}`)
	f.Add(`{"email":null,"password":null}`)

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("JSON unmarshal panicked: data=%q, panic=%v", jsonData, r)
			}
		}()

		var req LoginRequest
		_ = json.Unmarshal([]byte(jsonData), &req)

		// Test that we can marshal it back
		_, _ = json.Marshal(req)
	})
}

// FuzzEmailValidation tests email format validation
// Run: go test -fuzz=FuzzEmailValidation -fuzztime=30s
func FuzzEmailValidation(f *testing.F) {
	// Seed with various email formats
	f.Add("test@example.com")
	f.Add("invalid.email")
	f.Add("@example.com")
	f.Add("test@")
	f.Add("")
	f.Add("test..test@example.com")
	f.Add("test@example..com")
	f.Add("test+tag@example.com")
	f.Add("test@subdomain.example.com")
	f.Add("test@123.456.789.012")
	f.Add("тест@example.com")                             // Cyrillic
	f.Add("test@тест.com")                                // Cyrillic domain
	f.Add("test@example.com" + strings.Repeat("x", 1000)) // Very long

	f.Fuzz(func(t *testing.T, email string) {
		// Test email in RegisterRequest
		req := RegisterRequest{
			Email:    email,
			Password: "ValidPass123!",
			FullName: "Test User",
		}

		// Marshal to JSON
		data, err := json.Marshal(req)
		if err != nil {
			// Skip if marshaling fails
			return
		}

		// Unmarshal back
		var req2 RegisterRequest
		err = json.Unmarshal(data, &req2)
		if err != nil {
			t.Errorf("Failed to unmarshal: email=%q, err=%v", email, err)
		}

		// Email should be preserved
		if req.Email != req2.Email {
			t.Errorf("Email changed: original=%q, unmarshaled=%q", req.Email, req2.Email)
		}
	})
}

// FuzzChangePasswordRequestJSON tests change password request parsing
// Run: go test -fuzz=FuzzChangePasswordRequestJSON -fuzztime=30s
func FuzzChangePasswordRequestJSON(f *testing.F) {
	// Seed corpus
	f.Add(`{"current_password":"Old123!","new_password":"New123!"}`)
	f.Add(`{"current_password":""}`)
	f.Add(`{"new_password":""}`)
	f.Add(`{}`)
	f.Add(`{"current_password":null,"new_password":null}`)

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Unmarshal panicked: %v", r)
			}
		}()

		var req ChangePasswordRequest
		_ = json.Unmarshal([]byte(jsonData), &req)
	})
}

// FuzzGoogleAuthRequestJSON tests Google auth request parsing
// Run: go test -fuzz=FuzzGoogleAuthRequestJSON -fuzztime=30s
func FuzzGoogleAuthRequestJSON(f *testing.F) {
	// Seed corpus
	f.Add(`{"token":"valid_google_token"}`)
	f.Add(`{"token":""}`)
	f.Add(`{}`)
	f.Add(`{"token":null}`)
	f.Add(`{"token":"` + strings.Repeat("x", 10000) + `"}`)

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Unmarshal panicked: %v", r)
			}
		}()

		var req GoogleAuthRequest
		err := json.Unmarshal([]byte(jsonData), &req)

		if err == nil {
			// Test marshaling back
			_, err = json.Marshal(req)
			if err != nil {
				t.Errorf("Failed to marshal back: %v", err)
			}
		}
	})
}

// FuzzJSONEscaping tests proper JSON escaping
// Run: go test -fuzz=FuzzJSONEscaping -fuzztime=30s
func FuzzJSONEscaping(f *testing.F) {
	// Seed with special characters
	f.Add("test@example.com")
	f.Add("test\"quote@example.com")
	f.Add("test\\backslash@example.com")
	f.Add("test\nline@example.com")
	f.Add("test\tline@example.com")
	f.Add("test\x00null@example.com")
	f.Add("test<script>@example.com")

	f.Fuzz(func(t *testing.T, email string) {
		// Skip extremely long inputs
		if len(email) > 1000 {
			t.Skip()
		}

		req := RegisterRequest{
			Email:    email,
			Password: "Pass123!",
			FullName: "Test User",
		}

		// Marshal
		data, err := json.Marshal(req)
		if err != nil {
			return
		}

		// Should be valid JSON
		if !json.Valid(data) {
			t.Errorf("Invalid JSON produced for email: %q", email)
		}

		// Unmarshal back
		var req2 RegisterRequest
		err = json.Unmarshal(data, &req2)
		if err != nil {
			t.Errorf("Failed to unmarshal: %v", err)
		}

		// Data should be preserved exactly
		if req.Email != req2.Email {
			t.Errorf("Data corruption: original=%q, result=%q", req.Email, req2.Email)
		}
	})
}
