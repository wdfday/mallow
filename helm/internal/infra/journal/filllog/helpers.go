package filllog

import (
	"fmt"
	"strings"
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

// nullableUUID returns nil if s is empty or not a canonical 8-4-4-4-12 UUID string.
// Prevents sentinel values like "manual" from hitting UUID-typed columns.
func nullableUUID(s string) any {
	if len(s) != 36 {
		return nil
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return nil
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return nil
		}
	}
	return s
}
