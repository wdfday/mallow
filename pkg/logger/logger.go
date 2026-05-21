// Package logger provides a project-wide slog logger factory.
//
// Output format: JSON with OpenTelemetry-compatible attribute names.
// Standard fields added to every record:
//
//	service.name  – name of the service (set at construction)
//	env           – deployment environment (LOG_ENV, default "development")
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// contextKey is an unexported type for context keys in this package.
type contextKey struct{}

// New returns a JSON slog.Logger tagged with the given service name.
// Log level is read from LOG_LEVEL env var (debug|info|warn|error), default info.
func New(service string) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	env := os.Getenv("LOG_ENV")
	if env == "" {
		env = "development"
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: level == slog.LevelDebug,
		// ReplaceAttr renames keys to OTel semantic conventions.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.MessageKey:
				a.Key = "body"
			case slog.LevelKey:
				a.Key = "severity"
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.SourceKey:
				a.Key = "code"
			}
			return a
		},
	})

	return slog.New(handler).With(
		slog.String("service.name", service),
		slog.String("env", env),
	)
}

// FromContext retrieves the logger stored in ctx, falling back to Default.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithContext stores logger in ctx.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// Default initialises the global slog default logger with JSON + OTel format.
// Call once in main() for services that use slog.Info / slog.Error directly.
func Default(service string) {
	slog.SetDefault(New(service))
}

// Setup initialises the global slog default logger.
//
// Format selection:
//   - LOG_FORMAT=json → JSON with OTel field names + trace_id injection (recommended for production)
//   - default          → text (readable for local development)
//
// Other env vars: LOG_LEVEL (debug|info|warn|error), LOG_FILE (path).
// When LOG_FILE is set, logs go to both stdout and the file (append mode).
// Returns a cleanup func that must be deferred in main().
func Setup(service string) func() {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	var w io.Writer = os.Stdout
	var closeFile func()

	if path := os.Getenv("LOG_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			slog.Warn("logger: could not open log file, using stdout only", "path", path, "err", err)
		} else {
			w = io.MultiWriter(os.Stdout, f)
			closeFile = func() { _ = f.Close() }
		}
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: level == slog.LevelDebug,
	}

	var h slog.Handler
	if os.Getenv("LOG_FORMAT") == "json" {
		// JSON format for production: Loki can parse fields, trace_id enables
		// Loki→Tempo correlation via the derivedField regex '"trace_id":"(\w+)"'.
		opts.ReplaceAttr = func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.MessageKey:
				a.Key = "msg"
			case slog.LevelKey:
				a.Key = "level"
			case slog.TimeKey:
				a.Key = "time"
			}
			return a
		}
		h = NewTraceHandler(slog.NewJSONHandler(w, opts))
	} else {
		h = slog.NewTextHandler(w, opts)
	}

	slog.SetDefault(slog.New(h).With("service", service))

	if closeFile != nil {
		return closeFile
	}
	return func() {}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
