package util

import "testing"

func TestStringOrDefault(t *testing.T) {
	tests := []struct {
		value, fallback, want string
	}{
		{"hello", "default", "hello"},
		{"", "default", "default"},
		{"", "", ""},
		{"value", "", "value"},
	}
	for _, tt := range tests {
		got := StringOrDefault(tt.value, tt.fallback)
		if got != tt.want {
			t.Errorf("StringOrDefault(%q, %q) = %q, want %q", tt.value, tt.fallback, got, tt.want)
		}
	}
}
