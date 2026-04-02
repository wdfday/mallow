package service

// EmailService defines the email notification interface.
type EmailService interface {
	SendVerificationEmail(to, name, token string) error
	SendPasswordResetEmail(to, name, token string) error
}

// NoopEmailService is a no-op implementation for development / testing.
type NoopEmailService struct{}

func NewNoopEmailService() EmailService {
	return &NoopEmailService{}
}

func (n *NoopEmailService) SendVerificationEmail(_, _, _ string) error  { return nil }
func (n *NoopEmailService) SendPasswordResetEmail(_, _, _ string) error { return nil }
