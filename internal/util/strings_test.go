package util

import "testing"

func TestStringOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		fallback string
		want     string
	}{
		{"non-empty returns s", "hello", "default", "hello"},
		{"empty returns fallback", "", "default", "default"},
		{"both empty returns empty", "", "", ""},
		{"whitespace returns s", "  ", "default", "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StringOrDefault(tt.s, tt.fallback)
			if got != tt.want {
				t.Errorf("StringOrDefault(%q, %q) = %q, want %q", tt.s, tt.fallback, got, tt.want)
			}
		})
	}
}
