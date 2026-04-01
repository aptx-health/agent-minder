package util

// StringOrDefault returns value if non-empty, otherwise fallback.
func StringOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
