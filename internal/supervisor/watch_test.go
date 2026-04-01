package supervisor

import (
	"testing"
)

func TestParseWatchFilter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantTyp string
		wantVal string
	}{
		{name: "valid label", input: "label:ready", wantTyp: "label", wantVal: "ready"},
		{name: "valid milestone", input: "milestone:v2.0", wantTyp: "milestone", wantVal: "v2.0"},
		{name: "label with spaces", input: "label:needs review", wantTyp: "label", wantVal: "needs review"},
		{name: "label with hyphens", input: "label:in-progress", wantTyp: "label", wantVal: "in-progress"},
		{name: "label with underscores", input: "label:no_agent", wantTyp: "label", wantVal: "no_agent"},
		{name: "uppercase type normalised", input: "LABEL:foo", wantTyp: "label", wantVal: "foo"},
		{name: "empty value", input: "label:", wantErr: true},
		{name: "no colon", input: "labelready", wantErr: true},
		{name: "unsupported type", input: "author:alice", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "colon only", input: ":", wantErr: true},
		{name: "invalid chars in value", input: "label:foo;bar", wantErr: true},
		{name: "newline in value", input: "label:foo\nbar", wantErr: true},
		{name: "tab in value", input: "label:foo\tbar", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWatchFilter(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseWatchFilter(%q) expected error, got %+v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWatchFilter(%q) unexpected error: %v", tt.input, err)
			}
			if got.Type != tt.wantTyp {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantTyp)
			}
			if got.Value != tt.wantVal {
				t.Errorf("Value = %q, want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestIsValidFilterValue(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"ready", true},
		{"v2.0", true},
		{"in-progress", true},
		{"no_agent", true},
		{"needs review", true},
		{"CamelCase123", true},
		{"foo;bar", false},
		{"foo\nbar", false},
		{"foo\tbar", false},
		{"foo/bar", false},
		{"foo:bar", false},
		{"foo@bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := isValidFilterValue(tt.value); got != tt.want {
				t.Errorf("isValidFilterValue(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
