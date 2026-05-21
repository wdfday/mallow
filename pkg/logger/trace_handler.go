package logger

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceHandler wraps any slog.Handler and injects OTel trace_id / span_id
// fields into every log record that has an active span in its context.
//
// Only works with the context-aware slog APIs (InfoContext, ErrorContext, etc.)
// or when the handler is called via slog.Logger.Log(ctx, ...).
// Calls without context (slog.Info, slog.Error) receive background context and
// will not carry trace_id — which is expected for background goroutine logging.
type TraceHandler struct {
	inner slog.Handler
}

// NewTraceHandler wraps inner with OTel trace context injection.
func NewTraceHandler(inner slog.Handler) *TraceHandler {
	return &TraceHandler{inner: inner}
}

func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{inner: h.inner.WithGroup(name)}
}
