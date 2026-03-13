package poller

import (
	"testing"
	"time"
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

func TestRelativeAge(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours", 2 * time.Hour, "2h"},
		{"hours_and_minutes", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"days", 48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := time.Now().Add(-tt.offset).UTC().Format("2006-01-02 15:04:05")
			got := relativeAge(ts)
			if got != tt.want {
				t.Errorf("relativeAge(%q) = %q, want %q", ts, got, tt.want)
			}
		})
	}
}

func TestRelativeAge_InvalidTimestamp(t *testing.T) {
	got := relativeAge("not-a-timestamp")
	if got != "??" {
		t.Errorf("relativeAge(invalid) = %q, want %q", got, "??")
	}
}
