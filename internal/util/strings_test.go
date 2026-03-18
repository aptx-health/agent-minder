package util

import "testing"

func TestStringOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		fallback string
		want     string
	}{
		{
			name:     "non-empty string returns s",
			s:        "hello",
			fallback: "default",
			want:     "hello",
		},
		{
			name:     "empty string returns fallback",
			s:        "",
			fallback: "default",
			want:     "default",
		},
		{
			name:     "both empty returns empty",
			s:        "",
			fallback: "",
			want:     "",
		},
		{
			name:     "whitespace-only string returns s",
			s:        "  ",
			fallback: "default",
			want:     "  ",
		},
		{
			name:     "empty fallback with non-empty s",
			s:        "hello",
			fallback: "",
			want:     "hello",
		},
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
