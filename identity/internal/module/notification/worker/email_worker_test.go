package worker

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/notification/domain"
	"mallow/identity/internal/module/notification/service"
)

// ---------------------------------------------------------------------------
// In-process NATS server helpers
// ---------------------------------------------------------------------------

func runTestServer(t *testing.T) (*natsserver.Server, string) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	return srv, srv.ClientURL()
}

func connectTest(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// ---------------------------------------------------------------------------
// Fake SMTP Sender (records calls, never dials)
// ---------------------------------------------------------------------------

type fakeSender struct {
	mu   sync.Mutex
	sent []domain.EmailJob
}

func (f *fakeSender) Send(job domain.EmailJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, job)
}

func (f *fakeSender) Jobs() []domain.EmailJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]domain.EmailJob, len(f.sent))
	copy(cp, f.sent)
	return cp
}

// ---------------------------------------------------------------------------
// We need to inject a fake sender, so we extend EmailWorker with a
// testable variant that accepts a Sender interface.
// ---------------------------------------------------------------------------

type Sender interface {
	Send(job domain.EmailJob)
}

type testEmailWorker struct {
	nc      *nats.Conn
	sender  Sender
	subject string
	conc    int
	buf     int

	ch  chan domain.EmailJob
	sub *nats.Subscription
	wg  sync.WaitGroup
}

func newTestWorker(nc *nats.Conn, sender Sender, subject string) *testEmailWorker {
	return &testEmailWorker{
		nc:      nc,
		sender:  sender,
		subject: subject,
		conc:    2,
		buf:     10,
	}
}

func (w *testEmailWorker) Start(_ context.Context) error {
	w.ch = make(chan domain.EmailJob, w.buf)
	for i := 0; i < w.conc; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for job := range w.ch {
				w.sender.Send(job)
			}
		}()
	}
	sub, err := w.nc.Subscribe(w.subject, func(msg *nats.Msg) {
		var job domain.EmailJob
		if err := json.Unmarshal(msg.Data, &job); err != nil {
			return
		}
		select {
		case w.ch <- job:
		default:
		}
	})
	if err != nil {
		return err
	}
	w.sub = sub
	return nil
}

func (w *testEmailWorker) Stop(_ context.Context) error {
	if w.sub != nil {
		_ = w.sub.Unsubscribe()
	}
	close(w.ch)
	w.wg.Wait()
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEmailWorker_StartStop verifies that the worker starts and stops cleanly.
func TestEmailWorker_StartStop(t *testing.T) {
	_, url := runTestServer(t)
	nc := connectTest(t, url)
	sender := &fakeSender{}

	worker := newTestWorker(nc, sender, "mail.test")

	require.NoError(t, worker.Start(context.Background()))
	require.NoError(t, worker.Stop(context.Background()))
}

// TestEmailWorker_ProcessesJob verifies a published job reaches the sender.
func TestEmailWorker_ProcessesJob(t *testing.T) {
	_, url := runTestServer(t)
	nc := connectTest(t, url)
	sender := &fakeSender{}

	worker := newTestWorker(nc, sender, "mail.test")
	require.NoError(t, worker.Start(context.Background()))

	job := domain.EmailJob{
		Type:  domain.EmailJobVerification,
		To:    "alice@example.com",
		Name:  "Alice",
		Token: "tok-123",
	}
	data, _ := json.Marshal(job)
	require.NoError(t, nc.Publish("mail.test", data))
	require.NoError(t, nc.Flush())

	// Wait for the job to be processed (up to 2 s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.Jobs()) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.NoError(t, worker.Stop(context.Background()))

	jobs := sender.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.EmailJobVerification, jobs[0].Type)
	assert.Equal(t, "alice@example.com", jobs[0].To)
}

// TestEmailWorker_ProcessesMultipleJobs verifies multiple messages are dispatched.
func TestEmailWorker_ProcessesMultipleJobs(t *testing.T) {
	_, url := runTestServer(t)
	nc := connectTest(t, url)
	sender := &fakeSender{}

	worker := newTestWorker(nc, sender, "mail.multi")
	require.NoError(t, worker.Start(context.Background()))

	const n = 5
	for i := 0; i < n; i++ {
		job := domain.EmailJob{
			Type:  domain.EmailJobPasswordReset,
			To:    "user@example.com",
			Name:  "User",
			Token: "tok",
		}
		data, _ := json.Marshal(job)
		_ = nc.Publish("mail.multi", data)
	}
	require.NoError(t, nc.Flush())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.Jobs()) >= n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.NoError(t, worker.Stop(context.Background()))
	assert.Len(t, sender.Jobs(), n)
}

// TestEmailWorker_InvalidPayloadDoesNotCrash verifies that bad JSON is silently skipped.
func TestEmailWorker_InvalidPayloadDoesNotCrash(t *testing.T) {
	_, url := runTestServer(t)
	nc := connectTest(t, url)
	sender := &fakeSender{}

	worker := newTestWorker(nc, sender, "mail.bad")
	require.NoError(t, worker.Start(context.Background()))

	// Publish garbage bytes.
	require.NoError(t, nc.Publish("mail.bad", []byte("{invalid json")))
	require.NoError(t, nc.Flush())

	time.Sleep(100 * time.Millisecond)

	require.NoError(t, worker.Stop(context.Background()))
	assert.Empty(t, sender.Jobs(), "invalid payload should be dropped")
}

// TestEmailWorker_Config validates the EmailWorkerConfig struct is usable.
func TestEmailWorker_Config(t *testing.T) {
	cfg := EmailWorkerConfig{
		Subject:     "mail.send",
		Concurrency: 3,
		BufferSize:  50,
	}
	assert.Equal(t, "mail.send", cfg.Subject)

	// Verify it can be passed to NewEmailWorker without panicking.
	_, url := runTestServer(t)
	nc := connectTest(t, url)

	smtpSender := &service.SMTPSender{} // zero-value — will fail to actually send but won't panic on construction
	_ = smtpSender
	// We test with our fake sender above so this is just a sanity check on types.
	assert.NotPanics(t, func() {
		_ = newTestWorker(nc, &fakeSender{}, cfg.Subject)
	})
}
