package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	pyroscope "github.com/grafana/pyroscope-go"

	"mallow/helm/internal/app"
	"mallow/helm/internal/config"
	pkgtelemetry "mallow/pkg/telemetry"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()

	// ── Pyroscope continuous profiling ────────────────────────────────
	// Goroutine + block profiling is the primary target — detects leaks
	// and channel stalls in the hand runtime / registry loops.
	if cfg.Server.PyroscopeURL != "" {
		profiler, err := pyroscope.Start(pyroscope.Config{
			ApplicationName: "helm",
			ServerAddress:   cfg.Server.PyroscopeURL,
			Logger:          nil, // silence pyroscope internal logs
			ProfileTypes: []pyroscope.ProfileType{
				pyroscope.ProfileCPU,
				pyroscope.ProfileGoroutines,
				pyroscope.ProfileAllocObjects,
				pyroscope.ProfileAllocSpace,
				pyroscope.ProfileInuseObjects,
				pyroscope.ProfileInuseSpace,
				pyroscope.ProfileBlockCount,
				pyroscope.ProfileBlockDuration,
				pyroscope.ProfileMutexCount,
				pyroscope.ProfileMutexDuration,
			},
		})
		if err != nil {
			log.Fatalf("pyroscope start failed: %v", err)
		}
		defer profiler.Stop()
		slog.Info("pyroscope profiling active", "server", cfg.Server.PyroscopeURL)
	}

	// ── OpenTelemetry tracing ─────────────────────────────────────────
	shutdownTracing, err := pkgtelemetry.Setup("helm")
	if err != nil {
		log.Fatalf("otel setup failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(ctx)
	}()

	app.New().Run()
	log.Print("helm stopped")
}
