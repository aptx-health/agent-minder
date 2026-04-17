package supervisor

import (
	"testing"
)

func TestParseWatchFilter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantTyp  string
		wantVals []string
	}{
		{name: "valid label", input: "label:ready", wantTyp: "label", wantVals: []string{"ready"}},
		{name: "valid milestone", input: "milestone:v2.0", wantTyp: "milestone", wantVals: []string{"v2.0"}},
		{name: "label with spaces", input: "label:needs review", wantTyp: "label", wantVals: []string{"needs review"}},
		{name: "label with hyphens", input: "label:in-progress", wantTyp: "label", wantVals: []string{"in-progress"}},
		{name: "label with underscores", input: "label:no_agent", wantTyp: "label", wantVals: []string{"no_agent"}},
		{name: "uppercase type normalised", input: "LABEL:foo", wantTyp: "label", wantVals: []string{"foo"}},
		{name: "multi-label AND", input: "label:agent-ready,ux", wantTyp: "label", wantVals: []string{"agent-ready", "ux"}},
		{name: "three labels AND", input: "label:a,b,c", wantTyp: "label", wantVals: []string{"a", "b", "c"}},
		{name: "milestone with comma kept intact", input: "milestone:v2.0", wantTyp: "milestone", wantVals: []string{"v2.0"}},
		{name: "empty value", input: "label:", wantErr: true},
		{name: "no colon", input: "labelready", wantErr: true},
		{name: "unsupported type", input: "author:alice", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "colon only", input: ":", wantErr: true},
		{name: "invalid chars in value", input: "label:foo;bar", wantErr: true},
		{name: "newline in value", input: "label:foo\nbar", wantErr: true},
		{name: "tab in value", input: "label:foo\tbar", wantErr: true},
		{name: "empty label in list", input: "label:foo,,bar", wantErr: true},
		{name: "trailing comma", input: "label:foo,", wantErr: true},
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
			if len(got.Values) != len(tt.wantVals) {
				t.Fatalf("Values = %v, want %v", got.Values, tt.wantVals)
			}
			for i, v := range got.Values {
				if v != tt.wantVals[i] {
					t.Errorf("Values[%d] = %q, want %q", i, v, tt.wantVals[i])
				}
			}
		})
	}
}

func TestHasAllLabels(t *testing.T) {
	tests := []struct {
		name     string
		issue    []string
		required []string
		want     bool
	}{
		{"single match", []string{"bug", "ux"}, []string{"ux"}, true},
		{"AND match", []string{"agent-ready", "ux", "high-priority"}, []string{"agent-ready", "ux"}, true},
		{"missing one", []string{"agent-ready"}, []string{"agent-ready", "ux"}, false},
		{"case insensitive", []string{"Agent-Ready", "UX"}, []string{"agent-ready", "ux"}, true},
		{"empty required", []string{"bug"}, nil, false},
		{"empty issue labels", nil, []string{"bug"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasAllLabels(tt.issue, tt.required); got != tt.want {
				t.Errorf("hasAllLabels(%v, %v) = %v, want %v", tt.issue, tt.required, got, tt.want)
			}
		})
	}
}

func TestResolveAgentSpecificityOrder(t *testing.T) {
	// More-specific routes (more labels) should match first.
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-agent"},
	})

	// Issue with both labels should route to ux-agent (more specific).
	agent := sup.resolveAgentForIssue([]string{"agent-ready", "ux"})
	if agent != "ux-agent" {
		t.Errorf("resolveAgentForIssue([agent-ready, ux]) = %q, want %q", agent, "ux-agent")
	}

	// Issue with only agent-ready should route to autopilot.
	agent = sup.resolveAgentForIssue([]string{"agent-ready"})
	if agent != "autopilot" {
		t.Errorf("resolveAgentForIssue([agent-ready]) = %q, want %q", agent, "autopilot")
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
