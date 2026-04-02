package service

import "testing"

func TestNewEncryptionService_Wrapper(t *testing.T) {
	svc, err := NewEncryptionService("12345678901234567890123456789012")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}
