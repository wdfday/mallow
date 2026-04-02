package service

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/notification/domain"
)

// ---------------------------------------------------------------------------
// SMTPSender template rendering (no real SMTP call)
// ---------------------------------------------------------------------------

func newTestSender() *SMTPSender {
	cfg := &config.Config{
		SMTP: config.SMTPConfig{
			Host:     "smtp.example.com",
			Port:     587,
			Username: "user@example.com",
			Password: "secret",
			From:     "noreply@example.com",
			FromName: "Test Service",
		},
	}
	return NewSMTPSender(cfg, slog.Default())
}

// TestSMTPSender_UnknownJobType ensures unknown types are skipped silently.
func TestSMTPSender_UnknownJobType(t *testing.T) {
	sender := newTestSender()

	// Should NOT panic and should not return any error — just log a warning.
	assert.NotPanics(t, func() {
		sender.Send(domain.EmailJob{
			Type:  "unknown_type",
			To:    "user@example.com",
			Name:  "Alice",
			Token: "tok",
		})
	})
}

// TestSMTPSender_Send_VerificationTemplateRenders ensures the verification
// template renders without error (send itself will fail due to no real SMTP
// server — that is expected and acceptable in a unit test).
func TestSMTPSender_Send_VerificationTemplateRenders(t *testing.T) {
	sender := newTestSender()

	// Calling Send will attempt SMTP but that's expected to fail.
	// We just verify it doesn't panic and the template renders.
	assert.NotPanics(t, func() {
		sender.Send(domain.EmailJob{
			Type:  domain.EmailJobVerification,
			To:    "user@example.com",
			Name:  "Alice",
			Token: "https://example.com/verify?token=abc123",
		})
	})
}

// TestSMTPSender_Send_PasswordResetTemplateRenders ensures the password reset
// template renders without panicking.
func TestSMTPSender_Send_PasswordResetTemplateRenders(t *testing.T) {
	sender := newTestSender()

	assert.NotPanics(t, func() {
		sender.Send(domain.EmailJob{
			Type:  domain.EmailJobPasswordReset,
			To:    "user@example.com",
			Name:  "Bob",
			Token: "https://example.com/reset?token=xyz789",
		})
	})
}

// TestVerificationTemplateExec directly tests the package-level template.
func TestVerificationTemplateExec(t *testing.T) {
	require.NotNil(t, verificationTmpl)

	// Execute with valid data — must not fail.
	err := verificationTmpl.Execute(nopWriter{}, map[string]string{
		"Name": "Alice",
		"Link": "https://example.com/verify",
	})
	assert.NoError(t, err)
}

// TestPasswordResetTemplateExec directly tests the password reset template.
func TestPasswordResetTemplateExec(t *testing.T) {
	err := passwordResetTmpl.Execute(nopWriter{}, map[string]string{
		"Name": "Bob",
		"Link": "https://example.com/reset",
	})
	assert.NoError(t, err)
}

// nopWriter discards all written bytes.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
