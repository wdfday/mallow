package helper

// StringPtr converts a string to *string, returning nil when empty.
func StringPtr(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}
