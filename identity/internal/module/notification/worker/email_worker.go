package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"mallow/identity/internal/module/notification/domain"
	"mallow/identity/internal/module/notification/service"

	nats "github.com/nats-io/nats.go"
)

// EmailWorker subscribes to a NATS subject, fans out jobs through an internal
// buffered channel, and dispatches them with a fixed-size goroutine pool.
type EmailWorker struct {
	nc          *nats.Conn
	sender      *service.SMTPSender
	subject     string
	concurrency int
	bufferSize  int

	ch  chan domain.EmailJob
	sub *nats.Subscription
	wg  sync.WaitGroup

	logger *slog.Logger
}

type EmailWorkerConfig struct {
	Subject     string
	Concurrency int
	BufferSize  int
}

func NewEmailWorker(
	nc *nats.Conn,
	sender *service.SMTPSender,
	cfg EmailWorkerConfig,
	logger *slog.Logger,
) *EmailWorker {
	return &EmailWorker{
		nc:          nc,
		sender:      sender,
		subject:     cfg.Subject,
		concurrency: cfg.Concurrency,
		bufferSize:  cfg.BufferSize,
		logger:      logger.With("component", "email.worker"),
	}
}

// Start opens the channel, launches worker goroutines, and subscribes to NATS.
func (w *EmailWorker) Start(_ context.Context) error {
	w.ch = make(chan domain.EmailJob, w.bufferSize)

	// Launch goroutine pool.
	for i := 0; i < w.concurrency; i++ {
		w.wg.Add(1)
		go w.processLoop()
	}

	// Subscribe to NATS subject.
	sub, err := w.nc.Subscribe(w.subject, func(msg *nats.Msg) {
		var job domain.EmailJob
		if err := json.Unmarshal(msg.Data, &job); err != nil {
			w.logger.Warn("invalid email job payload", "error", err)
			return
		}
		select {
		case w.ch <- job:
		default:
			w.logger.Warn("email channel full, dropping job",
				"to", job.To,
				"type", string(job.Type),
			)
		}
	})
	if err != nil {
		return err
	}
	w.sub = sub
	w.logger.Info("email worker started",
		"subject", w.subject,
		"concurrency", w.concurrency,
		"buffer", w.bufferSize,
	)
	return nil
}

// Stop unsubscribes from NATS, closes the channel, and waits for workers to drain.
func (w *EmailWorker) Stop(_ context.Context) error {
	if w.sub != nil {
		_ = w.sub.Unsubscribe()
	}
	close(w.ch)
	w.wg.Wait()
	w.logger.Info("email worker stopped")
	return nil
}

func (w *EmailWorker) processLoop() {
	defer w.wg.Done()
	for job := range w.ch {
		w.sender.Send(job)
	}
}
