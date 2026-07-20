package signallog

import (
	"fmt"
	"strings"
	"time"
)

// buildPlaceholders returns "$start,$start+1,…,$start+n-1".
func buildPlaceholders(start, n int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ",")
}

// nullableNumeric returns nil for an empty string so PG NUMERIC stays NULL,
// preserving "no value reported" vs "explicitly zero".
func nullableNumeric(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableTime returns nil for the zero time so PG TIMESTAMPTZ stays NULL
// instead of storing 0001-01-01.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
