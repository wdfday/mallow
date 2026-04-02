package notification

import (
	"context"
	"log/slog"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/notification/service"
	"mallow/identity/internal/module/notification/worker"

	nats "github.com/nats-io/nats.go"
	"go.uber.org/fx"
)

// Module wires the NATS-backed email pipeline:
//
//	EmailService (publisher) → NATS subject → EmailWorker (subscriber + goroutine pool) → SMTPSender
var Module = fx.Module("notification",
	fx.Provide(
		// SMTP sender — performs actual delivery.
		service.NewSMTPSender,

		// NATSEmailService — publishes jobs to NATS; implements EmailService.
		fx.Annotate(
			provideNATSEmailService,
			fx.As(new(service.EmailService)),
		),

		// EmailWorker — subscribes NATS, buffers via channel, dispatches with goroutine pool.
		provideEmailWorker,
	),
	fx.Invoke(registerEmailWorkerLifecycle),
)

func provideNATSEmailService(nc *nats.Conn, cfg *config.Config, logger *slog.Logger) service.EmailService {
	return service.NewNATSEmailService(nc, cfg.Mail.Subject, logger)
}

func provideEmailWorker(nc *nats.Conn, sender *service.SMTPSender, cfg *config.Config, logger *slog.Logger) *worker.EmailWorker {
	return worker.NewEmailWorker(nc, sender, worker.EmailWorkerConfig{
		Subject:     cfg.Mail.Subject,
		Concurrency: cfg.Mail.Concurrency,
		BufferSize:  cfg.Mail.BufferSize,
	}, logger)
}

func registerEmailWorkerLifecycle(lc fx.Lifecycle, w *worker.EmailWorker, logger *slog.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info("Starting email worker...")
			return w.Start(ctx)
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Stopping email worker...")
			return w.Stop(ctx)
		},
	})
}
