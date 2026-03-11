package poller

import (
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
)

func TestParseAnalysisRawJSON(t *testing.T) {
	raw := `{"analysis":"All repos stable","concerns":[{"severity":"warning","message":"Schema drift"}],"bus_message":{"topic":"proj/coord","message":"Heads up"}}`
	resp := parseAnalysis(raw)

	if resp.Analysis != "All repos stable" {
		t.Errorf("analysis = %q", resp.Analysis)
	}
	if len(resp.Concerns) != 1 || resp.Concerns[0].Severity != "warning" {
		t.Errorf("concerns = %+v", resp.Concerns)
	}
	if resp.BusMessage == nil || resp.BusMessage.Topic != "proj/coord" {
		t.Errorf("bus_message = %+v", resp.BusMessage)
	}
}

func TestParseAnalysisCodeFence(t *testing.T) {
	raw := "Here's the analysis:\n```json\n{\"analysis\":\"Fenced response\",\"concerns\":[]}\n```\n"
	resp := parseAnalysis(raw)
	if resp.Analysis != "Fenced response" {
		t.Errorf("analysis = %q", resp.Analysis)
	}
	if resp.BusMessage != nil {
		t.Errorf("expected no bus_message, got %+v", resp.BusMessage)
	}
}

func TestParseAnalysisPlainText(t *testing.T) {
	raw := "Everything looks fine. No concerns."
	resp := parseAnalysis(raw)
	if resp.Analysis != raw {
		t.Errorf("analysis = %q, want %q", resp.Analysis, raw)
	}
	if resp.BusMessage != nil {
		t.Error("expected no bus_message for plain text")
	}
	if len(resp.Concerns) != 0 {
		t.Error("expected no concerns for plain text")
	}
}

func TestParseAnalysisEmpty(t *testing.T) {
	resp := parseAnalysis("")
	if resp.Analysis != "" {
		t.Errorf("analysis = %q, want empty", resp.Analysis)
	}
}

func TestMatchExistingConcernExactMatch(t *testing.T) {
	active := []db.Concern{
		{ID: 1, Message: "Schema drift: 'priority' column added to agent-msg messages table"},
	}
	if matchExistingConcern("Schema drift: 'priority' column added to agent-msg messages table", active) == 0 {
		t.Error("exact match should find existing concern")
	}
}

func TestMatchExistingConcernReworded(t *testing.T) {
	active := []db.Concern{
		{ID: 2, Message: "Schema drift: 'priority' column added to agent-msg messages table. All consumers must update."},
	}
	if matchExistingConcern("Schema drift detected: 'priority' column added to messages table. Consumers should update queries.", active) == 0 {
		t.Error("reworded concern should match existing")
	}
}

func TestMatchExistingConcernDifferent(t *testing.T) {
	active := []db.Concern{
		{ID: 3, Message: "Schema drift: 'priority' column added to agent-msg messages table"},
	}
	if matchExistingConcern("Stale branch detected: feature/auth has not been updated in 14 days", active) != 0 {
		t.Error("unrelated concern should not match")
	}
}

func TestMatchExistingConcernEmptyActive(t *testing.T) {
	if matchExistingConcern("Some new concern", nil) != 0 {
		t.Error("should not match against empty active list")
	}
}

func TestValidSeverity(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"info", "info"},
		{"warning", "warning"},
		{"warn", "warning"},
		{"danger", "danger"},
		{"critical", "danger"},
		{"unknown", "info"},
		{"", "info"},
	}
	for _, tt := range tests {
		if got := validSeverity(tt.input); got != tt.want {
			t.Errorf("validSeverity(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseAnalysisNoBusMessage(t *testing.T) {
	raw := `{"analysis":"Quiet period","concerns":[{"severity":"info","message":"No activity"}]}`
	resp := parseAnalysis(raw)
	if resp.Analysis != "Quiet period" {
		t.Errorf("analysis = %q", resp.Analysis)
	}
	if resp.BusMessage != nil {
		t.Error("expected no bus_message")
	}
	if len(resp.Concerns) != 1 {
		t.Errorf("concerns len = %d, want 1", len(resp.Concerns))
	}
}
