package poller

import (
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
)

func TestComputeContentHash_Deterministic(t *testing.T) {
	h1 := computeContentHash("open", "bug,enhancement", "fix the thing", []string{"comment 1", "comment 2"})
	h2 := computeContentHash("open", "bug,enhancement", "fix the thing", []string{"comment 1", "comment 2"})
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex SHA-256, got %d chars", len(h1))
	}
}

func TestComputeContentHash_LabelOrderIndependent(t *testing.T) {
	h1 := computeContentHash("open", "bug,enhancement", "body", nil)
	h2 := computeContentHash("open", "enhancement,bug", "body", nil)
	if h1 != h2 {
		t.Error("label order should not affect hash")
	}
}

func TestComputeContentHash_FieldChangeInvalidates(t *testing.T) {
	base := computeContentHash("open", "bug", "body", []string{"c1"})

	// Change state.
	if computeContentHash("closed", "bug", "body", []string{"c1"}) == base {
		t.Error("state change should invalidate hash")
	}

	// Change labels.
	if computeContentHash("open", "bug,wip", "body", []string{"c1"}) == base {
		t.Error("label change should invalidate hash")
	}

	// Change body.
	if computeContentHash("open", "bug", "different body", []string{"c1"}) == base {
		t.Error("body change should invalidate hash")
	}

	// Change comments.
	if computeContentHash("open", "bug", "body", []string{"c1", "c2"}) == base {
		t.Error("comment change should invalidate hash")
	}
}

func TestParseItemSweep_ValidJSON(t *testing.T) {
	raw := `{"objective":"Add rate limiting to API","progress":"PR open, 2 approvals pending CI"}`
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Objective != "Add rate limiting to API" {
		t.Errorf("objective = %q", resp.Objective)
	}
	if resp.Progress != "PR open, 2 approvals pending CI" {
		t.Errorf("progress = %q", resp.Progress)
	}
}

func TestParseItemSweep_FencedJSON(t *testing.T) {
	raw := "Here's the summary:\n```json\n{\"objective\":\"Fix auth bug\",\"progress\":\"Under review\"}\n```\n"
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Objective != "Fix auth bug" {
		t.Errorf("objective = %q", resp.Objective)
	}
}

func TestParseItemSweep_PlainTextFallback(t *testing.T) {
	raw := "This issue is about improving performance."
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Progress != raw {
		t.Errorf("progress = %q, want %q", resp.Progress, raw)
	}
	if resp.Objective != "" {
		t.Errorf("objective should be empty for plain text, got %q", resp.Objective)
	}
}

func TestParseItemSweep_Empty(t *testing.T) {
	resp := parseItemSweep("")
	if resp != nil {
		t.Error("expected nil for empty input")
	}
}

func TestBuildItemSweepPrompt(t *testing.T) {
	item := &db.TrackedItem{
		Owner:    "octocat",
		Repo:     "hello-world",
		Number:   42,
		ItemType: "issue",
		Title:    "Fix the thing",
		State:    "open",
		Labels:   "bug,urgent",
	}
	content := &ghpkg.ItemContent{
		Body:     "This is broken",
		Comments: []string{"I can reproduce this", "Working on a fix"},
	}

	prompt := buildItemSweepPrompt(item, content)

	if !strings.Contains(prompt, "octocat/hello-world#42") {
		t.Error("prompt should contain item reference")
	}
	if !strings.Contains(prompt, "Fix the thing") {
		t.Error("prompt should contain title")
	}
	if !strings.Contains(prompt, "open") {
		t.Error("prompt should contain state")
	}
	if !strings.Contains(prompt, "bug,urgent") {
		t.Error("prompt should contain labels")
	}
	if !strings.Contains(prompt, "This is broken") {
		t.Error("prompt should contain body")
	}
	if !strings.Contains(prompt, "I can reproduce this") {
		t.Error("prompt should contain comments")
	}
	if !strings.Contains(prompt, "Comment 1") {
		t.Error("prompt should contain comment numbers")
	}
}

func TestBuildItemSweepPrompt_TruncatesLongBody(t *testing.T) {
	item := &db.TrackedItem{
		Owner:    "o",
		Repo:     "r",
		Number:   1,
		ItemType: "issue",
		Title:    "Test",
		State:    "open",
	}
	longBody := strings.Repeat("x", 3000)
	content := &ghpkg.ItemContent{
		Body: longBody,
	}

	prompt := buildItemSweepPrompt(item, content)
	if !strings.Contains(prompt, "[truncated]") {
		t.Error("long body should be truncated")
	}
}
