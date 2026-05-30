package orderlog

// nullableText returns nil for an empty string so the column stays NULL.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableNumeric returns nil for an empty/zero-less decimal string so a NUMERIC
// column stays NULL rather than being coerced.
func nullableNumeric(s string) any {
	if s == "" || s == "0" {
		return nil
	}
	return s
}

// nullableUUID returns nil unless s is a canonical 8-4-4-4-12 UUID string. Keeps
// sentinels like "manual" or "" out of the UUID-typed hand_id column.
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
