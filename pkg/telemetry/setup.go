package telemetry

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// Setup installs a TracerProvider as the global OTel provider and returns a shutdown function.
//
// Exporter selection (checked in order):
//  1. OTEL_EXPORTER_OTLP_ENDPOINT is set → OTLP gRPC exporter (e.g. "otel-collector:4317")
//  2. OTEL_LOG_TRACES=true              → stdout (dev/debug)
//  3. Neither                           → no-op (traces discarded)
//
// Call shutdown() on service exit (e.g. via fx OnStop or defer).
func Setup(serviceName string) (shutdown func(context.Context) error, err error) {
	res, err := resource.New(context.Background(),
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
		resource.WithHost(),
		resource.WithProcessPID(),
	)
	if err != nil {
		return nil, err
	}

	var exp sdktrace.SpanExporter
	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		exp, err = otlptracegrpc.New(context.Background(),
			otlptracegrpc.WithEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}
	case os.Getenv("OTEL_LOG_TRACES") == "true":
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, err
		}
	default:
		// No exporter — TracerProvider is installed but spans are dropped.
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		return func(ctx context.Context) error {
			shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return tp.Shutdown(shutCtx)
		}, nil
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
		shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(shutCtx)
	}, nil
}
