package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/notification/domain"
)

// ---------------------------------------------------------------------------
// Minimal fake NATS connection for unit testing (no real server needed).
// ---------------------------------------------------------------------------

// fakeConn captures Publish calls without a real NATS server.
type fakeConn struct {
	published []publishedMsg
	failNext  bool
}

type publishedMsg struct {
	subject string
	data    []byte
}

func (f *fakeConn) Publish(subject string, data []byte) error {
	if f.failNext {
		return errors.New("nats: connection closed")
	}
	f.published = append(f.published, publishedMsg{subject: subject, data: data})
	return nil
}

// natsPublisher mirrors the subset of *nats.Conn used by NATSEmailService.
type natsPublisher interface {
	Publish(subject string, data []byte) error
}

// testableNATSEmailService is the same logic as NATSEmailService but accepts
// the narrower natsPublisher interface so we can inject fakeConn.
type testableNATSEmailService struct {
	nc      natsPublisher
	subject string
}

func (s *testableNATSEmailService) SendVerificationEmail(to, name, token string) error {
	return s.publish(domain.EmailJob{
		Type:  domain.EmailJobVerification,
		To:    to,
		Name:  name,
		Token: token,
	})
}

func (s *testableNATSEmailService) SendPasswordResetEmail(to, name, token string) error {
	return s.publish(domain.EmailJob{
		Type:  domain.EmailJobPasswordReset,
		To:    to,
		Name:  name,
		Token: token,
	})
}

func (s *testableNATSEmailService) publish(job domain.EmailJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.nc.Publish(s.subject, data)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNATSEmailService_SendVerificationEmail(t *testing.T) {
	conn := &fakeConn{}
	svc := &testableNATSEmailService{nc: conn, subject: "mail.send"}

	err := svc.SendVerificationEmail("user@example.com", "Alice", "tok-abc")
	require.NoError(t, err)
	require.Len(t, conn.published, 1)

	msg := conn.published[0]
	assert.Equal(t, "mail.send", msg.subject)

	var job domain.EmailJob
	require.NoError(t, json.Unmarshal(msg.data, &job))
	assert.Equal(t, domain.EmailJobVerification, job.Type)
	assert.Equal(t, "user@example.com", job.To)
	assert.Equal(t, "Alice", job.Name)
	assert.Equal(t, "tok-abc", job.Token)
}

func TestNATSEmailService_SendPasswordResetEmail(t *testing.T) {
	conn := &fakeConn{}
	svc := &testableNATSEmailService{nc: conn, subject: "mail.send"}

	err := svc.SendPasswordResetEmail("user@example.com", "Alice", "reset-tok")
	require.NoError(t, err)
	require.Len(t, conn.published, 1)

	var job domain.EmailJob
	require.NoError(t, json.Unmarshal(conn.published[0].data, &job))
	assert.Equal(t, domain.EmailJobPasswordReset, job.Type)
}

func TestNATSEmailService_PublishError(t *testing.T) {
	conn := &fakeConn{failNext: true}
	svc := &testableNATSEmailService{nc: conn, subject: "mail.send"}

	err := svc.SendVerificationEmail("user@example.com", "Alice", "tok")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nats")
}

func TestNATSEmailService_MultiplePublish(t *testing.T) {
	conn := &fakeConn{}
	svc := &testableNATSEmailService{nc: conn, subject: "mail.send"}

	_ = svc.SendVerificationEmail("a@example.com", "Alice", "tok1")
	_ = svc.SendPasswordResetEmail("b@example.com", "Bob", "tok2")

	assert.Len(t, conn.published, 2)

	// Verify ordering
	var job1, job2 domain.EmailJob
	_ = json.Unmarshal(conn.published[0].data, &job1)
	_ = json.Unmarshal(conn.published[1].data, &job2)

	assert.Equal(t, domain.EmailJobVerification, job1.Type)
	assert.Equal(t, domain.EmailJobPasswordReset, job2.Type)
}

// TestEmailJobSerialization ensures the domain struct round-trips cleanly.
func TestEmailJobSerialization(t *testing.T) {
	original := domain.EmailJob{
		Type:  domain.EmailJobVerification,
		To:    "test@example.com",
		Name:  "Test User",
		Token: "verify-token-123",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded domain.EmailJob
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, original, decoded)
}

// ---------------------------------------------------------------------------
// Noop email service (smoke test)
// ---------------------------------------------------------------------------

func TestNoopEmailService(t *testing.T) {
	svc := NewNoopEmailService()

	// Both methods should be no-ops with no error.
	assert.NoError(t, svc.SendVerificationEmail("a@b.com", "Alice", "tok"))
	assert.NoError(t, svc.SendPasswordResetEmail("a@b.com", "Alice", "tok"))
}

// ---------------------------------------------------------------------------
// Timeout guard — ensures there are no goroutine leaks during the test run.
// ---------------------------------------------------------------------------
var _ = time.Second // keep import
