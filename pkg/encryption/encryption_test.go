package encryption

import (
	"testing"
)

const validKey = "12345678901234567890123456789012"

func newService(t *testing.T) *EncryptionService {
	t.Helper()
	svc, err := NewEncryptionService(validKey)
	if err != nil {
		t.Fatalf("NewEncryptionService() error = %v", err)
	}
	return svc
}

func TestNewEncryptionService_ValidKey(t *testing.T) {
	svc, err := NewEncryptionService(validKey)
	if err != nil {
		t.Fatalf("NewEncryptionService() error = %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewEncryptionService_ShortKey(t *testing.T) {
	_, err := NewEncryptionService("tooshort")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	svc := newService(t)

	cases := []string{"hello world", "sensitive-data-123!@#", "a"}
	for _, plaintext := range cases {
		ciphertext, err := svc.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt() error = %v", err)
		}
		if plaintext == ciphertext {
			t.Fatalf("ciphertext equals plaintext for %q", plaintext)
		}

		decrypted, err := svc.Decrypt(ciphertext)
		if err != nil {
			t.Fatalf("Decrypt() error = %v", err)
		}
		if decrypted != plaintext {
			t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
		}
	}
}
