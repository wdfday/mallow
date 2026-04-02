package infra

import (
	"log/slog"

	"go.uber.org/fx"

	pkgtelemetry "mallow/pkg/telemetry"
)

// provideTracerProvider installs the OTel SDK TracerProvider via pkg/telemetry.Setup.
// Exporter is selected from env:
//   - OTEL_EXPORTER_OTLP_ENDPOINT → OTLP gRPC
//   - OTEL_LOG_TRACES=true         → stdout
//   - (default)                    → no-op (spans discarded)
func provideTracerProvider(lc fx.Lifecycle, logger *slog.Logger) error {
	shutdown, err := pkgtelemetry.Setup("investment")
	if err != nil {
		return err
	}
	logger.Info("OTel TracerProvider installed", "service", "investment")

	lc.Append(fx.Hook{
		OnStop: shutdown,
	})
	return nil
}
