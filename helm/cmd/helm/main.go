package main

import (
	"context"
	"log"
	"runtime"
	"time"

	pyroscope "github.com/grafana/pyroscope-go"

	"mallow/helm/internal/app"
	"mallow/helm/internal/config"
	pkglogger "mallow/pkg/logger"
	pkgtelemetry "mallow/pkg/telemetry"
)

func main() {
	closeLog := pkglogger.Setup("helm")
	defer closeLog()

	cfg := config.Load()

	// ── Pyroscope continuous profiling ────────────────────────────────
	// Goroutine + block profiling is the primary target — detects leaks
	// and channel stalls in the hand runtime / registry loops.
	//
	// Block and mutex profiling require explicit rate > 0 — Go runtime
	// disables them by default (rate = 0 → no samples collected).
	// Set before pyroscope.Start so the first profile window is complete.
	if cfg.Server.PyroscopeURL != "" {
		runtime.SetBlockProfileRate(1)     // sample every blocking event
		runtime.SetMutexProfileFraction(1) // sample every mutex contention
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
		log.Printf("pyroscope profiling active: %s", cfg.Server.PyroscopeURL)
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
