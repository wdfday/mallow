package service

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"mallow/identity/internal/module/notification/domain"

	nats "github.com/nats-io/nats.go"
)

// NATSEmailService implements EmailService by publishing jobs to a NATS subject.
// The actual sending is done by EmailWorker.
type NATSEmailService struct {
	nc      *nats.Conn
	subject string
	logger  *slog.Logger
}

func NewNATSEmailService(nc *nats.Conn, subject string, logger *slog.Logger) EmailService {
	return &NATSEmailService{
		nc:      nc,
		subject: subject,
		logger:  logger.With("component", "nats.email"),
	}
}

func (s *NATSEmailService) SendVerificationEmail(to, name, token string) error {
	return s.publish(domain.EmailJob{
		Type:  domain.EmailJobVerification,
		To:    to,
		Name:  name,
		Token: token,
	})
}

func (s *NATSEmailService) SendPasswordResetEmail(to, name, token string) error {
	return s.publish(domain.EmailJob{
		Type:  domain.EmailJobPasswordReset,
		To:    to,
		Name:  name,
		Token: token,
	})
}

func (s *NATSEmailService) publish(job domain.EmailJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal email job: %w", err)
	}
	if err := s.nc.Publish(s.subject, data); err != nil {
		s.logger.Error("failed to publish email job",
			"type", string(job.Type),
			"to", job.To,
			"error", err,
		)
		return fmt.Errorf("nats publish: %w", err)
	}
	s.logger.Debug("email job published",
		"type", string(job.Type),
		"to", job.To,
	)
	return nil
}
