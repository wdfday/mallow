// Package telemetry wraps the OpenTelemetry API.
// Services call telemetry.Tracer("my-service") to get a named Tracer.
// By default the global TracerProvider is a no-op; install an SDK provider
// in each service's infra module (e.g. stdouttrace, OTLP) to emit spans.
package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Tracer returns a named tracer from the global TracerProvider.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}
