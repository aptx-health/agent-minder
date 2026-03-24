package discord

import (
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
)

func TestAnalysisEmbed(t *testing.T) {
	r := &api.AnalysisResponse{
		NewCommits:     5,
		NewMessages:    2,
		ConcernsRaised: 1,
		Analysis:       "Everything looks good. The auth middleware PR is progressing well.",
		PolledAt:       "2026-03-24 14:30:00",
	}

	embed := analysisEmbed(r)

	if embed.Title != "Analysis" {
		t.Errorf("expected title 'Analysis', got %q", embed.Title)
	}
	if embed.Color != ColorBlue {
		t.Errorf("expected blue color, got %d", embed.Color)
	}
	if embed.Description != r.Analysis {
		t.Errorf("expected analysis text in description")
	}
	if len(embed.Fields) != 2 {
		t.Errorf("expected 2 fields (activity + concerns), got %d", len(embed.Fields))
	}
}

func TestAnalysisEmbed_Truncation(t *testing.T) {
	longText := make([]byte, 5000)
	for i := range longText {
		longText[i] = 'a'
	}
	r := &api.AnalysisResponse{
		Analysis: string(longText),
		PolledAt: "2026-03-24 14:30:00",
	}

	embed := analysisEmbed(r)

	if len(embed.Description) > 4020 {
		t.Errorf("expected truncated description, got length %d", len(embed.Description))
	}
}

func TestStatusEmbed_Empty(t *testing.T) {
	embed := statusEmbed(nil)
	if embed.Description != "No tasks found." {
		t.Errorf("expected 'No tasks found.', got %q", embed.Description)
	}
}

func TestStatusEmbed_GroupedByStatus(t *testing.T) {
	tasks := []api.TaskResponse{
		{IssueNumber: 1, IssueTitle: "Task one", Status: "running"},
		{IssueNumber: 2, IssueTitle: "Task two", Status: "running"},
		{IssueNumber: 3, IssueTitle: "Task three", Status: "done", CostUSD: 1.50},
		{IssueNumber: 4, IssueTitle: "Task four", Status: "bailed"},
	}

	embed := statusEmbed(tasks)

	if embed.Description != "**4 total tasks**" {
		t.Errorf("unexpected description: %q", embed.Description)
	}
	if len(embed.Fields) != 3 {
		t.Errorf("expected 3 status groups, got %d", len(embed.Fields))
	}
	// Color should be orange (bailed tasks present).
	if embed.Color != ColorOrange {
		t.Errorf("expected orange color for bailed tasks, got %d", embed.Color)
	}
}

func TestSettingsEmbed(t *testing.T) {
	status := &api.StatusResponse{
		DeployID:  "deploy-abc",
		UptimeSec: 3661, // 1h 1m
		Config: api.DeployConfig{
			MaxAgents: 3,
			MaxTurns:  50,
			MaxBudget: 3.00,
			Analyzer:  "sonnet",
			SkipLabel: "no-agent",
			AutoMerge: true,
		},
	}

	embed := settingsEmbed(status)

	if embed.Title != "Settings" {
		t.Errorf("expected title 'Settings', got %q", embed.Title)
	}
	if len(embed.Fields) < 6 {
		t.Errorf("expected at least 6 fields, got %d", len(embed.Fields))
	}
}

func TestCostEmbed(t *testing.T) {
	metrics := &MetricsResponse{
		Spend: SpendInfo{
			Total:       12.50,
			Daily:       &CostDetail{TotalCost: 3.00, TaskCount: 2},
			Weekly:      &CostDetail{TotalCost: 10.00, TaskCount: 5},
			Overall:     &CostDetail{TotalCost: 12.50, TaskCount: 8},
			Ceiling:     50.00,
			Remaining:   37.50,
			Utilization: 0.25,
		},
		Tasks: TasksInfo{
			Total:       8,
			SuccessRate: 0.75,
		},
	}

	embed := costEmbed(metrics)

	if embed.Title != "Cost Overview" {
		t.Errorf("expected title 'Cost Overview', got %q", embed.Title)
	}
	if embed.Color != ColorGreen {
		t.Errorf("expected green color, got %d", embed.Color)
	}
}

func TestCostEmbed_BudgetPaused(t *testing.T) {
	metrics := &MetricsResponse{
		Spend: SpendInfo{
			Total: 50.00,
		},
		Tasks:        TasksInfo{Total: 10, SuccessRate: 0.8},
		BudgetPaused: true,
	}

	embed := costEmbed(metrics)

	if embed.Color != ColorRed {
		t.Errorf("expected red color when budget paused, got %d", embed.Color)
	}
}

func TestBudgetBar(t *testing.T) {
	tests := []struct {
		util float64
		want int // expected filled blocks
	}{
		{0.0, 0},
		{0.5, 10},
		{1.0, 20},
		{1.5, 20}, // capped
	}

	for _, tt := range tests {
		bar := budgetBar(tt.util)
		// Bar format: `████░░░░` with backticks — Unicode chars are multi-byte.
		runes := []rune(bar)
		if len(runes) != 22 { // 20 chars + 2 backticks
			t.Errorf("budgetBar(%.1f): expected 22 runes, got %d (%q)", tt.util, len(runes), bar)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{30, "30s"},
		{90, "1m"},
		{3661, "1h 1m"},
		{86400 + 3600, "1d 1h"},
	}

	for _, tt := range tests {
		got := formatDuration(time.Duration(tt.input) * time.Second)
		if got != tt.expected {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestStatusIcon(t *testing.T) {
	icons := map[string]string{
		"running": "🔄",
		"done":    "✅",
		"bailed":  "🏳️",
		"failed":  "❌",
		"unknown": "📋",
	}
	for status, want := range icons {
		got := statusIcon(status)
		if got != want {
			t.Errorf("statusIcon(%q) = %q, want %q", status, got, want)
		}
	}
}
