package util

// StringOrDefault returns s if it is non-empty, otherwise returns fallback.
func StringOrDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
