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

func TestIsDuplicateConcernExactMatch(t *testing.T) {
	active := []db.Concern{
		{Message: "Schema drift: 'priority' column added to agent-msg messages table"},
	}
	if !isDuplicateConcern("Schema drift: 'priority' column added to agent-msg messages table", active) {
		t.Error("exact match should be duplicate")
	}
}

func TestIsDuplicateConcernReworded(t *testing.T) {
	active := []db.Concern{
		{Message: "Schema drift: 'priority' column added to agent-msg messages table. All consumers must update."},
	}
	// Similar but reworded — shares most significant words
	if !isDuplicateConcern("Schema drift detected: 'priority' column added to messages table. Consumers should update queries.", active) {
		t.Error("reworded concern should be detected as duplicate")
	}
}

func TestIsDuplicateConcernDifferent(t *testing.T) {
	active := []db.Concern{
		{Message: "Schema drift: 'priority' column added to agent-msg messages table"},
	}
	if isDuplicateConcern("Stale branch detected: feature/auth has not been updated in 14 days", active) {
		t.Error("unrelated concern should not be duplicate")
	}
}

func TestIsDuplicateConcernEmptyActive(t *testing.T) {
	if isDuplicateConcern("Some new concern", nil) {
		t.Error("should not match against empty active list")
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
